// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// POST /api/v1/plan (FR-055 amendment R-04, FR-060), the API half of the
// side-effect-free operation and the strict mirror of /recipes/plan
// (FR-061).

// newPlanAPI mounts the plan endpoint over a real store and a real
// planner. There is no registry behind the Retriever: the plan then
// reports a failure, which is exactly the shape a caller must be able to
// read — the endpoint answers 200 with a report that says what went
// wrong, never a bare error.
func newPlanAPI(t *testing.T, source string) *http.ServeMux {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing store: %v", cerr)
		}
	})
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, a := range []struct {
		name string
		role auth.Role
		pw   string
	}{
		{"lecteur", auth.RoleViewer, "pw-view"},
		{"op", auth.RoleOperator, "pw-op"},
	} {
		if err := accounts.AddAccount(a.name, a.role, a.pw, now); err != nil {
			t.Fatal(err)
		}
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	remotes, err := engine.NewRemotes(config.Registries{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := engine.LoadTrust(config.Trust{}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	planner := engine.NewPlanner(st, remotes, trust, source, engine.PlanConfig{Mode: "mirror"})

	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterPlan(a, &api.PlanOptions{Planner: planner})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux
}

// TestPlanEndpointAnswersTheReport: 200 with the report and the exit code
// the CLI would have used, whatever the outcome. A plan that reports a
// refusal SUCCEEDED — it answered the question — and a 4xx would make the
// document unreadable to every client that treats one as an error body.
func TestPlanEndpointAnswersTheReport(t *testing.T) {
	// A Retriever file the instance can read, naming a cookbook nothing
	// serves: the plan resolves nothing and reports why.
	dir := t.TempDir()
	path := filepath.Join(dir, "retriever.yaml")
	doc := "apiVersion: recipe.tobby.dev/v1alpha1\nkind: Retriever\nmetadata:\n  name: zone-a\nspec:\n  cookbook: 127.0.0.1:1/cookbook\n  recipes:\n    - name: demo\n      version: 1.0.0\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := newPlanAPI(t, path)

	w := call(t, mux, http.MethodPost, "/api/v1/plan", "op", "pw-op", "")
	if w.Code != http.StatusOK {
		t.Fatalf("plan = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Plan     *engine.Plan `json:"plan"`
		ExitCode int          `json:"exit_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("the response is not the documented document: %v\n%s", err, w.Body.String())
	}
	if resp.Plan == nil {
		t.Fatal("the response carries no plan")
	}
	if resp.Plan.Mode != "mirror" {
		t.Errorf("plan mode = %q, want the instance's own", resp.Plan.Mode)
	}
	if resp.Plan.Zone != "zone-a" {
		t.Errorf("zone = %q, want the Retriever's metadata.name", resp.Plan.Zone)
	}
	if resp.ExitCode != resp.Plan.ExitCode() {
		t.Errorf("exit_code = %d, want the plan's own %d", resp.ExitCode, resp.Plan.ExitCode())
	}
	// The unreachable cookbook is reported inside the document, with its
	// stable code — never as an HTTP error.
	if len(resp.Plan.Problems) == 0 {
		t.Fatal("an unreachable cookbook produced no problem in the report")
	}
	if resp.Plan.Problems[0].Code == "" || resp.Plan.Problems[0].Class == "" {
		t.Errorf("the problem carries no code or class: %+v", resp.Plan.Problems[0])
	}
	// FR-055: the pre-flight check ran even though nothing resolved — the
	// store's own capacity is knowable regardless.
	if len(resp.Plan.Checks) != 1 {
		t.Errorf("checks = %+v, want the store verdict", resp.Plan.Checks)
	}
}

// TestPlanEndpointAcceptsACandidateDocument covers the CI-gate entry
// point: a Retriever that exists nowhere yet, submitted inline.
func TestPlanEndpointAcceptsACandidateDocument(t *testing.T) {
	mux := newPlanAPI(t, "")
	body := `{"document":"apiVersion: recipe.tobby.dev/v1alpha1\nkind: Retriever\nmetadata:\n  name: candidate-zone\nspec:\n  cookbook: 127.0.0.1:1/cookbook\n  recipes:\n    - name: demo\n      version: 1.0.0\n"}`

	w := call(t, mux, http.MethodPost, "/api/v1/plan", "op", "pw-op", body)
	if w.Code != http.StatusOK {
		t.Fatalf("plan = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Plan *engine.Plan `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Plan.Zone != "candidate-zone" {
		t.Errorf("zone = %q, want the submitted document's", resp.Plan.Zone)
	}
	if !resp.Plan.SourceIsCandidate {
		t.Error("a submitted document is not reported as a candidate run")
	}
	// The cookbook is unreachable, so the run reports a failure — inside
	// the document, with a stable code, and still with the FR-055 store
	// verdict attached. What is under test is that the SUBMITTED document
	// is what got planned, never the configured source (which is empty
	// here, so a fallback would have failed differently).
	if resp.Plan.Outcome != engine.OutcomeFailed {
		t.Errorf("outcome = %q, want %q against an unreachable cookbook", resp.Plan.Outcome, engine.OutcomeFailed)
	}
	if len(resp.Plan.Recipes) != 1 || resp.Plan.Recipes[0].Name != "demo" {
		t.Errorf("the plan did not resolve the submitted document: %+v", resp.Plan.Recipes)
	}
}

// TestPlanEndpointRefusesAMalformedBody: a rejected request body is a
// verdict on what the client sent, answered with the validation entry —
// distinct from a report that says the plan failed.
func TestPlanEndpointRefusesAMalformedBody(t *testing.T) {
	mux := newPlanAPI(t, "")
	w := call(t, mux, http.MethodPost, "/api/v1/plan", "op", "pw-op", "not json")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body = %d, want 422: %s", w.Code, w.Body.String())
	}
	var problem taxonomy.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != taxonomy.CodeValidation {
		t.Errorf("code = %s, want %s", problem.Code, taxonomy.CodeValidation)
	}
}

// TestPlanEndpointWithoutAPlanner says so rather than answering an empty
// report: an instance with no recipe engine has nothing to plan.
func TestPlanEndpointWithoutAPlanner(t *testing.T) {
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("op", auth.RoleOperator, "pw-op", time.Now()); err != nil {
		t.Fatal(err)
	}
	a := api.New(&auth.Authenticator{
		Store: accounts, Sessions: auth.NewSessions(time.Hour), Logger: slog.New(slog.DiscardHandler),
	}, slog.New(slog.DiscardHandler))
	api.RegisterPlan(a, &api.PlanOptions{})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())

	w := call(t, mux, http.MethodPost, "/api/v1/plan", "op", "pw-op", "")
	if w.Code == http.StatusOK {
		t.Fatalf("an instance without a planner answered a plan: %s", w.Body.String())
	}
	var problem taxonomy.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != taxonomy.CodeConfigInvalid {
		t.Errorf("code = %s, want %s", problem.Code, taxonomy.CodeConfigInvalid)
	}
}
