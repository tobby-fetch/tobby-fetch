// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The design tokens are the documented surface of ui.themeOverride (NFR-017,
// FR-064): what the operator may redeclare is what the Theming page lists,
// and what the components read is what tokens.css declares. Both halves are
// held here, so a token cannot be added to the CSS and forgotten in the
// documentation, nor read by a component without being declared.

// varUseRe matches a read of a token WITHOUT a fallback: --tobby-sticky-offset
// is set by the shell at run time and read as var(--tobby-sticky-offset, 0),
// which is a layout measurement and not a token an operator may override.
var varUseRe = regexp.MustCompile(`var\((--tobby-[a-z0-9-]+)\)`)

func declaredTokens(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	out := map[string]bool{}
	for _, sel := range []string{":root {", `:root[data-theme="light"]`} {
		for name := range themeTokens(t, css, sel) {
			out[name] = true
		}
	}
	return out
}

func TestEveryTokenReadIsDeclared(t *testing.T) {
	declared := declaredTokens(t)
	for _, file := range []string{"static/app.css", "static/tokens.css"} {
		raw, err := os.ReadFile(file) //nolint:gosec // G304: fixed stylesheet paths of this package
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range varUseRe.FindAllStringSubmatch(string(raw), -1) {
			if !declared[m[1]] {
				t.Errorf("%s reads %s, which tokens.css declares in neither theme: the override surface would have a hole", file, m[1])
			}
		}
	}
}

func TestEveryDeclaredTokenIsDocumented(t *testing.T) {
	declared := declaredTokens(t)
	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, page := range []string{
		"../help/corpus/en/reference/theming.md",
		"../help/corpus/fr/reference/theming.md",
	} {
		raw, err := os.ReadFile(page) //nolint:gosec // G304: fixed corpus paths of this test
		if err != nil {
			t.Fatalf("the theming page is missing: %v", err)
		}
		text := string(raw)
		for _, n := range names {
			if !strings.Contains(text, "`"+n+"`") {
				t.Errorf("%s does not document %s", page, n)
			}
		}
	}
}
