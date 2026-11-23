// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command i18ncheck is the automatic completeness check of the message
// catalogs (release criterion 7.2, FR-063): for the web interface, the
// command line (its messages are the error taxonomy's) and the transfer
// slip it fails on
//
//   - a key the code uses that a language does not define (EN or FR);
//   - a key a language defines that the other lacks;
//   - a key defined but never used (orphan);
//   - a placeholder ({{.Name}}) present in one language and not in the
//     other, or a plural entry in one language and a plain one in the other.
//
// Usage, from the repository root:
//
//	go run ./tools/i18ncheck
//
// How a key counts as USED is deliberately conservative, so that the check
// has no false negatives on the "missing" side and few false positives on
// the "orphan" side:
//
//   - the first string argument of a call to T, TN or t (Go) and of
//     {{.T "key"}} / {{.TN "key"}} (templates) is a use, and must exist;
//   - any other Go string literal equal to a key is a use (keys held in a
//     variable or a table before being translated);
//   - a key built at run time from a literal prefix counts as used when it
//     starts with that prefix: {{.T (printf "scan.level_%s" .X)}} in a
//     template, "plan.sig_" + x or fmt.Sprintf("plan.sig_%s", x) in Go. The
//     prefix must contain a dot, so "a" + b elsewhere is not a family;
//   - catalogs whose keys are derived (the taxonomy's per-code sections and
//     exit-code rows) expand their sources in code, see taxonomyDerived;
//   - what the rules above cannot see, and what is defined ahead of its
//     use, is declared with a reason in tools/i18ncheck/known-unused.txt
//     (an exact key or a prefix). An entry that matches no unused key any
//     more fails the check: the ledger cannot outlive its cause.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Languages every catalog must carry.
var languages = []string{"en", "fr"}

// catalog describes one set of message files and where its keys are used.
type catalog struct {
	// Name labels the findings.
	Name string
	// Dir holds <lang>.yaml for each language, relative to the root.
	Dir string
	// GoDirs are scanned (non-test .go files, not recursive) for uses.
	GoDirs []string
	// TemplateDirs are scanned recursively for {{.T "key"}} uses.
	TemplateDirs []string
	// CallNames are the functions and methods whose first argument is a key.
	CallNames []string
	// Derived expands uses that are not literal keys (see taxonomyDerived).
	Derived func(src *sources) []string
}

// The catalogs of the product. English is the reference language.
var catalogs = []catalog{
	{
		Name:         "ui",
		Dir:          "internal/ui/i18n",
		GoDirs:       []string{"internal/ui"},
		TemplateDirs: []string{"internal/ui/templates"},
		CallNames:    []string{"T", "TN"},
	},
	{
		Name:      "slip",
		Dir:       "internal/slip/i18n",
		GoDirs:    []string{"internal/slip"},
		CallNames: []string{"t"},
	},
	{
		Name:    "taxonomy",
		Dir:     "internal/taxonomy/messages",
		GoDirs:  []string{"internal/taxonomy"},
		Derived: taxonomyDerived,
	},
}

// entry is one message of one language.
type entry struct {
	// Plural is true for a map of plural forms.
	Plural bool
	// Placeholders is the set of {{.Name}} used by the message, all plural
	// forms together.
	Placeholders map[string]bool
}

var (
	actionRe      = regexp.MustCompile(`\{\{.*?\}\}`)
	placeholderRe = regexp.MustCompile(`\.([\p{L}_][\p{L}\p{N}_]*)`)
	templateUseRe = regexp.MustCompile(`\.TN?\s+"([^"]+)"`)
	// printfPrefixRe finds the literal head of a key built with printf in a
	// template, or with Sprintf in Go: "scan.level_%s" -> "scan.level_".
	printfPrefixRe = regexp.MustCompile(`"([^"%]+)%[sdv]`)
)

func placeholdersOf(text string, into map[string]bool) {
	for _, action := range actionRe.FindAllString(text, -1) {
		for _, m := range placeholderRe.FindAllStringSubmatch(action, -1) {
			into[m[1]] = true
		}
	}
}

// loadMessages reads one catalog file.
func loadMessages(r io.Reader) (map[string]entry, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]entry, len(doc))
	for key, v := range doc {
		e := entry{Placeholders: map[string]bool{}}
		switch val := v.(type) {
		case string:
			placeholdersOf(val, e.Placeholders)
		case map[string]any:
			e.Plural = true
			for _, form := range val {
				s, ok := form.(string)
				if !ok {
					return nil, fmt.Errorf("key %q: a plural form is not a string", key)
				}
				placeholdersOf(s, e.Placeholders)
			}
		default:
			return nil, fmt.Errorf("key %q: neither a string nor a map of plural forms", key)
		}
		out[key] = e
	}
	return out, nil
}

