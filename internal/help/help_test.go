// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// testLabels stands in for the UI catalogs. The values are deliberately
// recognisable so a test can assert they reached the output.
var testLabels = &Labels{
	StatusUpdated:   "Status as of the embedded data file",
	StatusFeature:   "Feature",
	StatusStatus:    "Status",
	StatusMilestone: "Milestone",
	StatusAvailable: "Available",
	StatusPartial:   "Partial",
	StatusUpcoming:  "Upcoming",
}

// TestCorpusLoads: the embedded corpus is non-empty, every page carries a
// title, and every page belongs to a known section. A corpus that fails to
// load is a build defect — this is the test that says so first.
func TestCorpusLoads(t *testing.T) {
	c := Load()
	keys := c.Keys()
	if len(keys) < 30 {
		t.Fatalf("only %d pages embedded — the corpus looks truncated", len(keys))
	}
	known := map[string]bool{}
	for _, s := range sectionOrder {
		known[s] = true
	}
	for _, key := range keys {
		pg, _, ok := c.Lookup("en", key)
		if !ok {
			t.Fatalf("%s: listed but not resolvable", key)
		}
		if pg.Title == "" {
			t.Errorf("%s: no title", key)
		}
		if !known[pg.Section] {
			t.Errorf("%s: section %q is not in the reading order", key, pg.Section)
		}
	}
}

// TestSectionOrder: the reading order lists exactly the sections present.
// A section added to the website and forgotten here would render nowhere
// in the help navigation — invisible, not broken, which is worse.
func TestSectionOrder(t *testing.T) {
	present := Load().Sections()
	declared := append([]string(nil), sectionOrder...)
	sort.Strings(declared)
	if !reflect.DeepEqual(present, declared) {
		t.Errorf("sections in the corpus %v, declared in the reading order %v", present, declared)
	}
}

// TestNoDanglingLink is the link check NFR-003 (amendment 2026-08-11) asks
// for: every cross-page link, every anchor and every screenshot of the
// embedded corpus resolves, in both languages. The contrary of offline
// help is help that points at nothing.
//
// Proved fallible: rendering the same corpus with the "reference/errors"
// mapping removed from resolveLink reported the 8 links to the error
// reference as dangling, and pointing one page at "../../nowhere/" added
// its own entry.
func TestNoDanglingLink(t *testing.T) {
	issues := Load().CheckLinks(testLabels)
	for _, is := range issues {
		t.Errorf("dangling link: %s", is)
	}
}

// TestEveryPageRenders: every page of every language produces a body. A
// page whose Markdown the parser cannot see would render empty and be
// caught by no link check.
func TestEveryPageRenders(t *testing.T) {
	c := Load()
	for _, lang := range c.Langs() {
		for _, key := range c.Keys() {
			r, ok := c.Render(lang, key, testLabels)
			if !ok {
				t.Fatalf("%s/%s: does not render", lang, key)
			}
			if len(strings.TrimSpace(string(r.HTML))) < 200 {
				t.Errorf("%s/%s: rendered body is suspiciously short (%d bytes)",
					lang, key, len(r.HTML))
			}
			if len(r.Headings) == 0 {
				t.Errorf("%s/%s: no heading — the page has no table of contents", lang, key)
			}
		}
	}
}

// TestNoExternalResource: no rendered page pulls a byte from anywhere but
// this instance (NFR-019). Navigation links to the public documentation
// are allowed — the reader copies them onto a connected machine — but a
// resource-loading attribute pointing off-host would turn an air-gapped
// screen into a hanging request.
//
// Proved fallible: adding an <img src="https://example.invalid/x.png"> to
// the rendering path failed this test on every page that carried it.
func TestNoExternalResource(t *testing.T) {
	c := Load()
	// Attributes and functions that FETCH. href is absent on purpose: it
	// navigates, it does not load.
	loaders := []string{`src="`, `srcset="`, `data="`, `poster="`, `url(`, `@import`}
	for _, lang := range c.Langs() {
		for _, key := range c.Keys() {
			r, _ := c.Render(lang, key, testLabels)
			body := string(r.HTML)
			for _, loader := range loaders {
				for i := 0; ; {
					j := strings.Index(body[i:], loader)
					if j < 0 {
						break
					}
					i += j + len(loader)
					value := body[i:]
					if k := strings.IndexAny(value, `"')`); k >= 0 {
						value = value[:k]
					}
					if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") ||
						strings.HasPrefix(value, "//") {
						t.Errorf("%s/%s: %s%s loads from outside the instance", lang, key, loader, value)
					}
				}
			}
			if strings.Contains(body, "<script") || strings.Contains(body, "<iframe") {
				t.Errorf("%s/%s: rendered body carries a script or a frame", lang, key)
			}
		}
	}
}

