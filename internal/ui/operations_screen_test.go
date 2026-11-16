// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/clock"
	"github.com/tobby-fetch/tobby-fetch/internal/diag"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
)

// The operations screens of milestone 7, track B (FR-095, FR-096, FR-097).

type stubBundles struct {
	actors []string
	secret string
}

func (s *stubBundles) Generate(_ context.Context, actor, origin string) (*diag.Bundle, error) {
	s.actors = append(s.actors, actor+"@"+origin)
	secrets := diag.NewSecrets()
	if s.secret != "" {
		secrets.Add("test", s.secret)
	}
	return diag.Build(diag.Options{Secrets: secrets}, &diag.Sources{
		Version: func() ([]byte, error) { return []byte(`{"version":"v1"}`), nil },
		Config:  func() ([]byte, error) { return []byte("mode: mirror\n" + s.secret), nil },
	})
}

func TestDiagnosticsScreenOffersTheBundleToAnAdminAndDownloadsIt(t *testing.T) {
	svc := &stubBundles{}
	u := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t), Diagnostics: svc}, nil)
	mux := mount(u)
	admin := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, admin, "/admin/diagnostics", nil).Body.String()
	for _, want := range []string{`action="/admin/diagnostics/bundle"`, "Generate a diagnostic bundle", "MANIFEST.json", "TBY-DIA-002", "Tobby never sends it anywhere"} {
		if !strings.Contains(body, want) {
			t.Errorf("the screen lacks %q", want)
		}
	}
	w := postForm(t, mux, admin, "/admin/diagnostics/bundle", "csrf="+csrfOf(t, u, admin), nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/gzip" ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "tobby-diagnostics-") {
		t.Fatalf("POST = %d %v", w.Code, w.Header())
	}
	if len(svc.actors) != 1 || !strings.HasPrefix(svc.actors[0], "alexis@") {
		t.Errorf("the requester was not passed on: %v", svc.actors)
	}
	// Roles below admin: refused before the service is reached.
	op := login(t, mux, "op", "pw-op")
	if w := postForm(t, mux, op, "/admin/diagnostics/bundle", "csrf="+csrfOf(t, u, op), nil); w.Code != http.StatusForbidden {
		t.Errorf("operator = %d, want 403", w.Code)
	}
	if len(svc.actors) != 1 {
		t.Error("a bundle was generated for an operator")
	}
}

func TestDiagnosticsRefusalRendersAnErrorPageNotAFile(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t), Diagnostics: &stubBundles{secret: "planted-secret-value"}}, nil)
	mux := mount(u)
	admin := login(t, mux, "alexis", "pw-admin")
	w := postForm(t, mux, admin, "/admin/diagnostics/bundle", "csrf="+csrfOf(t, u, admin), nil)
	body := w.Body.String()
	if w.Code != http.StatusInternalServerError || !strings.Contains(body, "TBY-DIA-002") {
		t.Fatalf("a refused bundle = %d, want the TBY-DIA-002 error page", w.Code)
	}
	if w.Header().Get("Content-Disposition") != "" || strings.Contains(body, "planted-secret-value") {
		t.Error("the refusal sent a download or leaked the secret")
	}
}

func TestStateScreenListsEntriesAndFlagsABroadSecret(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "accounts.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t),
		State: func() (*statedir.Inventory, error) { return statedir.Inspect(root) }}, nil)
	mux := mount(u)
	admin := login(t, mux, "alexis", "pw-admin")
	body := get(t, mux, admin, "/admin/state", nil).Body.String()
	for _, want := range []string{"accounts.yaml", "media-imports.json", "Identity — back it up", "Discardable", "tobby state inspect"} {
		if !strings.Contains(body, want) {
			t.Errorf("the screen lacks %q", want)
		}
	}
	if strings.Contains(body, "Readable by every user") {
		t.Error("a 0600 entry is flagged")
	}
	if os.PathSeparator == '/' {
		if err := os.Chmod(filepath.Join(root, "accounts.yaml"), 0o644); err != nil { //nolint:gosec // G302: the test loosens the file on purpose
			t.Fatal(err)
		}
		body = get(t, mux, admin, "/admin/state", nil).Body.String()
		if !strings.Contains(body, "Readable by every user of the host: accounts.yaml") {
			t.Error("a world-readable secret entry is not flagged")
		}
	}
	// Without a state directory the screen says so.
	u2 := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t)}, nil)
	mux2 := mount(u2)
	if body := get(t, mux2, login(t, mux2, "alexis", "pw-admin"), "/admin/state", nil).Body.String(); !strings.Contains(body, "runs without a state directory") {
		t.Error("a state-less instance is not told so")
	}
}

// TestImplausibleClockShowsAPersistentBannerThatRetracts: FR-096 — on every
// page, in the viewer's language, naming the code; gone by itself once a
// later check is plausible.
func TestImplausibleClockShowsAPersistentBannerThatRetracts(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	mon := clock.NewMonitor(time.Hour)
	mon.Now = func() time.Time { return now }
	u := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t), Clock: mon}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	if body := get(t, mux, c, "/", nil).Body.String(); strings.Contains(body, "TBY-CLK") {
		t.Fatal("a banner with no implausible verdict")
	}
	mon.CheckMedium("med_late", now.Add(72*time.Hour))
	for _, path := range []string{"/", "/tasks"} {
		body := get(t, mux, c, path, nil).Body.String()
		for _, want := range []string{"TBY-CLK-002", "72h0m0s earlier than", "med_late", "expiries and freshness checks may be wrong"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: banner lacks %q", path, want)
			}
		}
	}
	fr := get(t, mux, c, "/", map[string]string{"Accept-Language": "fr"}).Body.String()
	if !strings.Contains(fr, "les expirations et les contrôles de fraîcheur peuvent être faux") {
		t.Error("the banner is not translated")
	}
	mon.CheckMedium("med_ok", now)
	if body := get(t, mux, c, "/", nil).Body.String(); strings.Contains(body, "TBY-CLK") {
		t.Error("the banner survived a plausible verdict")
	}
}
