// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/federation"
)

// federatedUI is a test UI with OIDC enabled against issuer, and an audit
// capture.
func federatedUI(t *testing.T, issuer string) (*UI, *http.ServeMux, *bytes.Buffer) {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	fed, err := federation.New(&federation.Options{
		OIDC: config.OIDC{Enabled: true, DisplayName: "Corp SSO", Issuer: issuer, ClientID: "tobby",
			ClientSecretFile: secret, RedirectURL: "https://tobby.test/auth/oidc/callback",
			Roles: config.RoleMapping{Viewer: []string{"staff"}}},
		Client: &http.Client{Timeout: 2 * time.Second}, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	u := newTestUI(t, false)
	u.federation = fed
	u.logger = logger
	u.authn.Logger = logger
	u.authn.Federated = fed.Enabled
	mux := mount(u)
	u.MountFederation(mux)
	return u, mux, &buf
}

// TestLoginPageOffersEveryMethod: with federation enabled the sign-in
// page offers the provider button AND the local form (FR-078: enabling
// federation never disables the local methods), the button carries the
// sanitized destination, and the local form says its use is recorded.
func TestLoginPageOffersEveryMethod(t *testing.T) {
	_, mux, _ := federatedUI(t, "https://idp.invalid")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login?next="+url.QueryEscape("//evil.test/x"), http.NoBody))
	body := w.Body.String()
	for _, want := range []string{
		"Sign in with Corp SSO",
		`href="/auth/oidc/login?next=%2F"`, // the open redirect was sanitized away (B-033)
		`id="username"`, "data-local-under-federation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-in page misses %q", want)
		}
	}
	if strings.Contains(body, "data-idp-unavailable") {
		t.Error("the provider is marked unavailable before any probe failed")
	}
}

// TestLoginPageSaysProviderIsDown is the R-20 page: with the provider
// unreachable, the page states it with TBY-AUTH-016 naming the provider,
// keeps the button (it retries), and the local administrator signs in —
// with the dedicated break-glass audit record.
func TestLoginPageSaysProviderIsDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	u, mux, buf := federatedUI(t, dead.URL)
	u.federation.Probe(context.Background())

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login", http.NoBody))
	body := w.Body.String()
	if !strings.Contains(body, "data-idp-unavailable") || !strings.Contains(body, "TBY-AUTH-016") || !strings.Contains(body, "Corp SSO") {
		t.Errorf("the sign-in page does not state the outage with TBY-AUTH-016 and the provider name")
	}
	if !strings.Contains(body, "Sign in with Corp SSO") || !strings.Contains(body, `id="password"`) {
		t.Error("the page dropped the provider button or the local form while the provider is down")
	}

	// The button answers the same code with its 502, on the sign-in page.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/oidc/login?next=%2F", http.NoBody))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "TBY-AUTH-016") {
		t.Errorf("federated sign-in while down: %d, want 502 TBY-AUTH-016", w.Code)
	}

	c := login(t, mux, "alexis", "pw-admin")
	if r := adminGet(t, mux, c, "/admin/accounts"); r.Code != http.StatusOK {
		t.Errorf("the local administrator cannot work while the provider is down: %d", r.Code)
	}
	recs := auditLines(buf.Bytes(), "auth.local_under_federation")
	if len(recs) != 1 || recs[0]["actor"] != "alexis" || recs[0]["target"] != "ui" {
		t.Errorf("break-glass records = %v, want one for alexis on the ui", recs)
	}
	var ok, failed int
	for _, rec := range auditLines(buf.Bytes(), "session.login") {
		switch {
		case rec["outcome"] == "success" && rec["target"] == "ui":
			ok++
		case rec["outcome"] == "failure" && rec["target"] == "oidc":
			failed++
		}
	}
	if ok != 1 || failed != 1 {
		t.Errorf("session.login records: %d local successes and %d federated failures, want 1 and 1", ok, failed)
	}
}

// TestFederatedCallbackRefusals: a callback that does not answer a sign-in
// this browser started renders the sign-in page with TBY-AUTH-014, opens
// no session, and is audited.
func TestFederatedCallbackRefusals(t *testing.T) {
	_, mux, buf := federatedUI(t, "https://idp.invalid")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=x&state=forged", http.NoBody))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "TBY-AUTH-014") {
		t.Errorf("forged callback: %d, want 401 TBY-AUTH-014", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Error("a refused callback set a session cookie")
		}
	}
	recs := auditLines(buf.Bytes(), "session.login")
	if len(recs) != 1 || recs[0]["outcome"] != "denied" || recs[0]["target"] != "oidc" {
		t.Errorf("refusal records = %v", recs)
	}
}

// TestFederatedSessionHasNoLocalPassword: the account screen of a
// federated identity offers no password form, and the POST is refused.
func TestFederatedSessionHasNoLocalPassword(t *testing.T) {
	u, mux, _ := federatedUI(t, "https://idp.invalid")
	sess := u.authn.Sessions.CreateWith("oidc:alexis", auth.RoleViewer, auth.MethodOIDC, time.Now())
	c := &http.Cookie{Name: auth.SessionCookie, Value: sess.ID} //nolint:gosec // G124: a request-side cookie in a test; attributes only exist on Set-Cookie
	body := adminGet(t, mux, c, "/account").Body.String()
	if !strings.Contains(body, "data-account-federated") || strings.Contains(body, `name="current"`) {
		t.Error("the account screen of a federated session offers a local password form")
	}
	form := url.Values{"current": {"pw-admin"}, "new": {"x"}, "confirm": {"x"}, "csrf": {sess.CSRF}}
	r := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "TBY-AUTH-010") {
		t.Errorf("password change on a federated session: %d, want TBY-AUTH-010", w.Code)
	}
}