// TestModeGuidesAreBilingual: the two operating modes and the entry points
// into them are served in French without falling back to English — the
// literal wording of the NFR-003 amendment ("operations guides for both
// modes and a troubleshooting guide, in English and French"). The
// troubleshooting guide itself is /help, rendered from the taxonomy
// catalog, whose bilingual completeness internal/taxonomy tests.
func TestModeGuidesAreBilingual(t *testing.T) {
	c := Load()
	for _, key := range c.Keys() {
		section, _, _ := strings.Cut(key, "/")
		if section != "passthrough" && section != "air-gap" && section != "try" {
			continue
		}
		if !c.Translated("fr", key) {
			t.Errorf("%s: no French page — an operations guide of a shipped mode must exist in both languages", key)
		}
	}
}

// TestFrenchPagesFallBackToEnglish: a page with no translation is served
// in English and says so, rather than 404-ing a French reader out of the
// documentation (website/README-docs.md documents that fallback).
func TestFrenchPagesFallBackToEnglish(t *testing.T) {
	c := Load()
	var fallbacks int
	for _, key := range c.Keys() {
		pg, fallback, ok := c.Lookup("fr", key)
		if !ok {
			t.Fatalf("%s: unreachable in French", key)
		}
		if fallback {
			fallbacks++
			if pg.Lang != "en" {
				t.Errorf("%s: fallback page is not the English one", key)
			}
		}
	}
	if fallbacks == 0 {
		t.Skip("every page is translated — the fallback path has no subject left")
	}
}

// TestNavIsOrdered: the navigation follows the reading order of the
// sections and, inside a section, the frontmatter order.
func TestNavIsOrdered(t *testing.T) {
	nav := Load().Nav("en")
	if len(nav) != len(sectionOrder) {
		t.Fatalf("navigation has %d sections, reading order declares %d", len(nav), len(sectionOrder))
	}
	for i, ns := range nav {
		if ns.ID != sectionOrder[i] {
			t.Errorf("section %d is %q, expected %q", i, ns.ID, sectionOrder[i])
		}
		prev := -1
		for _, p := range ns.Pages {
			pg, _, _ := Load().Lookup("en", p.Key)
			if pg.Order < prev {
				t.Errorf("%s: order %d comes after %d", p.Key, pg.Order, prev)
			}
			prev = pg.Order
		}
	}
}

// TestUnknownPageIsNotFound: a key nobody published resolves to nothing.
// The routing layer turns that into the taxonomized 404, and the traversal
// attempts below never reach a file because the lookup is a map hit on a
// fixed key set (NFR-011).
func TestUnknownPageIsNotFound(t *testing.T) {
	c := Load()
	for _, key := range []string{
		"", "nowhere", "passthrough", "passthrough/nowhere",
		"../../../etc/passwd", "..%2f..%2fetc/passwd",
		"passthrough/../../go.mod", "/etc/passwd",
		"passthrough/operate/../../../secret",
	} {
		if _, ok := c.Render("en", key, testLabels); ok {
			t.Errorf("%q resolved to a page", key)
		}
	}
}

// TestAssetsAreReferenced: every embedded screenshot is pointed at by at
// least one page. The corpus is carried into isolated zones — a megabyte
// nothing links to is a megabyte every operator transports for nothing.
func TestAssetsAreReferenced(t *testing.T) {
	c := Load()
	used := map[string]bool{}
	for _, lang := range c.Langs() {
		for _, key := range c.Keys() {
			r, _ := c.Render(lang, key, testLabels)
			for _, name := range c.AssetNames() {
				if strings.Contains(string(r.HTML), assetPath(name)) {
					used[name] = true
				}
			}
		}
	}
	for _, name := range c.AssetNames() {
		if !used[name] {
			t.Errorf("screenshot %q is embedded but referenced by no page", name)
		}
	}
}
