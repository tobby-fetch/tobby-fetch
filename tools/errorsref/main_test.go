// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The published error reference is this tool's output and nothing else: a
// message reworded in the catalog, a code added, a reference entry edited
// without regenerating — each one fails here, in the language concerned,
// with the command that fixes it.
func TestPublishedPagesAreTheGeneratedOnes(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range taxonomy.Languages() {
		want, err := generate(lang)
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		got, err := os.ReadFile(filepath.Join(root, pages[lang])) //nolint:gosec // G304: fixed page list
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(normalize(got), want) {
			t.Errorf("%s is not what the catalog generates: run `go run ./tools/errorsref` and commit the result", pages[lang])
		}
	}
}

// Every code keeps its own heading, hence its anchor: 183 deep links in
// the documentation and every /help#TBY-… link of the product land on
// them. One section per catalog code, in catalog order inside a domain,
// no duplicate and nothing else at that heading level.
func TestEveryCodeHasExactlyOneHeading(t *testing.T) {
	heading := regexp.MustCompile(`(?m)^### (.+)$`)
	for _, lang := range taxonomy.Languages() {
		page, err := generate(lang)
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		seen := map[string]int{}
		for _, m := range heading.FindAllStringSubmatch(string(page), -1) {
			seen[m[1]]++
		}
		for _, entry := range taxonomy.All() {
			code := string(entry.Code)
			if seen[code] != 1 {
				t.Errorf("%s: %s has %d headings, want 1", lang, code, seen[code])
			}
			delete(seen, code)
		}
		for extra := range seen {
			t.Errorf("%s: heading %q is not a catalog code", lang, extra)
		}
	}
}

// A page that prints an unresolved template, a leftover sentinel or a raw
// angle bracket outside a code span would be wrong in a way no diff
// against the generator can show: the generator would agree with itself.
func TestNoRenderingLeftovers(t *testing.T) {
	for _, lang := range taxonomy.Languages() {
		page, err := generate(lang)
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		for n, line := range strings.Split(string(page), "\n") {
			if strings.ContainsAny(line, paramOpen+paramClose) || strings.Contains(line, "{{") {
				t.Errorf("%s line %d: unresolved parameter: %s", lang, n+1, line)
			}
			if !strings.HasPrefix(line, "- **") {
				continue
			}
			if rawAngle(line) {
				t.Errorf("%s line %d: unescaped angle bracket outside a code span: %s", lang, n+1, line)
			}
		}
	}
}

// rawAngle reports a < or > that Markdown would read as HTML: outside a
// code span and not backslash-escaped.
func rawAngle(line string) bool {
	inCode := false
	var prev rune
	for _, r := range line {
		switch {
		case r == '`':
			inCode = !inCode
		case !inCode && (r == '<' || r == '>') && prev != '\\':
			return true
		}
		prev = r
	}
	return false
}

func TestMarkdownRendersParametersByContext(t *testing.T) {
	in := "Run `tobby user passwd " + paramOpen + "name" + paramClose + "` for " + paramOpen + "name" + paramClose + " (<1 h)."
	want := "Run `tobby user passwd <name>` for *\\<name\\>* (\\<1 h)."
	if got := markdown(in); got != want {
		t.Errorf("markdown() = %q, want %q", got, want)
	}
}

// The cross-check is what keeps a new code from shipping with half a
// section: it must name every kind of gap.
func TestCrossCheckNamesEveryGap(t *testing.T) {
	ref := &reference{
		Domains: []domain{{ID: "AAA"}, {ID: "EMPTY"}},
		Codes: map[string]codeRef{
			"TBY-AAA-001": {Offline: "yes", Blocks: "the request"},
			"TBY-AAA-002": {Offline: "yes"},
			"TBY-OLD-001": {Offline: "yes", Blocks: "nothing"},
		},
	}
	seen := map[string]bool{"TBY-AAA-001": true, "TBY-AAA-002": true, "TBY-AAA-003": true, "TBY-BBB-001": true}
	byDomain := map[string][]taxonomy.Entry{
		"AAA": {{Code: "TBY-AAA-001"}, {Code: "TBY-AAA-002"}, {Code: "TBY-AAA-003"}},
		"BBB": {{Code: "TBY-BBB-001"}},
	}
	err := crossCheck("en", ref, seen, byDomain)
	if err == nil {
		t.Fatal("crossCheck accepted an incomplete reference")
	}
	for _, want := range []string{
		"domain EMPTY has a section but no code",
		"domain BBB has codes in the catalog but no section title",
		"TBY-AAA-002 needs both",
		"TBY-AAA-003 has no reference entry",
		"TBY-BBB-001 has no reference entry",
		"TBY-OLD-001 has a reference entry but is not in the catalog",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("crossCheck does not report %q:\n%v", want, err)
		}
	}
}
