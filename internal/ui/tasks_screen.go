// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Task screens (roadmap 2.4, UI-SPEC §5.7/§5.8): the /tasks list with its
// auto-terminating load-polling, and the /tasks/{id} detail with per-item
// status, the extensible reports zone (FR-024), and the cursor-based log
// follow (FR-090). URLs carry exactly the parameters of the /api/v1/tasks
// mirror (FR-061, ADR-0015 §1).

package ui

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// taskRow decorates one task for the list, the dashboard tile, and the
// detail header: computed aggregates, badge state, principal code (R-03),
// and elapsed duration — the persisted schema stays untouched (UI-SPEC
// §5.7: "done with errors" is a rendering of failed + aggregates).
type taskRow struct {
	*tasks.Task
	Agg tasks.Aggregates
	// State selects the badge: pending, running, done, done-errors, failed.
	State string
	// Succeeded counts the items settled without failure (done + skipped).
	Succeeded int
	// Code is the principal taxonomy code of a failed task, visible from
	// the list (single aggregation rule: taxonomy.Principal).
	Code string
	// Duration is the elapsed run time; running tasks measure against now.
	Duration    time.Duration
	HasDuration bool
}

// newTaskRow computes the row of one task.
func newTaskRow(t *tasks.Task, now time.Time) taskRow {
	row := taskRow{Task: t, Agg: t.Aggregate()}
	row.Succeeded = row.Agg.Done + row.Agg.Skipped
	switch t.Status {
	case tasks.StatusPending:
		row.State = "pending"
	case tasks.StatusRunning:
		row.State = "running"
	case tasks.StatusDone, tasks.StatusSkipped:
		row.State = "done"
	case tasks.StatusFailed:
		row.State = "failed"
		if row.Agg.Done > 0 {
			row.State = "done-errors"
		}
		if p := t.Principal(); p != nil {
			row.Code = string(p.Code())
		}
	}
	if !t.Started.IsZero() {
		end := t.Finished
		if end.IsZero() {
			end = now
		}
		row.Duration = end.Sub(t.Started)
		row.HasDuration = true
	}
	return row
}

// tasksData feeds the /tasks page.
type tasksData struct {
	Rows []taskRow
	// Q, Status, Type mirror the /api/v1/tasks parameters (FR-061).
	Q, Status, Type string
	HasFilter       bool
	// Total, Page, TotalPages and the two hrefs are the /content
	// pagination model applied to the task history (FR-061): the same
	// page parameter, the same 25-entry pages, the same nav.
	Total, Page, TotalPages int
	PrevHref, NextHref      string
	// AnyActive re-arms the polling attributes: the server re-emits them
	// only while a task listed ON THIS PAGE still moves (auto-terminating
	// load-polling, UI-SPEC §8) — a page of settled history has nothing
	// to poll for.
	AnyActive bool
	// PollURL is the canonical listing URL the row fragment polls — page
	// included, so the poll refreshes the page being looked at.
	PollURL string
}

// tasksHref rebuilds the canonical listing URL for a page — the same
// parameter set the API mirror accepts (FR-061), rendered by the shared
// tasks.ListQuery like /content renders its BrowseQuery.
func tasksHref(lq tasks.ListQuery, page int) string {
	lq.Page = page
	if enc := lq.Values().Encode(); enc != "" {
		return "/tasks?" + enc
	}
	return "/tasks"
}

// tasksList serves GET /tasks: search and filters server-side, newest
// first, paginated like /content. The polled row fragment and the filter
// form swap on the same canonical URL (ADR-0015 §1), told apart by the
// HX-Target header.
func (u *UI) tasksList(w http.ResponseWriter, r *http.Request) {
	lq := tasks.ParseListQuery(r.URL.Query())
	data := &tasksData{
		Q: lq.Q, Status: string(lq.Status), Type: lq.Type,
		HasFilter: lq.HasFilter(), Page: lq.Page, TotalPages: 1,
	}
	if u.queue != nil {
		page := u.queue.ListPage(lq)
		data.Total, data.Page, data.TotalPages = page.Total, page.Page, page.TotalPages
		for _, t := range page.Tasks {
			if t.Active() {
				data.AnyActive = true
			}
			data.Rows = append(data.Rows, newTaskRow(t, u.now()))
		}
		if page.Page > 1 {
			data.PrevHref = tasksHref(lq, page.Page-1)
		}
		if page.Page < page.TotalPages {
			data.NextHref = tasksHref(lq, page.Page+1)
		}
	}
	data.PollURL = tasksHref(lq, lq.Page)

	if isFragment(r) {
		if r.Header.Get("HX-Target") == "task-rows" {
			u.render.Fragment(w, r, "tasks", "task-rows", data)
			return
		}
		u.render.Fragment(w, r, "tasks", "tasks-results", data)
		return
	}
	u.render.Page(w, r, "tasks", data)
}

// taskItemView is one item row of the detail, its persisted failure
// re-rendered in the viewer's language (ADR-0015 §4).
type taskItemView struct {
	tasks.Item
	// State is the item status as a plain string for template comparisons.
	State string
	Err   *ErrView
	// LargeBlobs renders the per-blob progress of the item's resumable
	// transfers (FR-029, R-29). Only blobs above the configured threshold
	// are tracked, so this is empty on ordinary items and carries a
	// handful of rows on the multi-gigabyte ones — which is exactly when
	// "running" stops being an answer an operator can act on.
	LargeBlobs []taskBlobView
}

// taskBlobView is one large blob's progress row.
type taskBlobView struct {
	Digest   string
	Received int64
	Size     int64
	// Percent is computed server side rather than in the template: the
	// zero-size case has to be decided somewhere, and a template is the
	// wrong place to decide it.
	Percent int
	Resumed bool
	Done    bool
}

