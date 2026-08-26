// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// render builds a renderer over a synthetic page of the real corpus'
// first key, so relative links resolve the way they do in production.
func render(t *testing.T, body string) (string, *renderer) {
	t.Helper()
	r := &renderer{
		corpus: Load(),
		page:   &Page{Key: "passthrough/operate", Section: "passthrough", Slug: "operate", Lang: "en"},
		lang:   "en",
		labels: testLabels,
	}
	out := r.blocks(splitLines(body), 0)
	return out, r
}

// TestNoRawHTMLReachesTheOutput: whatever a page says, the renderer emits
// tags it wrote itself and text it escaped (NFR-013). The corpus is
// repository content, but the rendering path is the property under test —
// it must be safe for any input, not for the input it happens to have.
//
// Proved fallible: writing the paragraph branch as fmt.Fprintf(b, "<p>%s",
// text) instead of r.inline(text) made every case below fail.
func TestNoRawHTMLReachesTheOutput(t *testing.T) {
	payload := `<script>alert(1)</script>`
	cases := map[string]string{
		"paragraph":   payload,
		"heading":     "## " + payload,
		"list item":   "- " + payload,
		"table cell":  "| head |\n| --- |\n| " + payload + " |",
		"code fence":  "```sh\n" + payload + "\n```",
		"code span":   "`" + payload + "`",
		"link text":   "[" + payload + "](../deploy/)",
		"link target": "[text](javascript:alert(1))",
		"image alt":   "![" + payload + "](../../../../assets/docs/try-signin.png)",
		"aside":       ":::note[" + payload + "]\n" + payload + "\n:::",
		"emphasis":    "**" + payload + "**",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, _ := render(t, body)
			if strings.Contains(out, "<script") {
				t.Errorf("raw markup survived: %s", out)
			}
			if strings.Contains(out, "javascript:") {
				t.Errorf("executable target survived: %s", out)
			}
		})
	}
}

// TestUnknownBlockIsReported: a block-level construct the parser does not
// know is dropped AND reported. Dropping alone would let the corpus grow a
// construct the offline guides silently swallow.
func TestUnknownBlockIsReported(t *testing.T) {
	out, r := render(t, "<CustomThing foo=\"bar\">\n  content\n</CustomThing>")
	if strings.Contains(out, "CustomThing") {
		t.Errorf("unknown construct reached the output: %s", out)
	}
	if len(r.issues) == 0 {
		t.Error("unknown construct was dropped without a word")
	}
}

// TestMDXPreambleIsDropped: the component imports of an .mdx page are
// build instructions for a bundler this binary does not have.
func TestMDXPreambleIsDropped(t *testing.T) {
	out, r := render(t, "import StatusTable from \"../x.astro\";\n\nText.")
	if strings.Contains(out, "import") {
		t.Errorf("import line reached the output: %s", out)
	}
	if len(r.issues) != 0 {
		t.Errorf("import line reported as a defect: %v", r.issues)
	}
}

// TestHeadingAnchors: anchors are the slugs the site generator computes —
// this is what makes a fragment written for the website resolve offline —
// and a repeated title does not produce a repeated id.
func TestHeadingAnchors(t *testing.T) {
	out, r := render(t, "## Backup: the state directory\n\n## Sauvegarde : le répertoire d'état\n\n## Backup: the state directory")
	want := []string{"backup-the-state-directory", "sauvegarde--le-répertoire-détat", "backup-the-state-directory-1"}
	if len(r.headings) != len(want) {
		t.Fatalf("got %d headings, want %d", len(r.headings), len(want))
	}
	for i, id := range want {
		if r.headings[i].ID != id {
			t.Errorf("heading %d anchored %q, want %q", i, r.headings[i].ID, id)
		}
		if !strings.Contains(out, `id="`+id+`"`) {
			t.Errorf("output carries no id=%q", id)
		}
	}
}