// sources is what the scan of the code found.
type sources struct {
	// Called holds the keys passed literally to a translation call.
	Called map[string]bool
	// Literals holds every Go string literal of the scanned files.
	Literals map[string]bool
	// Prefixes holds the literal prefixes of keys built at run time.
	Prefixes map[string]bool
	// ConstNames holds the values of string constants by constant name.
	ConstNames map[string]string
}

func newSources() *sources {
	return &sources{Called: map[string]bool{}, Literals: map[string]bool{}, Prefixes: map[string]bool{}, ConstNames: map[string]string{}}
}

// scanGoDir parses the non-test Go files of dir.
func scanGoDir(dir string, callNames map[string]bool, into *sources) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if s, err := strconv.Unquote(x.Value); err == nil {
						into.Literals[s] = true
					}
				}
			case *ast.BinaryExpr:
				if lit, ok := x.X.(*ast.BasicLit); ok && x.Op == token.ADD && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						addPrefix(into, s)
					}
				}
			case *ast.CallExpr:
				if calleeName(x.Fun) == "Sprintf" && len(x.Args) > 0 {
					if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							if m := printfPrefixRe.FindStringSubmatch(`"` + s); m != nil {
								addPrefix(into, m[1])
							}
						}
					}
				}
				if len(x.Args) == 0 || !callNames[calleeName(x.Fun)] {
					return true
				}
				if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						into.Called[s] = true
					}
				}
			case *ast.ValueSpec:
				for i, id := range x.Names {
					if i >= len(x.Values) {
						break
					}
					if lit, ok := x.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							into.ConstNames[id.Name] = s
						}
					}
				}
			}
			return true
		})
	}
	return nil
}

// addPrefix records a run-time key prefix; only key-shaped ones count (a
// dot in them), so "a" + b elsewhere in the code does not become a family.
func addPrefix(into *sources, prefix string) {
	if len(prefix) >= 3 && strings.Contains(prefix, ".") {
		into.Prefixes[prefix] = true
	}
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// scanTemplates finds {{.T "key"}} and {{.TN "key"}} in the files of dir.
func scanTemplates(root fs.FS, dir string, into *sources) error {
	return fs.WalkDir(root, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		for _, m := range templateUseRe.FindAllStringSubmatch(string(raw), -1) {
			into.Called[m[1]] = true
		}
		for _, m := range printfPrefixRe.FindAllStringSubmatch(string(raw), -1) {
			addPrefix(into, m[1])
		}
		return nil
	})
}

var codeRe = regexp.MustCompile(`^TBY-[A-Z]{3,4}-\d{3}$`)

// taxonomyDerived expands the taxonomy's implicit keys: every error code
// literal owns <code>.what/.cause/.action, every ExitName* constant owns
// exit.<name>.title/.meaning (exit.go builds the ids by concatenation).
func taxonomyDerived(src *sources) []string {
	var keys []string
	for lit := range src.Literals {
		if codeRe.MatchString(lit) {
			for _, section := range []string{"what", "cause", "action"} {
				keys = append(keys, lit+"."+section)
			}
		}
	}
	for name, value := range src.ConstNames {
		if strings.HasPrefix(name, "ExitName") {
			keys = append(keys, "exit."+value+".title", "exit."+value+".meaning")
		}
	}
	return keys
}

// unusedEntry is a declared unused key, or key prefix.
type unusedEntry struct {
	Catalog string
	Prefix  string
	Reason  string
}

// parseKnownUnused reads known-unused.txt: "<catalog> <prefix> :: <reason>".
func parseKnownUnused(r io.Reader) ([]unusedEntry, error) {
	var out []unusedEntry
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, reason, ok := strings.Cut(line, " :: ")
		fields := strings.Fields(head)
		if !ok || len(fields) != 2 || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("known-unused.txt:%d: want \"<catalog> <prefix> :: <reason>\"", n)
		}
		out = append(out, unusedEntry{Catalog: fields[0], Prefix: fields[1], Reason: strings.TrimSpace(reason)})
	}
	return out, sc.Err()
}

