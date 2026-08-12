// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestHelpAnchorsMatchTaxonomy: /help carries one anchored section per
// catalog entry, and each id is exactly the fragment of the HelpAnchor the
// taxonomy emits — no error link can 404 (R-03, UI-SPEC §5.10).
func TestHelpAnchorsMatchTaxonomy(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view") // readable by every role

	r := httptest.NewRequest(http.MethodGet, "/help", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/help = %d", w.Code)
	}
	body := w.Body.String()

	if len(taxonomy.All()) == 0 {
		t.Fatal("empty taxonomy catalog")
	}
	for _, en := range taxonomy.All() {
		m := taxonomy.Localize("en", taxonomy.New(en.Code, genericParams(en)))
		anchor, ok := strings.CutPrefix(m.HelpAnchor, "/help#")
		if !ok {
			t.Fatalf("HelpAnchor %q of %s is not a /help fragment", m.HelpAnchor, en.Code)
		}
		if anchor != string(en.Code) {
			t.Errorf("HelpAnchor fragment %q differs from code %q", anchor, en.Code)
		}
		if !strings.Contains(body, `id="`+anchor+`"`) {
			t.Errorf("/help misses the anchored section id=%q", anchor)
		}
		// The summary links every code.
		if !strings.Contains(body, `href="#`+anchor+`"`) {
			t.Errorf("/help summary misses the anchor link for %s", anchor)
		}
	}
}

// genericParams builds the placeholder parameter set of one entry, the
// same way the screen does.
func genericParams(en taxonomy.Entry) taxonomy.Params {
	p := make(taxonomy.Params, len(en.Params))
	for _, name := range en.Params {
		p[name] = "<" + name + ">"
	}
	return p
}

// TestHelpLocalizedTriptych: the triptych renders in the current language
// with readable generic parameters ("<host>").
func TestHelpLocalizedTriptych(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	r := httptest.NewRequest(http.MethodGet, "/help", http.NoBody)
	r.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body := w.Body.String()

	if !strings.Contains(body, "Cause probable") || !strings.Contains(body, "Action corrective") {
		t.Error("French /help misses the localized triptych labels")
	}
	// TBY-REG-003's cause names its host parameter generically (the value
	// is "<host>", HTML-escaped by the template engine).
	if !strings.Contains(body, "&lt;host&gt;") {
		t.Error("generic parameters do not render as <name> placeholders")
	}

	// The head of the trio carries the local sub-menu (UI-SPEC §5.10-5.12).
	for _, href := range []string{`href="/help"`, `href="/about"`, `href="/api-docs"`} {
		if !strings.Contains(body, href) {
			t.Errorf("/help sub-menu misses %s", href)
		}
	}
}
