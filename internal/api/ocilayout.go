// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// OCI image layout and store-reset endpoints (FR-060): the strict mirror
// of the /oci-layout and /admin/store screens (FR-061). Both surfaces go
// through interop.Service, so the confirmation rule of FR-046 and the
// selection rules of FR-051 exist once and cannot drift between them.
//
// Role floor: admin on all four. Export and import name a path on the
// HOST filesystem — an export writes the store's content wherever the
// caller says, and that is an administrative capability, not an operator
// one. The reset is admin by the letter of FR-046.

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxLayoutRequest bounds the request body: a handful of paths and
// selection names, never a payload.
const maxLayoutRequest = 64 << 10

// RegisterOCILayout mounts the FR-051 and FR-046 endpoints.
func RegisterOCILayout(a *API, svc *interop.Service, queue *tasks.Queue) {
	l := &layoutAPI{api: a, svc: svc, queue: queue}
	a.Handle("POST /api/v1/oci-layout/plan", a.RequireRole(auth.RoleAdmin, l.plan))
	a.Handle("POST /api/v1/oci-layout/export", a.RequireRole(auth.RoleAdmin, l.export))
	a.Handle("POST /api/v1/oci-layout/import", a.RequireRole(auth.RoleAdmin, l.importLayout))
	a.Handle("POST /api/v1/store/reset", a.RequireRole(auth.RoleAdmin, l.reset))
}

type layoutAPI struct {
	api   *API
	svc   *interop.Service
	queue *tasks.Queue
}

// exportRequestBody is the submitted export. Both selection fields empty
// exports the whole store — the interoperability escape hatch in its
// most useful form.
type exportRequestBody struct {
	Output       string   `json:"output"`
	Format       string   `json:"format,omitempty"`
	Recipes      []string `json:"recipes,omitempty"`
	Repositories []string `json:"repositories,omitempty"`
	Overwrite    bool     `json:"overwrite,omitempty"`
}

// importRequestBody is the submitted import.
type importRequestBody struct {
	Input      string `json:"input"`
	Repository string `json:"repository,omitempty"`
}

// resetRequestBody carries the typed confirmation of FR-046.
type resetRequestBody struct {
	Confirmation string `json:"confirmation"`
}

// planResponse is the side-effect-free projection: what the export would
// contain, how many bytes it would write, and the size of the largest
// single file — the numbers a pre-flight compares with free space and
// with a target filesystem's per-file limit (FR-055).
type planResponse struct {
	Format           string        `json:"format"`
	References       []string      `json:"references"`
	Manifests        int           `json:"manifests"`
	Blobs            int           `json:"blobs"`
	Files            int           `json:"files"`
	ContentBytes     int64         `json:"contentBytes"`
	TotalBytes       int64         `json:"totalBytes"`
	LargestFileBytes int64         `json:"largestFileBytes"`
	Missing          []missingJSON `json:"missing,omitempty"`
}

// missingJSON is one piece of content the selection named and the store
// does not hold. The reason is a stable untranslated label; surfaces
// localize around it (ADR-0015 §7).
type missingJSON struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Reason     string `json:"reason"`
}

// resetResponse says what the reset discarded, so a caller can report it
// rather than assume.
type resetResponse struct {
	Repositories int   `json:"repositories"`
	Bytes        int64 `json:"bytes"`
}

// plan serves POST /api/v1/oci-layout/plan.
func (l *layoutAPI) plan(w http.ResponseWriter, r *http.Request) {
	var body exportRequestBody
	if !l.decode(w, r, &body) {
		return
	}
	plan, projection, err := l.svc.Plan(r.Context(), &interop.ExportRequest{
		Selector: interop.Selector{Recipes: body.Recipes, Repositories: body.Repositories},
		Output:   body.Output,
		Format:   ocilayout.Format(body.Format),
	})
	if err != nil {
		l.problem(w, r, err)
		return
	}
	resp := planResponse{
		Format:           string(projection.Format),
		Manifests:        projection.Manifests,
		Blobs:            projection.Blobs,
		Files:            projection.Files,
		ContentBytes:     projection.ContentBytes,
		TotalBytes:       projection.TotalBytes,
		LargestFileBytes: projection.LargestFileBytes,
		Missing:          missingList(plan.Missing),
	}
	resp.References = make([]string, 0, len(plan.Refs))
	for _, ref := range plan.Refs {
		resp.References = append(resp.References, ref.String())
	}
	l.api.JSON(w, http.StatusOK, resp)
}

