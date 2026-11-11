// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// On-demand store integrity (FR-057, R-31): the API mirror of the Store
// integrity screen (FR-061). Starting a verification enqueues a tracked
// task, exactly as every other long operation does; the report it leaves
// is served from the task that produced it, so the screen, the command
// line and a caller's own tooling all read one document.

package api

import (
	"errors"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// IntegrityVerifier is the verification service as this surface needs it.
// *check.Service implements it.
type IntegrityVerifier interface {
	// Start enqueues one verification, or refuses with TBY-INT-004 naming
	// the one already in flight.
	Start(actor, origin string) (*tasks.Task, error)
	// Latest is the most recent verification task, finished or not.
	Latest() (*tasks.Task, bool)
	// Cancel stops the verification in flight and reports whether there
	// was one.
	Cancel() bool
}

// IntegrityOptions is what RegisterIntegrity needs wired.
type IntegrityOptions struct {
	// Verifier is the service. Nil on an instance with no task queue.
	Verifier IntegrityVerifier
}

// RegisterIntegrity mounts the store-integrity endpoints.
//
// Reading the last report is a viewer's business — it names files, not
// secrets, and it is what the screen polls. Starting a verification
// re-hashes the whole store: disk I/O for minutes, and the serial task
// worker held for as long, so it is an operator's action (ADR-0009), the
// floor of the Media screen's verification for the same reason.
func RegisterIntegrity(a *API, opts *IntegrityOptions) {
	i := &integrityAPI{api: a, opts: opts}
	a.Handle("GET /api/v1/store/verification", a.RequireRole(auth.RoleViewer, i.latest))
	a.Handle("POST /api/v1/store/verify", a.RequireRole(auth.RoleOperator, i.start))
	a.Handle("POST /api/v1/store/verify/cancel", a.RequireRole(auth.RoleOperator, i.cancel))
}

type integrityAPI struct {
	api  *API
	opts *IntegrityOptions
}

// integrityStatus is the GET /api/v1/store/verification document: the last
// verification and where it stands. Verification is null before any has
// been requested.
type integrityStatus struct {
	TaskID string       `json:"task_id,omitempty"`
	Status tasks.Status `json:"status,omitempty"`
	// Verification is the timestamped report; while the verification runs
	// it is a snapshot carrying only its progress.
	Verification *integrity.Report `json:"verification"`
}

// latest serves GET /api/v1/store/verification.
func (i *integrityAPI) latest(w http.ResponseWriter, r *http.Request) {
	if i.opts == nil || i.opts.Verifier == nil {
		i.api.Problem(w, r, unavailable())
		return
	}
	t, ok := i.opts.Verifier.Latest()
	if !ok {
		i.api.JSON(w, http.StatusOK, integrityStatus{})
		return
	}
	i.api.JSON(w, http.StatusOK, integrityStatus{TaskID: t.ID, Status: t.Status, Verification: t.Verification})
}

// start serves POST /api/v1/store/verify: enqueue one verification and
// answer 201 with its task. A second request while one is in flight is a
// 409 naming it (TBY-INT-004).
func (i *integrityAPI) start(w http.ResponseWriter, r *http.Request) {
	if i.opts == nil || i.opts.Verifier == nil {
		i.api.Problem(w, r, unavailable())
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	t, err := i.opts.Verifier.Start(id.Name, auth.ClientOrigin(r))
	if err != nil {
		var te *taxonomy.Error
		if errors.As(err, &te) {
			i.api.Problem(w, r, te)
			return
		}
		i.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	i.api.JSON(w, http.StatusCreated, map[string]any{"task": newTaskJSON(t)})
}

// cancel serves POST /api/v1/store/verify/cancel: stop the verification in
// flight. 200 with {"cancelled": false} when none is running — cancelling
// nothing is not an error. The task ends failed with TBY-INT-006.
func (i *integrityAPI) cancel(w http.ResponseWriter, r *http.Request) {
	if i.opts == nil || i.opts.Verifier == nil {
		i.api.Problem(w, r, unavailable())
		return
	}
	i.api.JSON(w, http.StatusOK, map[string]bool{"cancelled": i.opts.Verifier.Cancel()})
}

// unavailable is the answer of an instance with no task queue to run a
// verification on.
func unavailable() *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
		"detail": "this instance has no task queue to run a store verification on",
	})
}
