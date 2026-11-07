// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestSanitizeNextRefusesWhatABrowserReadsAsAnotherHost pins B-033. The
// post-login target only refused "//": a browser also reads a backslash
// as a slash, and strips tab, CR and LF from a URL before resolving it,
// so each of these is "//evil.example" by the time the Location header is
// followed. Every case must fall back to the dashboard.
func TestSanitizeNextRefusesWhatABrowserReadsAsAnotherHost(t *testing.T) {
	for _, next := range []string{
		`/\evil.example`,
		`/\\evil.example`,
		"/\t/evil.example",
		"/\n/evil.example",
		"/\r/evil.example",
		"/\x00/evil.example",
		"/\x7f/evil.example",
		`/tasks\..\..\\evil.example`,
		"//evil.example",
		"https://evil.example/",
		"evil.example",
		"",
	} {
		if got := sanitizeNext(next); got != "/" {
			t.Errorf("sanitizeNext(%q) = %q, want the dashboard", next, got)
		}
	}
	for _, next := range []string{"/", "/tasks", "/content?repo=a%2Fb", "/help#TBY-AUTH-002"} {
		if got := sanitizeNext(next); got != next {
			t.Errorf("sanitizeNext(%q) = %q, want it kept", next, got)
		}
	}
}

// TestLoginNeverRedirectsOffHost drives the whole sign-in: the Location
// a successful login answers with is what the browser follows.
func TestLoginNeverRedirectsOffHost(t *testing.T) {
	mux := mount(newTestUI(t, false))
	form := url.Values{"username": {"alexis"}, "password": {"pw-admin"}, "next": {`/\evil.example`}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("login redirected to %q, want the dashboard", loc)
	}
}
