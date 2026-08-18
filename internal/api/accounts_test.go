// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// newAccountsAPI mounts the account endpoints behind real Basic
// authentication: an admin and a viewer account exist. The authenticator
// is returned so tests can seed and inspect UI sessions (R-34).
func newAccountsAPI(t *testing.T) (*http.ServeMux, *auth.Store, *auth.Authenticator) {
	t.Helper()
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("alexis", auth.RoleAdmin, "pw-admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", time.Now()); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterAccounts(a, accounts)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, accounts, authn
}

func adminDo(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, http.NoBody)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.SetBasicAuth("alexis", "pw-admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestAccountsEndpointAdminOnly: the mirror of /admin/accounts is
// admin-gated (ADR-0009) — a viewer gets the taxonomized 403 (FR-061).
func TestAccountsEndpointAdminOnly(t *testing.T) {
	mux, _, _ := newAccountsAPI(t)

	for _, target := range []string{"/api/v1/accounts", "/api/v1/tokens"} {
		r := httptest.NewRequest(http.MethodGet, target, http.NoBody)
		r.SetBasicAuth("lecteur", "pw-view")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
			t.Errorf("viewer %s = %d, want taxonomized 403; body=%s", target, w.Code, w.Body.String())
		}
	}

	w := adminDo(t, mux, http.MethodGet, "/api/v1/accounts", "")
	if w.Code != http.StatusOK {
		t.Fatalf("accounts = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Accounts []struct {
			Name    string `json:"name"`
			Role    string `json:"role"`
			Created string `json:"created"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Accounts) != 2 || resp.Accounts[0].Name != "alexis" || resp.Accounts[0].Role != "admin" {
		t.Errorf("unexpected accounts payload: %+v", resp.Accounts)
	}
	// Never a hash in the listing (NFR-015).
	if strings.Contains(w.Body.String(), "argon2") || strings.Contains(w.Body.String(), "hash") {
		t.Error("accounts listing leaks credential material")
	}
}

// TestTokenLifecycle covers the FR-072 loop: create (201, secret exactly
// once), authenticate with the secret, revoke, secret dead.
func TestTokenLifecycle(t *testing.T) {
	mux, store, _ := newAccountsAPI(t)

	w := adminDo(t, mux, http.MethodPost, "/api/v1/tokens", `{"name":"ci-import","role":"operator"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Name   string `json:"name"`
		Role   string `json:"role"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "ci-import" || created.Role != "operator" || !strings.HasPrefix(created.Secret, "tby_") {
		t.Fatalf("unexpected creation payload: %+v", created)
	}

	// The listing shows the token but never the secret (FR-072).
	w = adminDo(t, mux, http.MethodGet, "/api/v1/tokens", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ci-import"`) {
		t.Fatalf("tokens listing = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), created.Secret) {
		t.Error("token listing re-displays the secret")
	}

	// The secret authenticates as a Bearer with the token's role: operator
	// is not admin, so the accounts mirror stays closed.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+created.Secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "TBY-AUTH-003") {
		t.Errorf("operator token on /accounts = %d, want taxonomized 403", rec.Code)
	}

	// A duplicate active name is refused.
	w = adminDo(t, mux, http.MethodPost, "/api/v1/tokens", `{"name":"ci-import","role":"viewer"}`)
	if w.Code == http.StatusCreated {
		t.Error("duplicate token name accepted")
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Errorf("duplicate refusal is not a problem document: %s", w.Header().Get("Content-Type"))
	}

	// Revocation is immediate and permanent.
	w = adminDo(t, mux, http.MethodPost, "/api/v1/tokens/ci-import/revoke", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revoked":true`) {
		t.Fatalf("revoke = %d: %s", w.Code, w.Body.String())
	}
	if _, ok := store.VerifyToken(created.Secret); ok {
		t.Error("secret still verifies after revocation (FR-072)")
	}

	// Revoking an unknown token answers the shared taxonomized 404.
	w = adminDo(t, mux, http.MethodPost, "/api/v1/tokens/nope/revoke", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown revoke = %d, want taxonomized 404; body=%s", w.Code, w.Body.String())
	}
}

// viewerChange posts a password-change body as the viewer account.
func viewerChange(t *testing.T, mux *http.ServeMux, pass, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("lecteur", pass)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestPasswordChangeEndpoint covers the R-34/FR-061 mirror: any
// authenticated role changes its own password with the same rules and
// taxonomy codes as the /account screen; UI sessions of the account die
// with the old credential.
func TestPasswordChangeEndpoint(t *testing.T) {
	mux, store, authn := newAccountsAPI(t)

	// Anonymous: refused by the surface with a problem document.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/account/password",
		strings.NewReader(`{"current":"x","new":"y"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "TBY-AUTH-002") {
		t.Errorf("anonymous change = %d, want taxonomized 401", w.Code)
	}

	// Wrong current password: 422 with the dedicated code, no change.
	w = viewerChange(t, mux, "pw-view", `{"current":"nope","new":"pw-2"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-006") {
		t.Errorf("wrong current = %d, want taxonomized 422; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("refusal is not a problem document: %s", ct)
	}

	// Invalid new password: empty or identical to the current one.
	for name, body := range map[string]string{
		"empty":     `{"current":"pw-view","new":""}`,
		"identical": `{"current":"pw-view","new":"pw-view"}`,
	} {
		w = viewerChange(t, mux, "pw-view", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-007") {
			t.Errorf("%s new = %d, want taxonomized 422 TBY-AUTH-007", name, w.Code)
		}
	}
	if _, ok := store.VerifyPassword("lecteur", "pw-view", time.Now()); !ok {
		t.Fatal("password changed despite the refusals")
	}

	// Success: 204, the new password verifies, the old one dies, and every
	// UI session of the account is closed (the API caller holds none).
	sess := authn.Sessions.Create("lecteur", auth.RoleViewer, time.Now())
	foreign := authn.Sessions.Create("alexis", auth.RoleAdmin, time.Now())
	w = viewerChange(t, mux, "pw-view", `{"current":"pw-view","new":"pw-2"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("change = %d: %s", w.Code, w.Body.String())
	}
	if _, ok := store.VerifyPassword("lecteur", "pw-2", time.Now()); !ok {
		t.Error("new password does not verify")
	}
	if _, ok := store.VerifyPassword("lecteur", "pw-view", time.Now()); ok {
		t.Error("old password still verifies")
	}
	if _, ok := authn.Sessions.Get(sess.ID, time.Now()); ok {
		t.Error("UI session of the account survived the API password change")
	}
	if _, ok := authn.Sessions.Get(foreign.ID, time.Now()); !ok {
		t.Error("session of another account was collateral damage")
	}

	// A token authenticates but carries no password: the current-password
	// check fails like any wrong credential (documented behavior).
	secret, _, err := store.CreateToken("ci", auth.RoleOperator, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/account/password",
		strings.NewReader(`{"current":"whatever","new":"pw-3"}`))
	r.Header.Set("Authorization", "Bearer "+secret)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-006") {
		t.Errorf("token caller = %d, want taxonomized 422 TBY-AUTH-006", w.Code)
	}
}

// problemCode reads the RFC 9457 taxonomy code of a problem document.
func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem document: %v (%s)", err, w.Body.String())
	}
	return p.Code
}

// TestAccountCreateEndpoint is the FR-073 mirror of the creation form: the
// instance derives the hash (FR-066), and the created account authenticates
// immediately with the role it was given.
func TestAccountCreateEndpoint(t *testing.T) {
	mux, store, _ := newAccountsAPI(t)

	w := adminDo(t, mux, http.MethodPost, "/api/v1/accounts",
		`{"name":"claire","role":"operator","password":"pw-claire"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Account struct {
			Name string    `json:"name"`
			Role auth.Role `json:"role"`
		} `json:"account"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Account.Name != "claire" || created.Account.Role != auth.RoleOperator {
		t.Errorf("created account = %+v", created.Account)
	}
	if strings.Contains(w.Body.String(), "hash") || strings.Contains(w.Body.String(), "pw-claire") {
		t.Errorf("the response leaks credential material: %s", w.Body.String())
	}
	if _, ok := store.VerifyPassword("claire", "pw-claire", time.Now()); !ok {
		t.Error("the created account does not authenticate")
	}

	// Validation and duplication carry their own codes.
	for body, want := range map[string]string{
		`{"name":"","role":"viewer","password":"pw"}`:      "TBY-AUTH-008",
		`{"name":"x","role":"root","password":"pw"}`:       "TBY-AUTH-008",
		`{"name":"x","role":"viewer","password":""}`:       "TBY-AUTH-008",
		`{"name":"claire","role":"viewer","password":"p"}`: "TBY-AUTH-009",
	} {
		w := adminDo(t, mux, http.MethodPost, "/api/v1/accounts", body)
		if got := problemCode(t, w); got != want {
			t.Errorf("%s: code = %q, want %q", body, got, want)
		}
	}
}

// TestAccountRoleAndDeleteEndpoints: the FR-074 role change and the FR-073
// removal, with the session invalidation both imply.
func TestAccountRoleAndDeleteEndpoints(t *testing.T) {
	mux, store, authn := newAccountsAPI(t)
	sess := authn.Sessions.Create("lecteur", auth.RoleViewer, time.Now())

	w := adminDo(t, mux, http.MethodPatch, "/api/v1/accounts/lecteur", `{"role":"operator"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("role change = %d: %s", w.Code, w.Body.String())
	}
	if acct, _ := store.Account("lecteur"); acct.Role != auth.RoleOperator {
		t.Errorf("role = %s, want operator", acct.Role)
	}
	if _, ok := authn.Sessions.Get(sess.ID, time.Now()); ok {
		t.Error("the account kept a session opened with its former role")
	}

	if w := adminDo(t, mux, http.MethodDelete, "/api/v1/accounts/lecteur", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	if _, ok := store.Account("lecteur"); ok {
		t.Error("account still present after the delete")
	}

	for _, c := range []struct {
		method, target, body, want string
	}{
		{http.MethodPatch, "/api/v1/accounts/ghost", `{"role":"viewer"}`, "TBY-AUTH-010"},
		{http.MethodDelete, "/api/v1/accounts/ghost", "", "TBY-AUTH-010"},
		{http.MethodPatch, "/api/v1/accounts/alexis", `{"role":"root"}`, "TBY-AUTH-008"},
		// The last administrator is protected on this surface too (FR-005).
		{http.MethodPatch, "/api/v1/accounts/alexis", `{"role":"viewer"}`, "TBY-AUTH-011"},
		{http.MethodDelete, "/api/v1/accounts/alexis", "", "TBY-AUTH-011"},
	} {
		w := adminDo(t, mux, c.method, c.target, c.body)
		if got := problemCode(t, w); got != c.want {
			t.Errorf("%s %s: code = %q, want %q", c.method, c.target, got, c.want)
		}
	}
	if acct, ok := store.Account("alexis"); !ok || acct.Role != auth.RoleAdmin {
		t.Error("the last administrator lost its role or its existence")
	}
}
