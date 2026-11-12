// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// The transfer slip endpoint (R-07, FR-061). What it draws is locked in
// internal/slip; this file owns the surface contract: the role, the three
// formats, the two languages, the headers that make an HTML page safe to
// open, and the unsigned notice that reaches the wire in every
// combination.

var slipNotices = map[string]string{
	"en": "Unsigned aid document — not proof of authenticity",
	"fr": "Document d'aide non signé — ne vaut pas preuve d'authenticité",
}

func TestMediaSlipIsReadableByAViewerInEveryFormatAndLanguage(t *testing.T) {
	v := &stubVerifier{zone: "zone-alpha", report: blockedReport()}
	mux, q, _ := newMediaAPI(t, v)
	// A scan report in a task document is what the slip lists per element.
	_, err := q.Create(tasks.TypeSync, "retriever", "op", []tasks.Item{{
		Name: "wordpress@6.8.2/app",
		Scan: &scan.Report{
			Counts: scan.Counts{High: 2},
			Policy: scan.Evaluation{Verdict: scan.VerdictWarn, Policy: scan.Policy{Mode: scan.ModeWarn, Threshold: 7}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	for lang, notice := range slipNotices {
		for _, format := range []string{"html", "text"} {
			w := mediaCall(t, mux, http.MethodGet, "/api/v1/media/slip?format="+format+"&lang="+lang, "lecteur", "pw-view", "")
			if w.Code != http.StatusOK {
				t.Fatalf("GET slip format=%s lang=%s as viewer = %d: %s", format, lang, w.Code, w.Body.String())
			}
			body := w.Body.String()
			want := strings.ReplaceAll(notice, "'", "&#39;")
			if format == "text" {
				want = strings.ToUpper(notice)
			}
			if n := strings.Count(body, want); n != 2 {
				t.Errorf("format=%s lang=%s: the unsigned notice appears %d times, want 2 (head and foot)", format, lang, n)
			}
			if !strings.Contains(body, "wordpress@6.8.2") {
				t.Errorf("format=%s lang=%s: the scan report of the task is not listed", format, lang)
			}
		}
	}

	w := mediaCall(t, mux, http.MethodGet, "/api/v1/media/slip?format=json", "lecteur", "pw-view", "")
	var doc struct {
		Signed bool   `json:"signed"`
		Side   string `json:"side"`
		Zone   string `json:"zone"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding the JSON slip: %v\n%s", err, w.Body.String())
	}
	if doc.Signed || doc.Side != "destination" || doc.Zone != "zone-alpha" {
		t.Errorf("JSON slip = %+v, want unsigned, destination side, zone-alpha", doc)
	}
}

func TestMediaSlipHTMLCarriesAPolicyThatAllowsNothingButItsStylesheet(t *testing.T) {
	mux, _, _ := newMediaAPI(t, &stubVerifier{zone: "zone-alpha", report: blockedReport()})
	w := mediaCall(t, mux, http.MethodGet, "/api/v1/media/slip", "lecteur", "pw-view", "")
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want HTML by default", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "'self'") {
		t.Errorf("Content-Security-Policy = %q: the slip may load nothing but its own hashed stylesheet", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v, want nosniff and no-store", w.Header())
	}
	if w.Header().Get("Content-Disposition") != "" {
		t.Error("a slip shown in the browser must not be forced to download")
	}
}

func TestMediaSlipDownloadAndLanguageNegotiation(t *testing.T) {
	mux, _, _ := newMediaAPI(t, &stubVerifier{zone: "zone-alpha", report: blockedReport()})
	w := mediaCall(t, mux, http.MethodGet, "/api/v1/media/slip?format=text&download=1", "lecteur", "pw-view", "")
	if got := w.Header().Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="transfer-slip`) || !strings.HasSuffix(got, `.txt"`) {
		t.Errorf("Content-Disposition = %q", got)
	}

	// No lang parameter: Accept-Language decides.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/media/slip?format=text", http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	r.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if !strings.Contains(rec.Body.String(), "BORDEREAU DE TRANSFERT") {
		t.Errorf("Accept-Language: fr did not give a French slip:\n%s", rec.Body.String())
	}
}

func TestMediaSlipRefusesWhatItDoesNotKnow(t *testing.T) {
	mux, _, _ := newMediaAPI(t, &stubVerifier{zone: "zone-alpha", report: blockedReport()})
	for _, q := range []string{"?format=pdf", "?lang=de"} {
		w := mediaCall(t, mux, http.MethodGet, "/api/v1/media/slip"+q, "lecteur", "pw-view", "")
		if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET slip%s = %d, want a validation refusal", q, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/media/slip", http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an anonymous GET slip = %d, want 401", rec.Code)
	}
}

// TestMediaSlipWorksOnASourceSide: no engine, no gate — a mirror source
// instance still has a medium to hand over.
func TestMediaSlipWorksOnASourceSide(t *testing.T) {
	mux, _, _ := newMediaAPI(t, nil)
	w := mediaCall(t, mux, http.MethodGet, "/api/v1/media/slip?format=text&lang=en", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET slip on a source side = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Source side") {
		t.Errorf("a source-side slip should say so:\n%s", w.Body.String())
	}
}
