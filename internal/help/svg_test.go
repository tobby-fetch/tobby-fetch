// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"strings"
	"testing"
)

// TestSVGAllowlistRejects: the diagrams are the only markup the corpus
// contributes to the page, so they are the only place where an allowlist
// decides what a browser executes. Every case below is a rejection of the
// whole block, not a silent strip: a diagram that renders "almost" the
// document its author wrote is worse than one that does not render.
//
// Proved fallible: replacing sanitizeSVG's body with `return src, nil`
// made all eight cases pass through unchanged, and TestNoRawHTMLReaches…
// stayed green — the allowlist is the only thing standing here.
func TestSVGAllowlistRejects(t *testing.T) {
	cases := map[string]string{
		"script element":     `<svg viewBox="0 0 10 10"><script>alert(1)</script></svg>`,
		"foreign object":     `<svg viewBox="0 0 10 10"><foreignObject><b>x</b></foreignObject></svg>`,
		"event handler":      `<svg viewBox="0 0 10 10"><rect onclick="alert(1)" x="0" y="0"></rect></svg>`,
		"external image":     `<svg viewBox="0 0 10 10"><image href="https://example.invalid/x.png"></image></svg>`,
		"external use":       `<svg viewBox="0 0 10 10"><use href="https://example.invalid/x#y"></use></svg>`,
		"remote fill":        `<svg viewBox="0 0 10 10"><rect fill="url(https://example.invalid/x)"></rect></svg>`,
		"javascript in href": `<svg viewBox="0 0 10 10"><a href="javascript:alert(1)"><rect></rect></a></svg>`,
		"style element":      `<svg viewBox="0 0 10 10"><style>*{x:y}</style></svg>`,
		"not an svg":         `<div><rect></rect></div>`,
		"malformed":          `<svg viewBox="0 0 10 10"><rect>`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := sanitizeSVG(src)
			if err == nil {
				t.Errorf("accepted, and produced %q", out)
			}
		})
	}
}

// TestSVGAllowlistAccepts: the drawing vocabulary of the corpus survives
// intact, arrowheads included — the allowlist must not be a way of not
// having diagrams.
func TestSVGAllowlistAccepts(t *testing.T) {
	src := `<svg viewBox="0 0 640 200" role="img" aria-label="A diagram" style="width:100%;max-width:640px;">
  <defs>
    <marker id="d-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <rect x="8" y="24" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <line x1="8" y1="8" x2="80" y2="8" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 3" marker-end="url(#d-arrow)" />
  <text x="8" y="14" font-size="11" text-anchor="middle" fill="var(--sl-color-gray-2)">Source &amp; zone</text>
</svg>`
	out, err := sanitizeSVG(src)
	if err != nil {
		t.Fatalf("rejected a corpus diagram: %v", err)
	}
	for _, want := range []string{
		`<svg viewBox="0 0 640 200"`, `role="img"`, `aria-label="A diagram"`,
		`<marker id="d-arrow"`, `marker-end="url(#d-arrow)"`,
		`<path d="M0 0 L10 5 L0 10 z"`, `Source &amp; zone`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("re-serialized diagram misses %q:\n%s", want, out)
		}
	}
}

// TestEveryCorpusDiagramIsAccepted: the allowlist is checked against the
// diagrams that actually ship, not only against invented ones. A rejected
// diagram is reported as a link issue and would fail TestNoDanglingLink;
// this test says so directly.
func TestEveryCorpusDiagramIsAccepted(t *testing.T) {
	c := Load()
	var diagrams int
	for _, lang := range c.Langs() {
		for _, key := range c.Keys() {
			pg, _, _ := c.Lookup(lang, key)
			if !strings.Contains(pg.body, "<svg") {
				continue
			}
			r, _ := c.Render(lang, key, testLabels)
			diagrams += strings.Count(string(r.HTML), "<svg")
			if n := strings.Count(pg.body, "<svg"); n != strings.Count(string(r.HTML), "<svg") {
				t.Errorf("%s/%s: %d diagrams in the source, %d rendered", lang, key, n, strings.Count(string(r.HTML), "<svg"))
			}
		}
	}
	if diagrams == 0 {
		t.Fatal("no diagram in the corpus — this test proves nothing")
	}
}

// TestSVGKeepsItsAccessibleName: a diagram without its alternative is a
// picture a screen reader announces as nothing (NFR-017).
func TestSVGKeepsItsAccessibleName(t *testing.T) {
	c := Load()
	for _, lang := range c.Langs() {
		for _, key := range c.Keys() {
			r, _ := c.Render(lang, key, testLabels)
			body := string(r.HTML)
			for i := 0; ; {
				j := strings.Index(body[i:], "<svg")
				if j < 0 {
					break
				}
				i += j
				end := strings.Index(body[i:], ">")
				if end < 0 {
					break
				}
				open := body[i : i+end]
				if !strings.Contains(open, "aria-label=") && !strings.Contains(open, "aria-labelledby=") {
					t.Errorf("%s/%s: a diagram carries no accessible name: %s", lang, key, open)
				}
				i += end
			}
		}
	}
}
