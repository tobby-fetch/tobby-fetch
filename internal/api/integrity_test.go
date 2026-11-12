// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The store-integrity endpoints (FR-057, FR-060, FR-061). The walk itself
// is proved in internal/integrity/check against a real store with real
// corruption, and end to end in internal/cli; what this surface owes is a
// contract: the request becomes a tracked task, a second one is a 409 that
// names the first, and the last report is served whole.

type stubIntegrity struct {
	started []string // "actor@origin"
	running string
	latest  *tasks.Task
}

func (s *stubIntegrity) Start(actor, origin string) (*tasks.Task, error) {
	if s.running != "" {
		return nil, taxonomy.New(taxonomy.CodeIntegrityRunning, taxonomy.Params{"task": s.running})
	}
	s.started = append(s.started, actor+"@"+origin)
	t := &tasks.Task{ID: "tsk_verify", Type: tasks.TypeStoreVerify, Status: tasks.StatusPending, Actor: actor, Origin: origin}
	s.running = t.ID
	return t, nil
}

func (s *stubIntegrity) Cancel() bool { c := s.running != ""; s.running = ""; return c }

func (s *stubIntegrity) Latest() (*tasks.Task, bool) { return s.latest, s.latest != nil }

func newIntegrityAPI(t *testing.T, v api.IntegrityVerifier) *http.ServeMux {
	t.Helper()
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		name string
		role auth.Role
		pass string
	}{{"lecteur", auth.RoleViewer, "pw-view"}, {"op", auth.RoleOperator, "pw-op"}} {
		if err := accounts.AddAccount(a.name, a.role, a.pass, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	a := api.New(&auth.Authenticator{Store: accounts, Sessions: auth.NewSessions(time.Hour), Logger: slog.New(slog.DiscardHandler)}, slog.New(slog.DiscardHandler))
	api.RegisterIntegrity(a, &api.IntegrityOptions{Verifier: v})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux
}

// TestStoreVerifyEnqueuesATaskAndRefusesASecond: 201 with the task for the
// first request, naming the requester and an origin; 409 TBY-INT-004 with
// the task in flight for the second.
func TestStoreVerifyEnqueuesATaskAndRefusesASecond(t *testing.T) {
	v := &stubIntegrity{}
	mux := newIntegrityAPI(t, v)

	w := mediaCall(t, mux, http.MethodPost, "/api/v1/store/verify", "op", "pw-op", "")
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/store/verify = %d (%s), want 201", w.Code, w.Body)
	}
	var created struct {
		Task struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Task.ID != "tsk_verify" || created.Task.Type != tasks.TypeStoreVerify {
		t.Errorf("task = %+v, want the store-verify task", created.Task)
	}
	if len(v.started) != 1 || v.started[0] == "op@" {
		t.Errorf("the service was started with %v: the requester and the client origin must both reach it", v.started)
	}

	w = mediaCall(t, mux, http.MethodPost, "/api/v1/store/verify", "op", "pw-op", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("second POST = %d, want 409", w.Code)
	}
	var problem taxonomy.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != taxonomy.CodeIntegrityRunning {
		t.Errorf("problem code = %s, want %s", problem.Code, taxonomy.CodeIntegrityRunning)
	}
}

// TestStoreVerificationServesTheLastReport: null before any verification,
// then the report whole — codes and parameters, never sentences — with the
// task that produced it.
func TestStoreVerificationServesTheLastReport(t *testing.T) {
	v := &stubIntegrity{}
	mux := newIntegrityAPI(t, v)

	w := mediaCall(t, mux, http.MethodGet, "/api/v1/store/verification", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 for a viewer", w.Code)
	}
	var none map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &none); err != nil {
		t.Fatal(err)
	}
	if got, present := none["verification"]; !present || got != nil {
		t.Errorf("before any verification: %v, want an explicit null", none)
	}

	v.latest = &tasks.Task{ID: "tsk_verify", Status: tasks.StatusFailed, Verification: &integrity.Report{
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
		Verdict: integrity.VerdictDefects, DefectCount: 1,
		Defects: []integrity.Defect{{
			Code: taxonomy.CodeIntegrityBlobDigest, Path: "docker/registry/v2/blobs/sha256/ab/abcd/data",
			Params: map[string]string{"expected": "sha256:aa", "actual": "sha256:bb"},
		}},
	}}
	w = mediaCall(t, mux, http.MethodGet, "/api/v1/store/verification", "lecteur", "pw-view", "")
	var got struct {
		TaskID       string            `json:"task_id"`
		Status       tasks.Status      `json:"status"`
		Verification *integrity.Report `json:"verification"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "tsk_verify" || got.Status != tasks.StatusFailed || got.Verification == nil ||
		got.Verification.Verdict != integrity.VerdictDefects || len(got.Verification.Defects) != 1 ||
		got.Verification.Defects[0].Code != taxonomy.CodeIntegrityBlobDigest || got.Verification.Defects[0].Params["expected"] != "sha256:aa" {
		t.Errorf("document = %s, want the report whole", w.Body)
	}
}

// TestStoreVerifyOnAnInstanceWithoutAQueueSaysSo: an instance with no
// service answers the taxonomized configuration error, never a panic.
func TestStoreVerifyOnAnInstanceWithoutAQueueSaysSo(t *testing.T) {
	mux := newIntegrityAPI(t, nil)
	for _, c := range []struct{ method, path, user, pass string }{
		{http.MethodGet, "/api/v1/store/verification", "lecteur", "pw-view"},
		{http.MethodPost, "/api/v1/store/verify", "op", "pw-op"},
	} {
		w := mediaCall(t, mux, c.method, c.path, c.user, c.pass, "")
		var problem taxonomy.Problem
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Code != taxonomy.CodeConfigInvalid {
			t.Errorf("%s %s = %d %s, want the taxonomized configuration refusal", c.method, c.path, w.Code, w.Body)
		}
	}
}
