// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// TestSecurityHeadersOnEveryUIResponse pins the v0.4.2 hardening: every
// response of the UI surface — a rendered page, an error page, a static
// asset, a redirect — carries the browser security headers. Sampled on
// the three response shapes rather than every route: the wrapper sits in
// Mount, so one covered route covers them all by construction.
func TestSecurityHeadersOnEveryUIResponse(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)

	for _, path := range []string{
		"/login",          // pre-auth page
		"/static/app.css", // embedded asset
		"/",               // redirect to /login (anonymous)
	} {
		r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		h := w.Header()
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("%s: Referrer-Policy = %q, want same-origin", path, got)
		}
		if got := h.Get("Content-Security-Policy"); got != cspHeader {
			t.Errorf("%s: Content-Security-Policy = %q, want the built policy", path, got)
		}
	}
}

// TestCSPShape pins what the policy must and must not say: inline
// template scripts are allowed by hash — never by 'unsafe-inline' — and
// nothing re-enables eval, which the layout's htmx-config also disables.
func TestCSPShape(t *testing.T) {
	if strings.Contains(cspHeader, "unsafe-eval") {
		t.Error("CSP re-enables eval; htmx runs with allowEval:false and needs no eval")
	}
	if strings.Contains(cspHeader, "script-src 'self' 'unsafe-inline'") ||
		strings.Contains(cspHeader, "script-src 'unsafe-inline'") {
		t.Error("script-src falls back to 'unsafe-inline'; inline scripts must be hash-allowed")
	}
	if !strings.Contains(cspHeader, "frame-ancestors 'none'") {
		t.Error("CSP misses frame-ancestors 'none'")
	}
	if !strings.Contains(cspHeader, "form-action 'self'") {
		t.Error("CSP misses form-action 'self'")
	}
	hashes := inlineScriptHashes()
	if len(hashes) == 0 {
		t.Fatal("no inline script hash computed; the layout script alone should produce one")
	}
	for _, h := range hashes {
		if !strings.Contains(cspHeader, h) {
			t.Errorf("hash %s missing from the CSP", h)
		}
	}
}

// TestRenderedInlineScriptsAreHashAllowed hashes the inline scripts of
// REAL rendered pages and checks each against the CSP — end to end, on
// the bytes a browser receives. This is the regression guard for the
// html/template subtlety renderedScript documents: hashing the template
// sources produced hashes no browser ever saw, and every inline script
// was silently blocked (first caught by the browser suite as
// "__tobbyWired is not set"). The sampled pages cover every template
// that carries an inline script: the layout (via the dashboard), the
// import screen, the publication screen, and the accounts screen.
func TestRenderedInlineScriptsAreHashAllowed(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	for _, path := range []string{"/", "/import", "/recipes/publish", "/admin/accounts"} {
		r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, w.Code)
		}
		scripts := inlineScriptRe.FindAllSubmatch(w.Body.Bytes(), -1)
		if path == "/" && len(scripts) == 0 {
			t.Fatal("the dashboard carries no inline script; the layout script vanished")
		}
		for i, m := range scripts {
			sum := sha256.Sum256(m[1])
			h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !strings.Contains(cspHeader, h) {
				t.Errorf("%s: inline script %d is not allowed by the CSP (hash %s): the browser will silently drop it", path, i, h)
			}
		}
	}
}

// TestInlineScriptsAreStatic is the soundness condition of the hash-based
// CSP: the hashes are computed over the template SOURCES, which equals
// the rendered bytes only while no inline <script> block contains a
// template action. A script gaining {{…}} would hash differently once
// rendered and be silently blocked in every browser — this test turns
// that silent breakage into a build failure with instructions.
func TestInlineScriptsAreStatic(t *testing.T) {
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range inlineScriptRe.FindAllSubmatch(raw, -1) {
			if strings.Contains(string(m[1]), "{{") {
				t.Errorf("%s: an inline <script> contains a template action; "+
					"pass the value through a data-* attribute instead, or the CSP hash will not match the rendered page", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// newProxyTLSUI builds a UI configured for the documented reverse-proxy
// deployment: plain-HTTP listener, server.secureCookies set.
func newProxyTLSUI(t *testing.T) *UI {
	t.Helper()
	authn := &auth.Authenticator{
		Store:    testAccounts(t),
		Sessions: auth.NewSessions(12 * time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	return New(authn, slog.New(slog.DiscardHandler), &Options{
		Version: "0.2.0-test", Mode: "mirror", Store: openTestStore(t),
		SecureCookies: true,
	})
}

// TestCookiesSecureBehindTLSTerminatingProxy covers the NFR-015 fix: with
// server.secureCookies set, the session cookie (and the preference
// cookies) carry the Secure attribute even though the request reached the
// listener in plain HTTP — the documented deploy/ topology, where r.TLS
// is nil on every request. Without the setting, behavior is unchanged.
func TestCookiesSecureBehindTLSTerminatingProxy(t *testing.T) {
	cookieByName := func(w *httptest.ResponseRecorder, name string) *http.Cookie {
		for _, c := range w.Result().Cookies() {
			if c.Name == name {
				return c
			}
		}
		return nil
	}
	loginRec := func(mux *http.ServeMux) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/login",
			strings.NewReader("username=alexis&password=pw-admin&next=%2F"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	secured := mount(newProxyTLSUI(t))
	w := loginRec(secured)
	sess := cookieByName(w, auth.SessionCookie)
	if sess == nil {
		t.Fatal("no session cookie set")
	}
	if !sess.Secure {
		t.Error("session cookie not Secure with server.secureCookies set (NFR-015)")
	}

	// The preference cookies follow the same rule.
	r := httptest.NewRequest(http.MethodPost, "/lang", strings.NewReader("lang=fr&back=%2F"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	secured.ServeHTTP(w, r)
	if lang := cookieByName(w, langCookie); lang == nil || !lang.Secure {
		t.Error("lang cookie not Secure with server.secureCookies set")
	}

	// Default posture unchanged: no proxy declared, plain HTTP, no Secure.
	plain := mount(newTestUI(t, false))
	if sess := cookieByName(loginRec(plain), auth.SessionCookie); sess == nil || sess.Secure {
		t.Error("session cookie Secure without TLS and without server.secureCookies — a plain-HTTP lab instance would lose its session")
	}
}
