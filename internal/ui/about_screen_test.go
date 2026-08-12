// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/server"
)

// TestAboutScreen: build identity, the ReservedPrefixes contract, the
// milestone roadmap, and the license block (UI-SPEC §5.11).
func TestAboutScreen(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view") // readable by every role

	r := httptest.NewRequest(http.MethodGet, "/about", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/about = %d", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{
		"0.2.0-test", // injected build version (same value as the shell)
		"mirror",     // read-only mode
		"GPL-3.0-only",
		`href="/about/third-party"`,
		`href="/api-docs"`,
		"Milestone 3", // roadmap shown here, never as greyed-out nav
		"Milestone 7",
		"/metrics", "/healthz", "/readyz", // endpoint table
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/about misses %q", want)
		}
	}
	// The full reserved-prefix contract is documented (ADR-0015 §2).
	for _, p := range server.ReservedPrefixes {
		if !strings.Contains(body, ">"+p+"<") {
			t.Errorf("/about misses the reserved prefix %q", p)
		}
	}
}

// TestThirdPartyNotices: the embedded THIRD-PARTY-NOTICES is served as
// plain text on /about/third-party (ADR-0010).
func TestThirdPartyNotices(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	r := httptest.NewRequest(http.MethodGet, "/about/third-party", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/about/third-party = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if !strings.Contains(w.Body.String(), "htmx") {
		t.Error("notices do not mention the vendored htmx asset")
	}
}
