// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command errorsref writes the published error reference from the error
// taxonomy:
//
//	go run ./tools/errorsref          # rewrite both pages
//	go run ./tools/errorsref -check   # fail if a page has drifted
//
// What happened, probable cause and corrective action come from the
// catalog the binary embeds (internal/taxonomy/messages), so the website
// states exactly what the UI, the CLI and the API print. The class, the
// exit code and the HTTP status come from the catalog entry. What only
// the page knows — whether the condition is fixable offline, what it
// blocks, and the occasional note — lives beside this tool, in
// reference/<lang>.yaml, one entry per code.
//
// The page is never edited by hand: a code without a reference entry, a
// reference entry without a code, or a page that differs from what this
// tool writes fails the test and the -check mode.
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

//go:embed reference/en.yaml reference/fr.yaml
var referenceFS embed.FS

// pages maps a catalog language to the page this tool owns.
var pages = map[string]string{
	"en": "website/src/content/docs/docs/reference/errors.md",
	"fr": "website/src/content/docs/fr/docs/reference/errors.md",
}

// reference is what the page says beyond the catalog, in one language.
type reference struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Order is the page's position in the Reference section.
	Order int `yaml:"order"`
	// Intro is the Markdown between the front matter and the index.
	Intro string `yaml:"intro"`
	// ExitCodes frames the exit-code table, which the tool prints from
	// the taxonomy between the markers internal/taxonomy's test looks for.
	ExitCodes exitSection `yaml:"exitCodes"`
	Labels    labels      `yaml:"labels"`
	// Domains lists the sections in page order; a catalog code whose
	// domain is absent here fails the generation.
	Domains []domain `yaml:"domains"`
	// Codes holds the per-code facts the catalog does not carry.
	Codes map[string]codeRef `yaml:"codes"`
}

type labels struct {
	Index        string `yaml:"index"`
	IndexDomain  string `yaml:"indexDomain"`
	IndexCodes   string `yaml:"indexCodes"`
	What         string `yaml:"what"`
	Cause        string `yaml:"cause"`
	Action       string `yaml:"action"`
	Note         string `yaml:"note"`
	Class        string `yaml:"class"`
	Exit         string `yaml:"exit"`
	HTTP         string `yaml:"http"`
	Offline      string `yaml:"offline"`
	Blocks       string `yaml:"blocks"`
	Operational  string `yaml:"operational"`
	Policy       string `yaml:"policy"`
	Verification string `yaml:"verification"`
	// Colon is the label terminator, typography included (" :" in French).
	Colon string `yaml:"colon"`
}

type exitSection struct {
	Title  string `yaml:"title"`
	Before string `yaml:"before"`
	After  string `yaml:"after"`
}

type domain struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
}

type codeRef struct {
	Offline string   `yaml:"offline"`
	Blocks  string   `yaml:"blocks"`
	Details []detail `yaml:"details"`
}

// detail is one extra bullet under a code. Label defaults to the
// language's "Note".
type detail struct {
	Label string `yaml:"label"`
	Text  string `yaml:"text"`
}

func main() {
	check := flag.Bool("check", false, "compare instead of writing; exit 1 on drift")
	flag.Parse()
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	drift := false
	for _, lang := range taxonomy.Languages() {
		want, err := generate(lang)
		if err != nil {
			fail(err)
		}
		path := filepath.Join(root, pages[lang])
		if !*check {
			if err := os.WriteFile(path, want, 0o644); err != nil { //nolint:gosec // G306: a published page, not a secret
				fail(err)
			}
			fmt.Printf("%s written\n", pages[lang])
			continue
		}
		got, err := os.ReadFile(path) //nolint:gosec // G304: path comes from the fixed page list
		if err != nil {
			fail(err)
		}
		if !bytes.Equal(normalize(got), want) {
			drift = true
			fmt.Fprintf(os.Stderr, "errorsref: %s differs from the catalog: run `go run ./tools/errorsref` and commit the result\n", pages[lang])
		}
	}
	if drift {
		os.Exit(1)
	}
	if *check {
		fmt.Println("error reference in sync with the catalog, in every language")
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "errorsref:", err)
	os.Exit(1)
}

// normalize removes what a checkout may have changed without changing
// the content: line endings.
func normalize(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func loadReference(lang string) (*reference, error) {
	raw, err := referenceFS.ReadFile("reference/" + lang + ".yaml")
	if err != nil {
		return nil, err
	}
	var ref reference
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&ref); err != nil {
		return nil, fmt.Errorf("reference/%s.yaml: %w", lang, err)
	}
	return &ref, nil
}