// TestInlineConstructs covers the inline grammar, including the cases
// where a delimiter is NOT markup: an unmatched star is arithmetic, an
// escaped angle bracket is a placeholder name (the taxonomy writes
// "*\<role\>*"), and a backtick span is literal text.
func TestInlineConstructs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "<strong>bold</strong>"},
		{"*em*", "<em>em</em>"},
		{"a * b * c", "a * b * c"},
		{"2 * 3 = 6", "2 * 3 = 6"},
		{"`tasks.keepFinished`", "<code>tasks.keepFinished</code>"},
		{`*\<role\>*`, "<em>&lt;role&gt;</em>"},
		{"a & b", "a &amp; b"},
		{"**`code` in bold**", "<strong><code>code</code> in bold</strong>"},
	}
	for _, c := range cases {
		out, _ := render(t, c.in)
		if !strings.Contains(out, c.want) {
			t.Errorf("%q rendered %q, want it to contain %q", c.in, out, c.want)
		}
	}
}

// TestLinkResolution: internal targets land on /help, the error reference
// lands on the live taxonomy index, and external targets keep their
// address but are marked — an isolated zone cannot follow them.
func TestLinkResolution(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[a](../deploy/)", `href="/help/passthrough/deploy"`},
		{"[a](../../security/secrets/)", `href="/help/security/secrets"`},
		{"[a](../../reference/errors/)", `href="/help"`},
		{"[a](../../reference/errors/#tby-reg-004)", `href="/help#TBY-REG-004"`},
		{"[a](https://example.org/x)", `rel="noreferrer noopener external"`},
		{"[a](#local)", `href="#local"`},
	}
	for _, c := range cases {
		out, r := render(t, c.in)
		if len(r.issues) != 0 {
			t.Errorf("%q reported %v", c.in, r.issues)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%q rendered %q, want it to contain %q", c.in, out, c.want)
		}
	}
}

// TestUnknownErrorCodeIsReported: a fragment on the error reference that
// names no catalog entry is a dangling link, not a silent one. This is the
// guard behind "every error code links to its entry" (NFR-003 amendment).
func TestUnknownErrorCodeIsReported(t *testing.T) {
	out, r := render(t, "[a](../../reference/errors/#tby-nope-999)")
	if strings.Contains(out, "href=") {
		t.Errorf("a link to no error code was rendered: %s", out)
	}
	if len(r.issues) != 1 {
		t.Fatalf("issues %v, want exactly one", r.issues)
	}
	if !strings.Contains(r.issues[0].Reason, "taxonomy catalog") {
		t.Errorf("issue does not name the cause: %s", r.issues[0])
	}
}

// TestNestedList: a nested list is nested, not flattened — the corpus uses
// two levels and a flattened list reads as a different instruction.
func TestNestedList(t *testing.T) {
	out, _ := render(t, "- first\n  - inner one\n  - inner two\n- second")
	if strings.Count(out, "<ul") != 2 || strings.Count(out, "<li>") != 4 {
		t.Errorf("nesting lost: %s", out)
	}
}

// TestOrderedList: a numbered procedure keeps its numbers.
func TestOrderedList(t *testing.T) {
	out, _ := render(t, "1. first\n2. second")
	if !strings.HasPrefix(strings.TrimSpace(out), "<ol") {
		t.Errorf("ordered list rendered as %s", out)
	}
}

// TestTableSeparatorIsNotARow: the alignment row of a GFM table is syntax.
func TestTableSeparatorIsNotARow(t *testing.T) {
	out, _ := render(t, "| a | b |\n| --- | --- |\n| 1 | 2 |")
	if strings.Count(out, "<tr>") != 2 {
		t.Errorf("separator row rendered as data: %s", out)
	}
}

// TestImageWithoutAssetIsReported: a screenshot the binary does not carry
// is a dangling target, and an offline guide showing a broken frame is
// exactly what NFR-003 exists to prevent.
func TestImageWithoutAssetIsReported(t *testing.T) {
	out, r := render(t, "![alt](../../../../assets/docs/does-not-exist.png)")
	if strings.Contains(out, "<img") {
		t.Errorf("image rendered without an embedded file: %s", out)
	}
	if len(r.issues) == 0 {
		t.Error("missing screenshot was not reported")
	}
}

