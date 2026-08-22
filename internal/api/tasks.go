// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Import and task endpoints (FR-060): the strict mirror of the /import
// and /tasks screens (FR-061, R-06). GET /api/v1/tasks accepts exactly the
// ?q=&status=&type=&page= parameters of the screen — both surfaces parse
// them with tasks.ParseListQuery; aggregates and the principal R-03 code
// are computed by the tasks and taxonomy packages — never re-derived here
// (ADR-0015 §4). Payloads carry raw bytes and RFC 3339 timestamps
// exclusively (ADR-0015 §7).

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// RegisterTasks mounts the import and task endpoints on the API surface.
// Triggering an import needs operator; reading tasks needs viewer
// (ADR-0009).
func RegisterTasks(a *API, q *tasks.Queue, st *store.Store, inspectTimeout time.Duration, importPolicy importer.Option) {
	t := &tasksAPI{api: a, queue: q, store: st, timeout: inspectTimeout, importPolicy: importPolicy}
	a.Handle("POST /api/v1/import", a.RequireRole(auth.RoleOperator, t.create))
	a.Handle("GET /api/v1/import/inspect", a.RequireRole(auth.RoleOperator, t.inspect))
	a.Handle("GET /api/v1/tasks", a.RequireRole(auth.RoleViewer, t.list))
	a.Handle("GET /api/v1/tasks/{id}", a.RequireRole(auth.RoleViewer, t.get))
	a.Handle("GET /api/v1/tasks/{id}/logs", a.RequireRole(auth.RoleViewer, t.logs))
}

type tasksAPI struct {
	api          *API
	queue        *tasks.Queue
	store        *store.Store
	timeout      time.Duration
	importPolicy importer.Option
}

// taskJSON is one task with its read-time aggregates: the persisted schema
// travels as-is (snake_case, RFC 3339), the counters and the principal
// code are computed fields (UI-SPEC §5.7).
type taskJSON struct {
	*tasks.Task
	ItemsTotal    int    `json:"items_total"`
	ItemsDone     int    `json:"items_done"`
	ItemsFailed   int    `json:"items_failed"`
	ItemsSkipped  int    `json:"items_skipped"`
	PrincipalCode string `json:"principal_code,omitempty"`
}

func newTaskJSON(t *tasks.Task) taskJSON {
	agg := t.Aggregate()
	out := taskJSON{
		Task:         t,
		ItemsTotal:   agg.Total,
		ItemsDone:    agg.Done,
		ItemsFailed:  agg.Failed,
		ItemsSkipped: agg.Skipped,
	}
	if t.Status == tasks.StatusFailed {
		if p := t.Principal(); p != nil {
			out.PrincipalCode = string(p.Code())
		}
	}
	return out
}