// blobViews renders an item's tracked blobs.
func blobViews(it *tasks.Item) []taskBlobView {
	if len(it.Blobs) == 0 {
		return nil
	}
	out := make([]taskBlobView, 0, len(it.Blobs))
	for _, b := range it.Blobs {
		v := taskBlobView{
			Digest: b.Digest, Received: b.ReceivedBytes, Size: b.SizeBytes,
			Resumed: b.Resumed, Done: b.Done,
		}
		switch {
		case b.Done:
			v.Percent = 100
		case b.SizeBytes > 0:
			v.Percent = int(b.ReceivedBytes * 100 / b.SizeBytes)
		}
		out = append(out, v)
	}
	return out
}

// taskDetailData feeds the /tasks/{id} page.
type taskDetailData struct {
	Row   taskRow
	Items []taskItemView
	// TaskErr is the task-level failure (the operation could not even
	// decompose into items).
	TaskErr *ErrView
	// LogChunk and LogNext seed the cursor-based log follow (UI-SPEC §8).
	LogChunk string
	LogNext  int64
	LogErr   bool
	// ArtifactHref and PullCommand close the milestone-2 loop on a
	// finished task (UI-SPEC §5.8).
	ArtifactHref string
	PullCommand  string
	// RelaunchHref points a failed task at where it is retried: /import
	// pre-filled for a unit import (incremental FR-026), the recipes
	// screen and its Synchronize action for a synchronization (FR-014 —
	// a sync is triggered, never replayed from a URL).
	RelaunchHref string
	DownloadHref string
}

// taskDetail serves GET /tasks/{id} and, with ?logs=1, the incremental
// log-follow fragment. Unknown identifiers answer the taxonomized 404
// (TBY-TSK-001).
func (u *UI) taskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var t *tasks.Task
	ok := false
	if u.queue != nil {
		t, ok = u.queue.Get(id)
	}
	if !ok {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeTaskNotFound, taxonomy.Params{"id": id}))
		return
	}
	if r.URL.Query().Get("logs") == "1" {
		u.taskLogs(w, r, t)
		return
	}

	lang := requestLang(r)
	data := &taskDetailData{
		Row:          newTaskRow(t, u.now()),
		DownloadHref: "/api/v1/tasks/" + t.ID + "/logs?format=raw",
	}
	if t.Error != nil {
		data.TaskErr = errView(lang, t.Error.Taxonomy(t.RunID))
	}
	for _, it := range t.Items {
		iv := taskItemView{Item: it, State: string(it.Status), LargeBlobs: blobViews(&it)}
		if it.Error != nil {
			iv.Err = errView(lang, it.Error.Taxonomy(t.RunID))
		}
		data.Items = append(data.Items, iv)
	}
	if t.Status == tasks.StatusFailed {
		if t.Type == tasks.TypeSync {
			data.RelaunchHref = "/recipes"
		} else {
			data.RelaunchHref = "/import?ref=" + url.QueryEscape(t.Reference)
		}
	}
	if t.Status == tasks.StatusDone && t.Type != tasks.TypeSync {
		data.ArtifactHref, data.PullCommand = u.taskArtifact(r, t)
	}

	// The polled body zone swaps on the same canonical URL (ADR-0015 §1),
	// told apart by the HX-Target header — like the /tasks rows (B-002).
	if isFragment(r) && r.Header.Get("HX-Target") == "task-body" {
		u.render.Fragment(w, r, "task-detail", "task-body", data)
		return
	}

	chunk, next, err := u.queue.ReadLog(t.ID, 0)
	if err != nil {
		data.LogErr = true
	} else {
		data.LogChunk = string(chunk)
		data.LogNext = next
	}
	u.render.Page(w, r, "task-detail", data)
}

// taskArtifact recomputes the relocated destination of a finished import —
// via relocate.Path, never a fresh remote inspection — and the pull
// command contextualized on the request host. Any parse failure simply
// omits the link (UI-SPEC §5.8).
func (u *UI) taskArtifact(r *http.Request, t *tasks.Task) (href, pull string) {
	ref, err := name.ParseReference(t.Reference, name.WithDefaultRegistry("docker.io"))
	if err != nil {
		return "", ""
	}
	repo, err := relocate.Path(ref.Context().Name())
	if err != nil {
		return "", ""
	}
	tag := "latest"
	if tg, isTag := ref.(name.Tag); isTag {
		tag = tg.TagStr()
	}
	kind := store.KindContainerImage
	if info, err := u.store.RepoInfo(r.Context(), repo); err == nil {
		kind = info.Kind
	}
	return "/content/" + repo + "/-/tags/" + tag, pullByTag(kind, r.Host, repo, tag)
}

// taskLogsData feeds the log-follow fragment: the new chunk appended
// out-of-band plus the sentinel re-emitted with the new cursor — only
// while the task is active (auto-terminating, UI-SPEC §8; the same
// contract a future SSE transport reuses).
type taskLogsData struct {
	ID     string
	Chunk  string
	Next   int64
	Active bool
}

// taskLogs serves the ?logs=1&after=N fragment of the detail screen.
func (u *UI) taskLogs(w http.ResponseWriter, r *http.Request, t *tasks.Task) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		after = 0
	}
	chunk, next, err := u.queue.ReadLog(t.ID, after)
	if err != nil {
		chunk, next = nil, after
	}
	u.render.Fragment(w, r, "task-detail", "task-log-follow",
		taskLogsData{ID: t.ID, Chunk: string(chunk), Next: next, Active: t.Active()})
}
