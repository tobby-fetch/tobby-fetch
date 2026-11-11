// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// TestTokenExpiryAPI is the API mirror of the R-14 token form: an
// explicit expiry is stored, echoed at creation and listed; an expiry in
// the past is refused; without one, the configured default applies and a
// long-lived token stays long-lived.
func TestTokenExpiryAPI(t *testing.T) {
	mux, store, _ := newAccountsAPI(t)
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	w := adminDo(t, mux, http.MethodPost, "/api/v1/tokens",
		`{"name":"ci","role":"viewer","expires":"`+exp.Format(time.RFC3339)+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create with expiry: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Secret  string    `json:"secret"`
		Expires time.Time `json:"expires"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Expires.Equal(exp) {
		t.Errorf("echoed expiry = %v, want %v", created.Expires, exp)
	}
	if _, st := store.VerifyToken(created.Secret, exp.Add(time.Second)); st != auth.TokenExpired {
		t.Errorf("after the expiry: state %v, want expired", st)
	}

	w = adminDo(t, mux, http.MethodPost, "/api/v1/tokens",
		`{"name":"late","role":"viewer","expires":"2001-01-01T00:00:00Z"}`)
	if w.Code < 400 {
		t.Errorf("expiry in the past: %d, want a refusal", w.Code)
	}

	w = adminDo(t, mux, http.MethodPost, "/api/v1/tokens", `{"name":"forever","role":"viewer"}`)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"expires"`) {
		t.Errorf("long-lived token: %d %s, want 201 without expires", w.Code, w.Body.String())
	}

	store.SetDefaultTokenLifetime(time.Hour)
	w = adminDo(t, mux, http.MethodPost, "/api/v1/tokens", `{"name":"hourly","role":"viewer"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"expires"`) {
		t.Errorf("default lifetime: %d %s, want 201 with expires", w.Code, w.Body.String())
	}

	w = adminDo(t, mux, http.MethodGet, "/api/v1/tokens", "")
	body := w.Body.String()
	if !strings.Contains(body, `"name":"ci"`) || strings.Count(body, `"expires"`) != 2 {
		t.Errorf("listing = %s, want the two expiring tokens with expires and the long-lived one without", body)
	}
}