// TestStatusTableUsesTheCallersLabels: the visible words of the table come
// from the UI catalogs, not from this package (FR-063, ADR-0015 §7).
func TestStatusTableUsesTheCallersLabels(t *testing.T) {
	out, _ := render(t, "<StatusTable />")
	for _, want := range []string{testLabels.StatusFeature, testLabels.StatusAvailable, testLabels.StatusUpdated} {
		if !strings.Contains(out, want) {
			t.Errorf("status table misses the caller's label %q", want)
		}
	}
	if !strings.Contains(out, "<table") {
		t.Errorf("status table rendered no table: %s", out)
	}
}

// TestStatusTableSecurityFilter: the one-pager's filtered view is shorter
// than the full table and is not empty.
func TestStatusTableSecurityFilter(t *testing.T) {
	full, _ := render(t, "<StatusTable />")
	filtered, _ := render(t, `<StatusTable securityOnly />`)
	fullRows, filteredRows := strings.Count(full, "<tr>"), strings.Count(filtered, "<tr>")
	if filteredRows < 2 {
		t.Fatalf("the filtered table has %d rows", filteredRows)
	}
	if filteredRows >= fullRows {
		t.Errorf("the filtered table (%d rows) is not shorter than the full one (%d)", filteredRows, fullRows)
	}
}

// TestNonASCIISurvivesRendering: the corpus is bilingual and the English
// half is full of em dashes, so every rendering path must carry multibyte
// UTF-8 through untouched.
//
// The bug this locks: the inline scanner walks the source BYTE by byte,
// and writing a byte back as string(s[i]) reads it as a code point —
// every continuation byte becomes its own Latin-1 character and
// "métriques" reaches the reader as "mÃ©triques". Found by looking at a
// rendered French guide in a browser; invisible to an ASCII-only test,
// which is what every test in this file was until this one.
//
// Proved fallible: restoring the byte-to-string conversion in inline()
// fails every case below, and the whole-corpus case names the pages.
func TestNonASCIISurvivesRendering(t *testing.T) {
	cases := []string{
		"Sondes et métriques",
		"Une instance passthrough est conçue pour tourner — sans surveillance.",
		"| Chemin | Rôle |\n| --- | --- |\n| `/healthz` | Vivacité. Répond dès que le listener est en place. |",
		"- La croissance du store, dite sans détour",
		"## Sauvegarde : le répertoire d'état",
		"**Arrêt propre** et *reprise après coupure*",
		":::note[À venir — jalon 5]\nLe nettoyage du store arrive avec R-33.\n:::",
	}
	for _, in := range cases {
		out, _ := render(t, in)
		if !utf8.ValidString(out) {
			t.Errorf("%q rendered invalid UTF-8", in)
		}
		for _, word := range []string{"métriques", "conçue", "—", "Rôle", "Vivacité", "détour", "état", "Arrêt", "À venir"} {
			if strings.Contains(in, word) && !strings.Contains(out, word) {
				t.Errorf("%q lost %q on the way out:\n%s", in, word, out)
			}
		}
	}

	// The same property over the corpus that actually ships: every
	// accented word of a source page is in its rendering.
	c := Load()
	for _, key := range c.Keys() {
		pg, _, ok := c.Lookup("fr", key)
		if !ok || pg.Lang != "fr" {
			continue
		}
		r, _ := c.Render("fr", key, testLabels)
		body := string(r.HTML)
		// Authoring comments (the "<!-- TODO: screenshot: … -->" of the
		// convention) are dropped on purpose, so they are not a source of
		// text the rendering owes the reader.
		source := commentRe.ReplaceAllString(pg.body, "")
		for _, word := range []string{"é", "è", "à", "ç", "ê", "—"} {
			if strings.Contains(source, word) && !strings.Contains(body, word) {
				t.Errorf("fr/%s: %q disappeared from the rendering", key, word)
			}
		}
		if strings.Contains(body, "Ã") {
			t.Errorf("fr/%s: the rendering carries mojibake", key)
		}
	}
}

// commentRe matches the authoring comments the renderer drops.
var commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
