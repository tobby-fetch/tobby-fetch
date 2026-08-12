// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIDocsScreen: the in-house OpenAPI viewer (UI-SPEC §5.12) — one
// <details> per endpoint, English contract content marked lang="en",
// host-contextualized curl examples, raw-document download link.
func TestAPIDocsScreen(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view") // readable by every role

	r := httptest.NewRequest(http.MethodGet, "/api-docs", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/api-docs = %d", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{
		`href="/api/v1/openapi.yaml"`,                 // raw download
		`lang="en"`,                                   // contract content marked English
		"<details",                                    // semantic, JS-free disclosure
		"GET /api/v1/tasks",                           // endpoints from the parsed document
		"POST /api/v1/import",                         //
		"POST /api/v1/tokens/{name}/revoke",           //
		"curl -u account:password http://example.com", // contextualized on r.Host
		`data-copy="curl -u account:password`,         // copiable chip
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/api-docs misses %q", want)
		}
	}

	// Parameters travel from the YAML: the inspect endpoint documents ref.
	if !strings.Contains(body, ">ref<") {
		t.Error("/api-docs misses the ref parameter of the inspect endpoint")
	}
	// $ref parameters resolve against components (task id, repo path).
	if !strings.Contains(body, ">id<") || !strings.Contains(body, ">repo<") {
		t.Error("/api-docs does not resolve $ref parameters")
	}

	// Every documented operation of the embedded document is rendered
	// (the shell's user menu is a <details> too — count the endpoint ones).
	if got := strings.Count(body, `<details class="t-apidoc"`); got != len(apiEndpoints) {
		t.Errorf("rendered %d endpoints, document carries %d", got, len(apiEndpoints))
	}
}