// createImportRequest is the POST /api/v1/import body: the reference and
// the selected platforms of a prior inspection (name + pinned digest —
// the transfer verifies every digest at commit, FR-026).
type createImportRequest struct {
	Reference string `json:"reference"`
	// VendorDependencies is the FR-025 per-operation opt-in: embed a
	// chart's missing declared dependencies before the artifact lands.
	// Default false — a chart unusable offline is refused (TBY-CHT-001).
	VendorDependencies bool `json:"vendorDependencies"`
	Platforms          []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"platforms"`
}

// create serves POST /api/v1/import: enqueue one unit-import task and
// answer 201 with it (mirror of the screen's confirmation step).
func (t *tasksAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.api.Problem(w, r, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": ""}).WithCause(err))
		return
	}
	req.Reference = strings.TrimSpace(req.Reference)
	if req.Reference == "" || len(req.Platforms) == 0 {
		t.api.Problem(w, r, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": req.Reference}).
			WithCause(errors.New("a reference and at least one platform are required")))
		return
	}
	items := make([]tasks.Item, 0, len(req.Platforms))
	for _, p := range req.Platforms {
		items = append(items, tasks.Item{Name: p.Name, Digest: p.Digest})
	}
	var copts []tasks.TaskOption
	if req.VendorDependencies {
		copts = append(copts, tasks.WithVendorDependencies())
	}
	id, _ := auth.IdentityFrom(r.Context())
	task, err := t.queue.Create(tasks.TypeUnitImport, req.Reference, id.Name, items, copts...)
	if err != nil {
		t.problem(w, r, err)
		return
	}
	audit.Log(r.Context(), t.api.logger, &audit.Event{
		Actor:   id.Name,
		Action:  audit.ActionImportCreate,
		Target:  req.Reference,
		Outcome: audit.OutcomeSuccess,
		Origin:  auth.ClientOrigin(r),
	})
	// Answer from a snapshot: the worker may already be mutating the
	// queue's own copy of the task.
	if snap, ok := t.queue.Get(task.ID); ok {
		task = snap
	}
	t.api.JSON(w, http.StatusCreated, map[string]any{"task": newTaskJSON(task)})
}

// inspectPlatform is one selectable entry of the inspection report.
type inspectPlatform struct {
	Name      string `json:"name"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Variant   string `json:"variant,omitempty"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
	Status    string `json:"status"`
}

// inspectResponse mirrors the screen's step 2 (FR-022, FR-026).
type inspectResponse struct {
	Reference   string            `json:"reference"`
	Repository  string            `json:"repository"`
	Tag         string            `json:"tag"`
	IndexDigest string            `json:"index_digest"`
	MediaType   string            `json:"media_type"`
	Kind        string            `json:"kind"`
	MultiArch   bool              `json:"multi_arch"`
	Platforms   []inspectPlatform `json:"platforms"`
}

// inspect serves GET /api/v1/import/inspect?ref=…: the bounded remote
// inspection (import.inspectTimeout), same parameter as the screen.
func (t *tasksAPI) inspect(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		t.api.Problem(w, r, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": ref}).
			WithCause(errors.New("the ref query parameter is required")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), t.timeout)
	defer cancel()
	rep, err := importer.Inspect(ctx, ref, t.store, t.importPolicy)
	if err != nil {
		t.problem(w, r, err)
		return
	}
	resp := inspectResponse{
		Reference:   rep.Reference,
		Repository:  rep.Repository,
		Tag:         rep.Tag,
		IndexDigest: rep.IndexDigest,
		MediaType:   rep.MediaType,
		Kind:        rep.Kind,
		MultiArch:   rep.MultiArch,
		Platforms:   make([]inspectPlatform, 0, len(rep.Platforms)),
	}
	for i := range rep.Platforms {
		p := &rep.Platforms[i]
		resp.Platforms = append(resp.Platforms, inspectPlatform{
			Name: p.Name(), OS: p.OS, Arch: p.Arch, Variant: p.Variant,
			Digest: p.Digest, SizeBytes: p.SizeBytes, Status: string(p.Status),
		})
	}
	t.api.JSON(w, http.StatusOK, resp)
}

// tasksListResponse mirrors the /tasks listing page: one page of tasks
// with the same pagination metadata as the /content mirror (FR-061).
type tasksListResponse struct {
	Tasks      []taskJSON `json:"tasks"`
	Page       int        `json:"page"`
	TotalPages int        `json:"totalPages"`
	Total      int        `json:"total"`
}

// list serves GET /api/v1/tasks?q=&status=&type=&page= (FR-061: the exact
// parameters of the /tasks screen), newest first, one page of
// tasks.ListPageSize entries — the /content pagination model.
func (t *tasksAPI) list(w http.ResponseWriter, r *http.Request) {
	page := t.queue.ListPage(tasks.ParseListQuery(r.URL.Query()))
	resp := tasksListResponse{
		Tasks:      make([]taskJSON, 0, len(page.Tasks)),
		Page:       page.Page,
		TotalPages: page.TotalPages,
		Total:      page.Total,
	}
	for _, task := range page.Tasks {
		resp.Tasks = append(resp.Tasks, newTaskJSON(task))
	}
	t.api.JSON(w, http.StatusOK, resp)
}

// get serves GET /api/v1/tasks/{id}; unknown identifiers answer the
// taxonomized 404 (TBY-TSK-001, same entry as the UI).
func (t *tasksAPI) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := t.queue.Get(id)
	if !ok {
		t.api.Problem(w, r, taxonomy.New(taxonomy.CodeTaskNotFound, taxonomy.Params{"id": id}))
		return
	}
	t.api.JSON(w, http.StatusOK, map[string]any{"task": newTaskJSON(task)})
}

// logs serves GET /api/v1/tasks/{id}/logs. With ?format=raw the whole log
// downloads as text/plain (Content-Disposition names task and run — the
// screen's download button); otherwise ?after=N answers the incremental
// {chunk, next} contract of the log follow (UI-SPEC §8).
func (t *tasksAPI) logs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := t.queue.Get(id)
	if !ok {
		t.api.Problem(w, r, taxonomy.New(taxonomy.CodeTaskNotFound, taxonomy.Params{"id": id}))
		return
	}
	if r.URL.Query().Get("format") == "raw" {
		// Stream from the file rather than buffering it (2026-08 audit):
		// a verbose sync log is easily tens of megabytes, and the whole
		// point of the raw download is the big ones.
		f, err := t.queue.OpenLog(id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreRead,
				taxonomy.Params{"detail": err.Error()}).WithCause(err))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="tobby-`+task.ID+`-`+task.RunID+`.log"`)
		if err != nil {
			// A task that never logged downloads as an empty file — the
			// same answer the buffered path gave.
			return
		}
		defer f.Close() //nolint:errcheck // read-only file
		_, _ = io.Copy(w, f)
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		after = 0
	}
	chunk, next, err := t.queue.ReadLog(id, after)
	if err != nil {
		t.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreRead,
			taxonomy.Params{"detail": err.Error()}).WithCause(err))
		return
	}
	t.api.JSON(w, http.StatusOK, map[string]any{"chunk": string(chunk), "next": next})
}

// problem maps an operation error onto the taxonomy: pass-through for
// taxonomy errors, TBY-SRV-001 otherwise.
func (t *tasksAPI) problem(w http.ResponseWriter, r *http.Request, err error) {
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	t.api.Problem(w, r, te)
}
