// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scanpolicy"
)

// TestPolicyEndpoint serves the effective policy to a viewer: every
// exception with its origin, scope and state at the instant of the
// request — an expiry that passed on the running instance shows at once —
// the Retriever's unbounded exception as such, and both hardening
// switches.
func TestPolicyEndpoint(t *testing.T) {
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", time.Now()); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{Store: accounts, Sessions: auth.NewSessions(time.Hour), Logger: slog.New(slog.DiscardHandler)}
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	cfg := config.Default()
	cfg.Scan.Mode = config.ScanModeBlock
	cfg.Scan.Exceptions = []config.ScanException{
		{ID: "CVE-2024-11111", Justification: "parser disabled", Expires: now.Add(24 * time.Hour), Scope: config.ExceptionScope{Recipe: "wordpress"}},
		{ID: "CVE-2024-22222", Justification: "legacy", Expires: now.Add(-time.Hour)},
	}
	src := scanpolicy.New(&scanpolicy.Options{
		Scan:      &cfg.Scan,
		Hardening: hardening.Switches{DisableRegistryPush: true},
		Retriever: func() ([]scan.Exception, bool) {
			return []scan.Exception{{ID: "CVE-2024-33333", Level: scan.LevelRetriever}}, true
		},
		Now: func() time.Time { return now },
	})
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterPolicy(a, src)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())

	r := httptest.NewRequest(http.MethodGet, "/api/v1/policy", http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/policy = %d: %s", w.Code, w.Body)
	}
	var v scanpolicy.View
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Mode != "block" || !v.Blocking || !v.RetrieverKnown || !v.Hardening.DisableRegistryPush || v.Hardening.DisableUnitImport {
		t.Errorf("policy = %+v", v)
	}
	want := map[string]string{"CVE-2024-11111": "active", "CVE-2024-22222": "expired", "CVE-2024-33333": "unbounded"}
	if len(v.Exceptions) != 3 {
		t.Fatalf("exceptions = %+v", v.Exceptions)
	}
	for _, ex := range v.Exceptions {
		if want[ex.ID] != ex.State {
			t.Errorf("%s: state %s, want %s", ex.ID, ex.State, want[ex.ID])
		}
	}
	if v.Exceptions[0].Scope == nil || v.Exceptions[0].Scope.Recipe != "wordpress" || v.Exceptions[0].Origin != "instance" ||
		v.Exceptions[2].Origin != "retriever" || v.Exceptions[2].Expires != nil {
		t.Errorf("exceptions = %+v", v.Exceptions)
	}

	// Anonymous: refused.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/policy", http.NoBody)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/v1/policy = %d, want 401", w.Code)
	}
}
