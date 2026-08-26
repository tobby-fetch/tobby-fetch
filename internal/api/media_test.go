// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// The destination-side media endpoints (FR-052, FR-060, FR-061). The
// engine is stubbed here on purpose: what a real verification decides is
// locked in the engine's own tests, against real media and a real
// registry. What this surface owes is a contract — the report travels
// whole, the waivers are an administrator's, and an import becomes a
// tracked task — and a stub is the only way to assert that contract
// without re-testing the engine through HTTP.

// stubVerifier answers a fixed report and records what it was asked.
type stubVerifier struct {
	zone    string
	report  *media.Report
	err     error
	lastOpt engine.MediaOptions
	calls   int
}

func (s *stubVerifier) Zone() string { return s.zone }

func (s *stubVerifier) MediaSummary() engine.MediaSummary {
	return engine.MediaSummary{
		Zone: s.zone, Root: "/mnt/medium", MediaID: "20260826T000000Z-abcdef",
		LastImport: &media.ImportRecord{MediaID: "20260701T000000Z-999999", RunID: "run_old"},
	}
}

func (s *stubVerifier) VerifyMedia(_ context.Context, _ *slog.Logger, opts engine.MediaOptions) (*media.Report, error) {
	s.calls++
	s.lastOpt = opts
	return s.report, s.err
}