// analyze compares the catalogs and the uses; it returns the findings.
func analyze(name string, msgs map[string]map[string]entry, src *sources, derived []string, families []unusedEntry) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s: "+format, append([]any{name}, args...)...))
	}

	all := map[string]bool{}
	for _, lang := range languages {
		for k := range msgs[lang] {
			all[k] = true
		}
	}
	keys := sortedKeys(all)

	// Defined in one language only; structure and placeholder drift.
	ref := languages[0]
	for _, k := range keys {
		for _, lang := range languages {
			if _, ok := msgs[lang][k]; !ok {
				add("key %q is missing from %s.yaml", k, lang)
			}
		}
		a, aok := msgs[ref][k]
		for _, lang := range languages[1:] {
			b, bok := msgs[lang][k]
			if !aok || !bok {
				continue
			}
			if a.Plural != b.Plural {
				add("key %q is a plural entry in one language and a plain message in the other (%s vs %s)", k, ref, lang)
			}
			for _, p := range diff(a.Placeholders, b.Placeholders) {
				add("key %q: placeholder {{.%s}} is in %s only", k, p, ref)
			}
			for _, p := range diff(b.Placeholders, a.Placeholders) {
				add("key %q: placeholder {{.%s}} is in %s only", k, p, lang)
			}
		}
	}

	// Used but not defined, per language.
	used := map[string]bool{}
	for k := range src.Called {
		used[k] = true
	}
	for _, k := range derived {
		used[k] = true
	}
	for _, k := range sortedKeys(used) {
		for _, lang := range languages {
			if _, ok := msgs[lang][k]; !ok {
				add("key %q is used by the code and undefined in %s.yaml", k, lang)
			}
		}
	}

	// Defined but never used.
	matched := map[string]bool{}
	for _, k := range keys {
		if used[k] || src.Literals[k] || hasPrefixIn(k, src.Prefixes) {
			continue
		}
		covered := false
		for _, f := range families {
			if f.Catalog == name && strings.HasPrefix(k, f.Prefix) {
				covered = true
				matched[f.Catalog+" "+f.Prefix] = true
			}
		}
		if !covered {
			add("key %q is defined and never used (orphan)", k)
		}
	}
	for _, f := range families {
		if f.Catalog != name {
			continue
		}
		if !matched[f.Catalog+" "+f.Prefix] {
			add("known-unused.txt: entry %q matches no unused key; remove it", f.Prefix)
		}
	}
	return problems
}

func hasPrefixIn(k string, prefixes map[string]bool) bool {
	for p := range prefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkCatalog loads and analyzes one catalog under root.
func checkCatalog(root string, c *catalog, families []unusedEntry) ([]string, error) {
	msgs := map[string]map[string]entry{}
	for _, lang := range languages {
		f, err := os.Open(filepath.Join(root, c.Dir, lang+".yaml")) //nolint:gosec // G304: the path is built from the catalogs table of this file
		if err != nil {
			return nil, err
		}
		m, err := loadMessages(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s/%s.yaml: %w", c.Dir, lang, err)
		}
		msgs[lang] = m
	}
	names := map[string]bool{}
	for _, n := range c.CallNames {
		names[n] = true
	}
	src := newSources()
	for _, d := range c.GoDirs {
		if err := scanGoDir(filepath.Join(root, d), names, src); err != nil {
			return nil, err
		}
	}
	for _, d := range c.TemplateDirs {
		if err := scanTemplates(os.DirFS(root), filepath.ToSlash(d), src); err != nil {
			return nil, err
		}
	}
	var derived []string
	if c.Derived != nil {
		derived = c.Derived(src)
	}
	return analyze(c.Name, msgs, src, derived, families), nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("i18ncheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	knownUnused := flags.String("known-unused", "tools/i18ncheck/known-unused.txt", "ledger of declared unused keys, relative to -root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	var families []unusedEntry
	if f, err := os.Open(filepath.Join(*root, *knownUnused)); err == nil {
		families, err = parseKnownUnused(f)
		_ = f.Close()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "i18ncheck:", err)
			return 2
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintln(stderr, "i18ncheck:", err)
		return 2
	}
	failed := 0
	for i := range catalogs {
		c := &catalogs[i]
		problems, err := checkCatalog(*root, c, families)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "i18ncheck:", err)
			return 2
		}
		if len(problems) == 0 {
			_, _ = fmt.Fprintf(stdout, "ok   %s\n", c.Name)
			continue
		}
		for _, p := range problems {
			_, _ = fmt.Fprintln(stdout, "FAIL", p)
		}
		failed += len(problems)
	}
	if failed > 0 {
		_, _ = fmt.Fprintf(stdout, "i18n completeness: %d problem(s)\n", failed)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "i18n completeness: no missing or orphaned keys, placeholders agree (EN, FR)")
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
