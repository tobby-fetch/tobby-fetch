// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/clock"
	"github.com/tobby-fetch/tobby-fetch/internal/diag"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
)

// The operations endpoints of milestone 7, track B (FR-095, FR-096,
// FR-097): roles, the archive as the response, the refusal as a problem
// document and never a partial file, the clock verdicts, the inventory.

type stubBundles struct {
	actors []string
	fail   error
	secret string
}

func (s *stubBundles) Generate(_ context.Context, actor, origin string) (*diag.Bundle, error) {
	s.actors = append(s.actors, actor+"@"+origin)
	if s.fail != nil {
		return nil, s.fail
	}
	secrets := diag.NewSecrets()
	if s.secret != "" {
		secrets.Add("test", s.secret)
	}
	return diag.Build(diag.Options{Secrets: secrets}, &diag.Sources{
		Version: func() ([]byte, error) { return []byte(`{"version":"v1"}`), nil },
		Config:  func() ([]byte, error) { return []byte("mode: mirror\n" + s.secret), nil },
	})
}

func newOperationsAPI(t *testing.T, opts *api.OperationsOptions) *http.ServeMux {
	t.Helper()
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		name string
		role auth.Role
		pass string
	}{{"lecteur", auth.RoleViewer, "pw-view"}, {"op", auth.RoleOperator, "pw-op"}, {"root", auth.RoleAdmin, "pw-root"}} {
		if err := accounts.AddAccount(a.name, a.role, a.pass, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	a := api.New(&auth.Authenticator{Store: accounts, Sessions: auth.NewSessions(time.Hour), Logger: slog.New(slog.DiscardHandler)}, slog.New(slog.DiscardHandler))
	api.RegisterOperations(a, opts)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux
}

func opCall(mux *http.ServeMux, method, path, user, pass string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, http.NoBody)
	r.SetBasicAuth(user, pass)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestDiagnosticsBundleIsAnAdminDownloadThatNamesTheRequester(t *testing.T) {
	b := &stubBundles{}
	mux := newOperationsAPI(t, &api.OperationsOptions{Diagnostics: b})
	for _, who := range [][2]string{{"lecteur", "pw-view"}, {"op", "pw-op"}} {
		w := opCall(mux, http.MethodPost, "/api/v1/diagnostics/bundle", who[0], who[1])
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
			t.Errorf("%s: %d %s, want the role refusal", who[0], w.Code, w.Body)
		}
	}
	if len(b.actors) != 0 {
		t.Fatalf("a bundle was generated for a role below admin: %v", b.actors)
	}
	w := opCall(mux, http.MethodPost, "/api/v1/diagnostics/bundle", "root", "pw-root")
	if w.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="tobby-diagnostics-`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	gz, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("the response is not a gzip archive: %v", err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "MANIFEST.json,version.json,config.yaml" {
		t.Errorf("archive members = %v", names)
	}
	if len(b.actors) != 1 || !strings.HasPrefix(b.actors[0], "root@") {
		t.Errorf("the requester was not passed to the service: %v", b.actors)
	}
}

func TestDiagnosticsBundleRefusalIsAProblemNotAPartialFile(t *testing.T) {
	mux := newOperationsAPI(t, &api.OperationsOptions{Diagnostics: &stubBundles{secret: "planted-secret-value"}})
	w := opCall(mux, http.MethodPost, "/api/v1/diagnostics/bundle", "root", "pw-root")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("Content-Type = %q, want a problem document", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "TBY-DIA-002") || !strings.Contains(body, "config.yaml") {
		t.Errorf("the problem does not name the code and the member: %s", body)
	}
	if strings.Contains(body, "planted-secret-value") {
		t.Error("the refusal leaks the secret")
	}
	mux = newOperationsAPI(t, &api.OperationsOptions{Diagnostics: &stubBundles{fail: errors.New("disk on fire")}})
	if w := opCall(mux, http.MethodPost, "/api/v1/diagnostics/bundle", "root", "pw-root"); !strings.Contains(w.Body.String(), "TBY-DIA-001") {
		t.Errorf("a non-taxonomy failure is not TBY-DIA-001: %s", w.Body)
	}
	mux = newOperationsAPI(t, &api.OperationsOptions{})
	if w := opCall(mux, http.MethodPost, "/api/v1/diagnostics/bundle", "root", "pw-root"); w.Code != http.StatusInternalServerError {
		t.Errorf("no service wired = %d", w.Code)
	}
}

func TestClockEndpointReportsVerdictsToAViewer(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	mon := clock.NewMonitor(time.Hour)
	mon.Now = func() time.Time { return now }
	mux := newOperationsAPI(t, &api.OperationsOptions{Clock: mon, Now: func() time.Time { return now }})

	var empty struct{ Verdicts []map[string]any }
	w := opCall(mux, http.MethodGet, "/api/v1/clock", "lecteur", "pw-view")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &empty) != nil || empty.Verdicts == nil || len(empty.Verdicts) != 0 {
		t.Fatalf("no verdict yet: %d %s", w.Code, w.Body)
	}
	mon.CheckMedium("med_9", now.Add(48*time.Hour))
	w = opCall(mux, http.MethodGet, "/api/v1/clock", "lecteur", "pw-view")
	var doc struct {
		Verdicts []struct {
			Anchor      string `json:"anchor"`
			Medium      string `json:"medium"`
			Implausible bool   `json:"implausible"`
			Code        string `json:"code"`
			GapSeconds  int64  `json:"gap_seconds"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || len(doc.Verdicts) != 1 {
		t.Fatalf("%v: %s", err, w.Body)
	}
	v := doc.Verdicts[0]
	if v.Anchor != "medium" || v.Medium != "med_9" || !v.Implausible || v.Code != "TBY-CLK-002" || v.GapSeconds != 48*3600 {
		t.Errorf("verdict = %+v", v)
	}
}

func TestStateEndpointIsAdminAndNeverReturnsContents(t *testing.T) {
	root := t.TempDir()
	accounts, err := auth.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("someone", auth.RoleViewer, "very-secret-password-hash-input", time.Now()); err != nil {
		t.Fatal(err)
	}
	mux := newOperationsAPI(t, &api.OperationsOptions{State: func() (*statedir.Inventory, error) { return statedir.Inspect(root) }})
	if w := opCall(mux, http.MethodGet, "/api/v1/state", "op", "pw-op"); w.Code != http.StatusForbidden {
		t.Errorf("operator = %d, want 403", w.Code)
	}
	w := opCall(mux, http.MethodGet, "/api/v1/state", "root", "pw-root")
	if w.Code != http.StatusOK {
		t.Fatalf("admin = %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"accounts.yaml"`) || strings.Contains(body, "argon2") || strings.Contains(body, "someone") {
		t.Errorf("the inventory is missing the file or leaks content: %s", body)
	}
	mux = newOperationsAPI(t, &api.OperationsOptions{})
	if w := opCall(mux, http.MethodGet, "/api/v1/state", "root", "pw-root"); w.Code < 400 || !strings.Contains(w.Body.String(), "TBY-CFG") {
		t.Errorf("no state directory = %d %s, want a configuration problem", w.Code, w.Body)
	}
}