// export serves POST /api/v1/oci-layout/export: enqueue the export and
// answer 201 with the task, like every other long operation of the
// product.
func (l *layoutAPI) export(w http.ResponseWriter, r *http.Request) {
	var body exportRequestBody
	if !l.decode(w, r, &body) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	task, err := l.svc.StartExport(id.Name, &interop.ExportRequest{
		Selector:  interop.Selector{Recipes: body.Recipes, Repositories: body.Repositories},
		Output:    strings.TrimSpace(body.Output),
		Format:    ocilayout.Format(body.Format),
		Overwrite: body.Overwrite,
	})
	l.audit(r, audit.ActionLayoutExport, id.Name, body.Output, err)
	if err != nil {
		l.problem(w, r, err)
		return
	}
	l.created(w, r, task)
}

// importLayout serves POST /api/v1/oci-layout/import.
func (l *layoutAPI) importLayout(w http.ResponseWriter, r *http.Request) {
	var body importRequestBody
	if !l.decode(w, r, &body) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	task, err := l.svc.StartImport(id.Name, &interop.ImportRequest{
		Input:      strings.TrimSpace(body.Input),
		Repository: strings.TrimSpace(body.Repository),
	})
	l.audit(r, audit.ActionLayoutImport, id.Name, body.Input, err)
	if err != nil {
		l.problem(w, r, err)
		return
	}
	l.created(w, r, task)
}

// reset serves POST /api/v1/store/reset (FR-046). Synchronous: the
// instance must be usable on an empty store when this returns, and a
// caller told "queued" would not know when that is.
func (l *layoutAPI) reset(w http.ResponseWriter, r *http.Request) {
	var body resetRequestBody
	if !l.decode(w, r, &body) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	// The audit record is written by the service, which is also where the
	// confirmation is checked: the FR-094 entry must exist for a REFUSED
	// reset too, and only the code that refuses knows it refused.
	res, err := l.svc.Reset(r.Context(), id.Name, auth.ClientOrigin(r), body.Confirmation)
	if err != nil {
		l.problem(w, r, err)
		return
	}
	l.api.JSON(w, http.StatusOK, resetResponse{Repositories: res.Repositories, Bytes: res.Bytes})
}

// created answers with the freshly enqueued task, read back from the
// queue: the worker may already be mutating its own copy.
func (l *layoutAPI) created(w http.ResponseWriter, _ *http.Request, task *tasks.Task) {
	if snap, ok := l.queue.Get(task.ID); ok {
		task = snap
	}
	l.api.JSON(w, http.StatusCreated, map[string]any{"task": newTaskJSON(task)})
}

// decode reads a bounded JSON body, answering the validation problem on
// malformed input.
func (l *layoutAPI) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLayoutRequest)).Decode(out); err != nil {
		l.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the request body", "path": "-", "constraint": err.Error(),
		}))
		return false
	}
	return true
}

// problem renders a service failure; everything it returns is already
// taxonomized.
func (l *layoutAPI) problem(w http.ResponseWriter, r *http.Request, err error) {
	l.api.Problem(w, r, asTaxonomy(err))
}

// audit emits the FR-094 record for the layout operations. A refused
// request is recorded too: who tried to write the store's content onto
// which path is exactly what the trail is for.
func (l *layoutAPI) audit(r *http.Request, action, actor, target string, err error) {
	outcome := audit.OutcomeSuccess
	if err != nil {
		outcome = audit.OutcomeFailure
		if te := asTaxonomy(err); te.Entry().Class == taxonomy.ClassPolicy {
			outcome = audit.OutcomeDenied
		}
	}
	audit.Log(r.Context(), l.api.logger, &audit.Event{
		Actor: actor, Action: action, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// asTaxonomy unwraps a service failure into its catalog entry. The
// service taxonomizes everything it returns; the fallback exists so a
// future path that forgets to still answers a problem document rather
// than a bare 500.
func asTaxonomy(err error) *taxonomy.Error {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te
	}
	return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
}

// missingList renders the absences for the API payload.
func missingList(missing []ocilayout.Missing) []missingJSON {
	if len(missing) == 0 {
		return nil
	}
	out := make([]missingJSON, 0, len(missing))
	for _, m := range missing {
		out = append(out, missingJSON{
			Repository: m.Ref.Repo, Tag: m.Ref.Tag, Digest: m.Digest, Reason: m.Reason,
		})
	}
	return out
}
