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
// authentication: an admin and a viewer account exist.
func newAccountsAPI(t *testing.T) (*http.ServeMux, *auth.Store) {
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
	return mux, accounts
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
	mux, _ := newAccountsAPI(t)

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
	mux, store := newAccountsAPI(t)

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