// newMediaAPI mounts the media endpoints behind real authentication, with
// one account per role (FR-076).
func newMediaAPI(t *testing.T, v *stubVerifier) (*http.ServeMux, *tasks.Queue) {
	t.Helper()
	root := t.TempDir()
	q, err := tasks.Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, a := range []struct {
		name string
		role auth.Role
		pass string
	}{
		{"lecteur", auth.RoleViewer, "pw-view"},
		{"op", auth.RoleOperator, "pw-op"},
		{"chef", auth.RoleAdmin, "pw-admin"},
	} {
		if err := accounts.AddAccount(a.name, a.role, a.pass, now); err != nil {
			t.Fatal(err)
		}
	}
	authn := &auth.Authenticator{
		Store: accounts, Sessions: auth.NewSessions(time.Hour),
		Logger: slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	opts := &api.MediaOptions{Queue: q, StorageRoot: "/mnt/medium"}
	if v != nil {
		opts.Engine = v
	}
	api.RegisterMedia(a, opts)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, q
}

// mediaCall issues one authenticated request.
func mediaCall(t *testing.T, mux *http.ServeMux, method, path, user, pass, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth(user, pass)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// blockedReport is a medium refused for the zone it is addressed to.
func blockedReport() *media.Report {
	return &media.Report{
		Verdict: media.VerdictBlocked,
		Blocks: []media.Block{{
			Code:        "TBY-MED-006",
			Params:      map[string]string{"expected": "zone-alpha", "found": "zone-beta"},
			Overridable: true,
		}},
		Zone: media.ZoneCheck{Expected: "zone-alpha", Found: "zone-beta"},
		Recipes: []media.RecipeVerdict{{
			Name: "wordpress", Version: "6.8.2", Pushable: false,
			Reason: &media.Reason{Code: "TBY-MED-012", Path: "docker/registry/v2/blobs/sha256/ab/abcd/data"},
		}},
		Findings: []media.Finding{{Code: "TBY-MED-020", Path: "meta/stray.pub"}},
	}
}

// TestMediaVerifyAnswersTheReportWhateverTheVerdict: a blocked medium is
// a successful verification of a bad medium. Answering an error status
// would deny the caller the one document that says why — and that
// document is what the screen, the CLI and a caller's own tooling all
// render (FR-061, FR-063).
func TestMediaVerifyAnswersTheReportWhateverTheVerdict(t *testing.T) {
	v := &stubVerifier{zone: "zone-alpha", report: blockedReport()}
	mux, _ := newMediaAPI(t, v)

	w := mediaCall(t, mux, http.MethodPost, "/api/v1/media/verify", "op", "pw-op", "{}")
	if w.Code != http.StatusOK {
		t.Fatalf("verify = %d, want 200 with the report: %s", w.Code, w.Body)
	}
	var got media.Report
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("the response is not a report: %v\n%s", err, w.Body)
	}
	if got.Verdict != media.VerdictBlocked {
		t.Errorf("verdict = %q, want %q", got.Verdict, media.VerdictBlocked)
	}
	// The refusal names why and WHICH FILE, both halves (FR-054).
	if len(got.Recipes) != 1 || got.Recipes[0].Reason == nil ||
		got.Recipes[0].Reason.Path != "docker/registry/v2/blobs/sha256/ab/abcd/data" {
		t.Errorf("recipe verdicts = %+v, want the offending file named", got.Recipes)
	}
	// And it carries codes, never sentences: a surface re-renders it in
	// the reader's language long after the fact.
	if strings.Contains(w.Body.String(), "addressed to another zone") {
		t.Error("the report carries a rendered sentence; it must carry codes and parameters")
	}
	if len(got.Findings) != 1 {
		t.Errorf("findings = %+v, want the extraneous content reported (FR-054)", got.Findings)
	}
}

// TestMediaWaiversAreAdministratorOnly is the enforcement of what FR-054
// calls "admin-overridable": a role floor on the endpoint cannot express
// it, because verifying and importing are operator actions and waiving is
// not.
func TestMediaWaiversAreAdministratorOnly(t *testing.T) {
	v := &stubVerifier{zone: "zone-alpha", report: blockedReport()}
	mux, q := newMediaAPI(t, v)

	for _, path := range []string{"/api/v1/media/verify", "/api/v1/media/import"} {
		w := mediaCall(t, mux, http.MethodPost, path, "op", "pw-op", `{"allowZoneMismatch":true}`)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
			t.Errorf("%s waived by an operator = %d %s, want the role refusal", path, w.Code, w.Body)
		}
	}
	if v.calls != 0 {
		t.Errorf("verification ran %d times for a refused waiver: the refusal must precede the work", v.calls)
	}
	if n := len(q.List("", tasks.TypeMediaImport, "")); n != 0 {
		t.Errorf("%d import tasks were created by a refused waiver", n)
	}

	// The same request from an administrator carries the waiver through
	// to the engine, named.
	w := mediaCall(t, mux, http.MethodPost, "/api/v1/media/verify", "chef", "pw-admin",
		`{"allowZoneMismatch":true,"allowStale":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("admin verify = %d: %s", w.Code, w.Body)
	}
	if !v.lastOpt.AllowZoneMismatch || !v.lastOpt.AllowStale {
		t.Errorf("the engine received %+v, want both waivers", v.lastOpt)
	}

	// An operator without a waiver is not refused: the floor is operator.
	w = mediaCall(t, mux, http.MethodPost, "/api/v1/media/verify", "op", "pw-op", "")
	if w.Code != http.StatusOK {
		t.Errorf("plain operator verify = %d, want 200: the floor is operator, not admin", w.Code)
	}
}

// TestMediaImportEnqueuesATaskCarryingItsWaivers: the waiver is persisted
// on the task, not merely logged — FR-075 asks a lowered barrier to be
// visible, and the task record is what an operator opens weeks later.
func TestMediaImportEnqueuesATaskCarryingItsWaivers(t *testing.T) {
	v := &stubVerifier{zone: "zone-alpha", report: blockedReport()}
	mux, q := newMediaAPI(t, v)

	w := mediaCall(t, mux, http.MethodPost, "/api/v1/media/import", "chef", "pw-admin",
		`{"allowStale":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("import = %d, want 201: %s", w.Code, w.Body)
	}
	var resp struct {
		Task struct {
			ID             string   `json:"id"`
			Type           string   `json:"type"`
			Reference      string   `json:"reference"`
			Actor          string   `json:"actor"`
			MediaOverrides []string `json:"media_overrides"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Task.Type != tasks.TypeMediaImport {
		t.Errorf("task type = %q, want %q", resp.Task.Type, tasks.TypeMediaImport)
	}
	if resp.Task.Reference != "/mnt/medium" {
		t.Errorf("task reference = %q, want the transported store", resp.Task.Reference)
	}
	if resp.Task.Actor != "chef" {
		t.Errorf("task actor = %q, want the authenticated identity (FR-094)", resp.Task.Actor)
	}
	if len(resp.Task.MediaOverrides) != 1 || resp.Task.MediaOverrides[0] != tasks.OverrideFreshness {
		t.Errorf("task overrides = %v, want [%s] named on the record",
			resp.Task.MediaOverrides, tasks.OverrideFreshness)
	}
	// The import is verification's business, not the endpoint's: creating
	// the task must not have verified anything yet.
	if v.calls != 0 {
		t.Errorf("the endpoint verified %d times; the task runner owns the FR-054 order", v.calls)
	}
	if _, ok := q.Get(resp.Task.ID); !ok {
		t.Error("the task was answered but not enqueued")
	}
}

// TestMediaSummaryNeedsOnlyAViewer: reading which medium is plugged in
// and when the zone last imported is an inventory view, and it costs
// nothing — unlike verification, which re-hashes the whole disk.
func TestMediaSummaryNeedsOnlyAViewer(t *testing.T) {
	v := &stubVerifier{zone: "zone-alpha"}
	mux, _ := newMediaAPI(t, v)

	w := mediaCall(t, mux, http.MethodGet, "/api/v1/media", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("summary = %d: %s", w.Code, w.Body)
	}
	var got engine.MediaSummary
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Zone != "zone-alpha" || got.MediaID == "" {
		t.Errorf("summary = %+v, want the zone and the medium's identity (R-28)", got)
	}
	if got.LastImport == nil || got.LastImport.MediaID == "" {
		t.Errorf("summary carries no freshness record: %+v", got.LastImport)
	}

	// Verifying is not: it is work, and a viewer may not start it.
	w = mediaCall(t, mux, http.MethodPost, "/api/v1/media/verify", "lecteur", "pw-view", "{}")
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer verify = %d, want 403", w.Code)
	}
}

// TestMediaEndpointsRefuseAnInstanceWithNoMedium: an instance that is not
// the destination side of a physical transfer says so, with the
// configuration code and the setting to change — never a 500 and never an
// empty report.
func TestMediaEndpointsRefuseAnInstanceWithNoMedium(t *testing.T) {
	mux, _ := newMediaAPI(t, nil)
	for _, c := range []struct{ method, path, user, pass string }{
		{http.MethodGet, "/api/v1/media", "lecteur", "pw-view"},
		{http.MethodPost, "/api/v1/media/verify", "op", "pw-op"},
		{http.MethodPost, "/api/v1/media/import", "op", "pw-op"},
	} {
		w := mediaCall(t, mux, c.method, c.path, c.user, c.pass, "{}")
		if w.Code == http.StatusOK || w.Code == http.StatusCreated {
			t.Errorf("%s %s = %d on an instance with no medium", c.method, c.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "TBY-CFG-001") {
			t.Errorf("%s %s = %s, want the configuration code", c.method, c.path, w.Body)
		}
	}
}

// TestMediaVerifyPropagatesAnInfrastructureFailure: a store that cannot
// be opened at all is not a verdict about the medium, and must not be
// dressed up as one.
func TestMediaVerifyPropagatesAnInfrastructureFailure(t *testing.T) {
	v := &stubVerifier{zone: "zone-alpha", err: errors.New("opening the transported store: input/output error")}
	mux, _ := newMediaAPI(t, v)

	w := mediaCall(t, mux, http.MethodPost, "/api/v1/media/verify", "op", "pw-op", "{}")
	if w.Code == http.StatusOK {
		t.Fatalf("verify = 200 although the store could not be read: %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), "TBY-SRV-001") {
		t.Errorf("body = %s, want the internal code, not a media verdict", w.Body)
	}
	// NFR-015: the wrapped cause is for logs, never for the response.
	if strings.Contains(w.Body.String(), "input/output error") {
		t.Error("the problem document leaks the wrapped technical cause")
	}
}