// generate renders the whole page for one language.
func generate(lang string) ([]byte, error) {
	ref, err := loadReference(lang)
	if err != nil {
		return nil, err
	}
	byDomain := map[string][]taxonomy.Entry{}
	seen := map[string]bool{}
	for _, entry := range taxonomy.All() {
		code := string(entry.Code)
		seen[code] = true
		byDomain[domainOf(code)] = append(byDomain[domainOf(code)], entry)
	}
	if err := crossCheck(lang, ref, seen, byDomain); err != nil {
		return nil, err
	}

	l := &ref.Labels
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %s\ndescription: %s\nsidebar:\n  order: %d\n---\n\n", ref.Title, ref.Description, ref.Order)
	b.WriteString(strings.TrimSpace(ref.Intro))
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "## %s\n\n%s\n\n%s\n%s%s\n\n%s\n\n", ref.ExitCodes.Title,
		strings.TrimSpace(ref.ExitCodes.Before), exitTableStart,
		taxonomy.ExitCodeTable(lang), exitTableEnd, strings.TrimSpace(ref.ExitCodes.After))

	fmt.Fprintf(&b, "## %s\n\n| %s | %s |\n|---|---|\n", l.Index, l.IndexDomain, l.IndexCodes)
	for _, d := range ref.Domains {
		entries := byDomain[d.ID]
		first, last := string(entries[0].Code), string(entries[len(entries)-1].Code)
		span := link(first)
		if len(entries) > 1 {
			span += " … " + link(last)
		}
		fmt.Fprintf(&b, "| %s (`TBY-%s`) | %s (%d) |\n", d.Title, d.ID, span, len(entries))
	}
	b.WriteString("\n")

	for _, d := range ref.Domains {
		fmt.Fprintf(&b, "## %s (TBY-%s)\n\n", d.Title, d.ID)
		for _, entry := range byDomain[d.ID] {
			writeEntry(&b, lang, l, entry, ref.Codes[string(entry.Code)])
		}
	}
	return []byte(strings.TrimRight(b.String(), "\n") + "\n"), nil
}

func writeEntry(b *strings.Builder, lang string, l *labels, entry taxonomy.Entry, c codeRef) {
	params := taxonomy.Params{}
	for _, p := range entry.Params {
		params[p] = paramOpen + p + paramClose
	}
	msg := taxonomy.Localize(lang, taxonomy.New(entry.Code, params))

	fmt.Fprintf(b, "### %s\n\n", entry.Code)
	bullet := func(label, text string) {
		fmt.Fprintf(b, "- **%s%s** %s\n", label, l.Colon, text)
	}
	bullet(l.What, markdown(msg.What))
	bullet(l.Cause, markdown(msg.Cause))
	bullet(l.Action, markdown(msg.Action))
	for _, d := range c.Details {
		label := d.Label
		if label == "" {
			label = l.Note
		}
		bullet(label, strings.TrimSpace(d.Text))
	}
	class := fmt.Sprintf("%s (%s %d)", className(l, entry.Class), l.Exit, entry.Class.ExitCode())
	if entry.HTTPStatus != 0 {
		class += fmt.Sprintf(" · **%s%s** %d", l.HTTP, l.Colon, entry.HTTPStatus)
	}
	bullet(l.Class, class)
	fmt.Fprintf(b, "- **%s%s** %s · **%s%s** %s\n\n", l.Offline, l.Colon, strings.TrimSpace(c.Offline), l.Blocks, l.Colon, strings.TrimSpace(c.Blocks))
}

func className(l *labels, c taxonomy.Class) string {
	switch c {
	case taxonomy.ClassPolicy:
		return l.Policy
	case taxonomy.ClassVerification:
		return l.Verification
	default:
		return l.Operational
	}
}

// crossCheck refuses a page that would be silently incomplete: every code
// has a reference entry with both facts, every reference entry has a
// code, every domain has a section and every section has a code.
func crossCheck(lang string, ref *reference, seen map[string]bool, byDomain map[string][]taxonomy.Entry) error {
	var problems []string
	sections := map[string]bool{}
	for _, d := range ref.Domains {
		sections[d.ID] = true
		if len(byDomain[d.ID]) == 0 {
			problems = append(problems, "domain "+d.ID+" has a section but no code in the catalog")
		}
	}
	for id := range byDomain {
		if !sections[id] {
			problems = append(problems, "domain "+id+" has codes in the catalog but no section title")
		}
	}
	for code := range seen {
		c, ok := ref.Codes[code]
		switch {
		case !ok:
			problems = append(problems, code+" has no reference entry")
		case strings.TrimSpace(c.Offline) == "" || strings.TrimSpace(c.Blocks) == "":
			problems = append(problems, code+" needs both `offline` and `blocks`")
		}
	}
	for code := range ref.Codes {
		if !seen[code] {
			problems = append(problems, code+" has a reference entry but is not in the catalog")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("tools/errorsref/reference/%s.yaml:\n  %s", lang, strings.Join(problems, "\n  "))
}

func domainOf(code string) string {
	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		return code
	}
	return parts[1]
}

func link(code string) string {
	return "[`" + code + "`](#" + strings.ToLower(code) + ")"
}

// The markers internal/taxonomy's exit-code test finds the table between.
const (
	exitTableStart = "<!-- generated:exit-codes -->"
	exitTableEnd   = "<!-- /generated:exit-codes -->"
)

// A message parameter is rendered through sentinels so that its final
// form can depend on where it lands: bare inside a code span, emphasized
// and escaped in running text.
const (
	paramOpen  = "\x02"
	paramClose = "\x03"
)

// markdown makes one catalog sentence safe to publish: catalog text is
// written for three renderers and is not Markdown, although it uses
// backticks for commands. Outside code spans, angle brackets are escaped
// (Markdown would read them as HTML) and a parameter becomes *\<name\>*;
// inside a code span everything is literal and a parameter is <name>.
func markdown(s string) string {
	var b strings.Builder
	inCode := false
	for _, r := range s {
		switch {
		case r == '`':
			inCode = !inCode
			b.WriteRune(r)
		case r == '\x02':
			if inCode {
				b.WriteString("<")
			} else {
				b.WriteString(`*\<`)
			}
		case r == '\x03':
			if inCode {
				b.WriteString(">")
			} else {
				b.WriteString(`\>*`)
			}
		case !inCode && (r == '<' || r == '>'):
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// repoRoot walks up to the module root, so the tool works from any
// directory of the repository.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
