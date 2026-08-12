// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// adminGet performs a session-authenticated GET.
func adminGet(t *testing.T, mux *http.ServeMux, c *http.Cookie, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// adminPost performs a session-authenticated form POST with the CSRF token.
func adminPost(t *testing.T, u *UI, mux *http.ServeMux, c *http.Cookie, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess, ok := u.authn.Sessions.Get(c.Value, t0)
	if !ok {
		t.Fatal("no session for cookie")
	}
	form.Set("csrf", sess.CSRF)
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestAdminAccountsGating: the screen is admin-only — operator and viewer
// get the taxonomized 403 naming the required role (UI-SPEC §5.9).
func TestAdminAccountsGating(t *testing.T) {
	u := newTestUI(t, false)
	if err := u.authn.Store.AddAccount("op", auth.RoleOperator, "pw-op", t0); err != nil {
		t.Fatal(err)
	}
	mux := mount(u)

	for user, pass := range map[string]string{"op": "pw-op", "lecteur": "pw-view"} {
		c := login(t, mux, user, pass)
		w := adminGet(t, mux, c, "/admin/accounts")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
			t.Errorf("%s on /admin/accounts = %d, want taxonomized 403", user, w.Code)
		}
		if !strings.Contains(w.Body.String(), "admin") {
			t.Errorf("%s refusal does not name the required role", user)
		}
	}
}

// TestAdminAccountsScreen: accounts table, CLI note, empty token state,
// and the hx-history exclusion of the page (ADR-0015 §5).
func TestAdminAccountsScreen(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminGet(t, mux, c, "/admin/accounts")
	if w.Code != http.StatusOK {
		t.Fatalf("admin/accounts = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"alexis", "lecteur", // accounts table
		"tobby user",             // password management is CLI-only at J2
		`hx-history="false"`,     // secret screen excluded from history cache
		"No API token yet.",      // empty token state
		"/admin/accounts/tokens", // creation form
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page misses %q", want)
		}
	}

	// Other pages keep the history cache attribute off.
	w = adminGet(t, mux, c, "/")
	if strings.Contains(w.Body.String(), "hx-history") {
		t.Error("dashboard carries hx-history, expected on the admin page only")
	}
}

var secretRe = regexp.MustCompile(`tby_[A-Za-z0-9_-]+`)

// TestTokenCreateSecretShownOnce: creation renders the secret exactly
// once; the next GET never re-displays it (FR-072).
func TestTokenCreateSecretShownOnce(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminPost(t, u, mux, c, "/admin/accounts/tokens",
		url.Values{"name": {"ci-import"}, "role": {"operator"}})
	if w.Code != http.StatusOK {
		t.Fatalf("token create = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	secret := secretRe.FindString(body)
	if secret == "" {
		t.Fatal("creation response does not display the secret")
	}
	if !strings.Contains(body, "never be shown again") {
		t.Error("secret block misses the one-time warning")
	}
	if !strings.Contains(body, `data-copy="`+secret+`"`) {
		t.Error("secret block misses the copy chip")
	}
	// The secret authenticates with the requested role.
	if tok, ok := u.authn.Store.VerifyToken(secret); !ok || tok.Role != auth.RoleOperator {
		t.Errorf("created secret does not verify as operator: %+v ok=%v", tok, ok)
	}

	// The next GET lists the token but never the secret.
	w = adminGet(t, mux, c, "/admin/accounts")
	if got := secretRe.FindString(w.Body.String()); got != "" {
		t.Errorf("secret re-displayed on a later GET: %q", got)
	}
	if !strings.Contains(w.Body.String(), "ci-import") {
		t.Error("token listing misses the created token")
	}

	// A duplicate active name re-renders the form with the message.
	w = adminPost(t, u, mux, c, "/admin/accounts/tokens",
		url.Values{"name": {"ci-import"}, "role": {"viewer"}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already carries this name") {
		t.Errorf("duplicate name = %d, want 409 with the inline message", w.Code)
	}
}

// TestTokenRevoke: the dialog-confirmed POST revokes immediately, shows a
// toast, and the token dies (FR-072).
func TestTokenRevoke(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminPost(t, u, mux, c, "/admin/accounts/tokens",
		url.Values{"name": {"old-ci"}, "role": {"viewer"}})
	secret := secretRe.FindString(w.Body.String())
	if secret == "" {
		t.Fatal("no secret in creation response")
	}
	// The revocation dialog is a native <dialog>, never confirm().
	if !strings.Contains(w.Body.String(), "<dialog") {
		t.Error("revocation confirmation is not the native <dialog> component")
	}

	w = adminPost(t, u, mux, c, "/admin/accounts/tokens/revoke", url.Values{"name": {"old-ci"}})
	if w.Code != http.StatusOK {
		t.Fatalf("revoke = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "t-toast--success") {
		t.Error("revocation misses the confirmation toast")
	}
	if !strings.Contains(w.Body.String(), "revoked") {
		t.Error("revoked token misses its status badge")
	}
	if _, ok := u.authn.Store.VerifyToken(secret); ok {
		t.Error("secret still verifies after revocation")
	}

	// Unknown token: taxonomized 404.
	w = adminPost(t, u, mux, c, "/admin/accounts/tokens/revoke", url.Values{"name": {"nope"}})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown revoke = %d, want taxonomized 404", w.Code)
	}
}

// TestTokenMutationsRequireCSRF: the creation POST without the session's
// CSRF token is refused (NFR-012).
func TestTokenMutationsRequireCSRF(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	r := httptest.NewRequest(http.MethodPost, "/admin/accounts/tokens",
		strings.NewReader("name=x&role=viewer"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-004") {
		t.Errorf("create without csrf = %d, want taxonomized 403", w.Code)
	}
	if len(u.authn.Store.Tokens()) != 0 {
		t.Error("token created despite the CSRF refusal")
	}
}
