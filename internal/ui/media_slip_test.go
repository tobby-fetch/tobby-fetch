// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The transfer slip on the Media screen (R-07). What it draws is locked in
// internal/slip; this file owns the surface: the entry points on the
// screen, the role, the formats and languages, and the headers of a page
// that leaves the shell.

var uiSlipNotices = map[string]string{
	"en": "Unsigned aid document — not proof of authenticity",
	"fr": "Document d'aide non signé — ne vaut pas preuve d'authenticité",
}

func TestMediaScreenOffersTheSlipInBothFormats(t *testing.T) {
	u, _ := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	body := get(t, mux, c, "/media", nil).Body.String()
	for _, want := range []string{
		`href="/media/slip"`, `href="/media/slip?lang=fr"`,
		`href="/media/slip?download=1"`, `href="/media/slip?format=text&amp;download=1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Media screen does not offer %s", want)
		}
	}
	// Files and a new tab, never boosted: hx-boost would swap a whole
	// document into the shell (B-013).
	if n := strings.Count(body, `hx-boost="false"`); n < 4 {
		t.Errorf("only %d slip links opt out of hx-boost, want 4", n)
	}
	// The screen itself says what the slip is not.
	if !strings.Contains(body, "not proof of authenticity") {
		t.Error("the Media screen offers the slip without saying it is unsigned")
	}
}

func TestMediaSlipServesAnUnsignedDocumentInBothLanguages(t *testing.T) {
	u, gate := mediaUI(t)
	gate.Observe(wholeReport())
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	for lang, notice := range uiSlipNotices {
		rec := get(t, mux, c, "/media/slip?lang="+lang, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /media/slip?lang=%s as viewer = %d", lang, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q", ct)
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "'self'") {
			t.Errorf("CSP = %q: the slip is a standalone document that loads nothing", csp)
		}
		body := rec.Body.String()
		if n := strings.Count(body, strings.ReplaceAll(notice, "'", "&#39;")); n != 2 {
			t.Errorf("lang=%s: html notice appears %d times, want 2", lang, n)
		}
		// Standalone: none of the shell's script or sheet is in it.
		for _, shell := range []string{"htmx", "app.css", "<nav"} {
			if strings.Contains(body, shell) {
				t.Errorf("the slip carries %q of the interface shell", shell)
			}
		}
		for _, want := range []string{"wordpress@6.8.2", "redis@7.2.5", "SHA256:zoneA", testMediaZone} {
			if !strings.Contains(body, want) {
				t.Errorf("lang=%s: the slip does not carry %q", lang, want)
			}
		}

		text := get(t, mux, c, "/media/slip?format=text&download=1&lang="+lang, nil)
		if text.Code != http.StatusOK || !strings.HasPrefix(text.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("text slip = %d %q", text.Code, text.Header().Get("Content-Type"))
		}
		if cd := text.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, ".txt") {
			t.Errorf("Content-Disposition = %q", cd)
		}
		if n := strings.Count(text.Body.String(), strings.ToUpper(notice)); n != 2 {
			t.Errorf("lang=%s: text notice appears %d times, want 2", lang, n)
		}
	}
}

func TestMediaSlipFollowsTheInterfaceLanguageAndRefusesUnknownOnes(t *testing.T) {
	u, _ := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	rec := get(t, mux, c, "/media/slip", map[string]string{"Accept-Language": "fr"})
	if !strings.Contains(rec.Body.String(), "Bordereau de transfert") {
		t.Error("the slip does not follow the negotiated interface language")
	}
	for _, q := range []string{"?format=pdf", "?lang=de"} {
		if rec := get(t, mux, c, "/media/slip"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /media/slip%s = %d, want 400", q, rec.Code)
		}
	}
}

// TestMediaSlipDoesNotVerify: drawing a slip reads the last verdict; it
// never starts the walk of the disk.
func TestMediaSlipDoesNotVerify(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")
	rec := get(t, mux, c, "/media/slip?format=text&lang=en", nil)
	if !strings.Contains(rec.Body.String(), "No verification report is held") {
		t.Errorf("a slip drawn before any verification should say so:\n%s", rec.Body.String())
	}
	if gate.Status().Running || gate.Status().Report != nil {
		t.Error("drawing the slip started or recorded a verification")
	}
}

// TestSlipStagesAgreeWithTheMediaScreen: the slip derives the three R-02
// stage verdicts from the report on its own (it cannot import this
// package), so this test is what keeps the two derivations one rule. A
// slip that said "passed" where the screen said "failed" would be worse
// than no slip.
func TestSlipStagesAgreeWithTheMediaScreen(t *testing.T) {
	u, gate := mediaUI(t)
	for name, rep := range map[string]*media.Report{
		"whole": wholeReport(),
		"partial": {
			Verdict: media.VerdictPartial,
			Recipes: []media.RecipeVerdict{
				{Name: "wordpress", Version: "6.8.2", Pushable: true},
				{Name: "redis", Version: "7.2.5", Reason: &media.Reason{Code: taxonomy.CodeMediaFileDigest, Path: "x"}},
			},
		},
		"bad signature": {
			Verdict: media.VerdictPartial,
			Recipes: []media.RecipeVerdict{
				{Name: "wordpress", Version: "6.8.2", Pushable: true},
				{Name: "redis", Version: "7.2.5", Reason: &media.Reason{Code: taxonomy.CodeSignature}},
			},
		},
		"blocked": {
			Verdict: media.VerdictBlocked,
			Blocks:  []media.Block{{Code: taxonomy.CodeMediaGraphAltered}},
		},
		"blocked but waived": {
			Verdict: media.VerdictPushable,
			Blocks:  []media.Block{{Code: taxonomy.CodeMediaZoneMismatch, Overridable: true, Overridden: true}},
			Recipes: []media.RecipeVerdict{{Name: "wordpress", Version: "6.8.2", Pushable: true}},
		},
	} {
		gate.Observe(rep)
		screen := u.mediaScreenData(httptest.NewRequest(http.MethodGet, "/media", http.NoBody))
		sl := u.slipInstance().Slip()
		if len(screen.Stages) != len(sl.Verification.Stages) {
			t.Fatalf("%s: %d stages on the screen, %d on the slip", name, len(screen.Stages), len(sl.Verification.Stages))
		}
		for i, st := range screen.Stages {
			got := sl.Verification.Stages[i]
			if got.Key != st.Key || got.State != st.State || got.Passed != st.Passed || got.Failed != st.Failed {
				t.Errorf("%s: stage %s is %+v on the slip, %+v on the screen", name, st.Key, got, st)
			}
		}
	}
}
