// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The plan endpoint (FR-055 amendment R-04, FR-060): the API half of the
// side-effect-free operation, in strict parity with the /recipes/plan
// screen (FR-061). It answers the complete report — version resolution
// (FR-021), per-digest statuses (FR-026), volumes (FR-055), projected
// prune (FR-045) and the policy verdicts reachable without a transfer
// (FR-030, FR-033) — and it writes nothing.

package api

import (
	"encoding/json"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxPlanBody bounds a plan request. The only large member is a candidate
// Retriever document, which the format bounds at a few kilobytes; the
// budget below is generous and still far from a lever.
const maxPlanBody = 256 << 10

// PlanOptions is what the plan endpoint reads from the instance.
type PlanOptions struct {
	// Planner produces the report. Nil on an instance wired without a
	// recipe engine, and the endpoint then says so rather than answering
	// an empty plan.
	Planner *engine.Planner
}

// RegisterPlan mounts POST /api/v1/plan.
//
// Operator, not viewer. A plan mutates nothing, which argues for the
// viewer floor the read endpoints use — but it makes the instance open
// outbound connections to every registry a Retriever names, on a body the
// caller supplies. That is an action with a footprint on somebody else's
// infrastructure, and the role that may cause it is the role that may
// trigger a synchronization (FR-014, ADR-0009).
func RegisterPlan(a *API, o *PlanOptions) {
	p := &planAPI{api: a, opts: *o}
	a.Handle("POST /api/v1/plan", a.RequireRole(auth.RoleOperator, p.plan))
}

type planAPI struct {
	api  *API
	opts PlanOptions
}

// planRequest is the endpoint's body. Every member is optional: an empty
// body plans the configured Retriever, which is the common case.
type planRequest struct {
	// Retriever names a candidate source — a file path readable by the
	// instance, an HTTP(S) URL, or an OCI reference (FR-010 forms).
	Retriever string `json:"retriever,omitempty"`
	// Document carries a candidate Retriever inline, as the YAML text. It
	// wins over Retriever, and it is how a caller plans a document that
	// exists nowhere yet — the CI gate case: plan the file in the merge
	// request, before anything is deployed.
	Document string `json:"document,omitempty"`
	// SkipDestination keeps the plan strictly local: no request is made
	// to the promotion destination.
	SkipDestination bool `json:"skip_destination,omitempty"`
}

// planResponse wraps the report with the exit code its outcome maps to,
// so a script driving the API branches on the same value the CLI would
// have exited with (FR-066).
type planResponse struct {
	Plan     *engine.Plan `json:"plan"`
	ExitCode int          `json:"exit_code"`
}

func (p *planAPI) plan(w http.ResponseWriter, r *http.Request) {
	if p.opts.Planner == nil {
		p.api.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance runs no recipe engine: there is nothing to plan",
		}))
		return
	}
	var body planRequest
	if r.Body != nil && r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlanBody))
		if err := dec.Decode(&body); err != nil {
			p.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
				"file": "the request body", "path": "(root)",
				"constraint": "the body must be a JSON object with the optional members \"retriever\", \"document\" and \"skip_destination\": " + err.Error(),
			}))
			return
		}
	}

	plan, err := p.opts.Planner.Plan(r.Context(), engine.PlanOptions{
		Retriever:         body.Retriever,
		RetrieverDocument: []byte(body.Document),
		SkipDestination:   body.SkipDestination,
	})
	if err != nil {
		p.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	// 200 whatever the outcome. A plan that reports a policy refusal
	// SUCCEEDED — it answered the question it was asked — and answering
	// 403 would make the report unreadable to every client that treats a
	// 4xx as an error body. The verdict lives in the document, where a
	// caller can act on it.
	p.api.JSON(w, http.StatusOK, planResponse{Plan: plan, ExitCode: plan.ExitCode()})
}
