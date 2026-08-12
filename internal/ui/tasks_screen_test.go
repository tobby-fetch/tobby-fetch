// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// startQueue runs the worker for the test's lifetime.
func startQueue(t *testing.T, q *tasks.Queue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	q.Start(ctx)
}

// waitSettled polls until the task leaves the active states (FR-029: a
// task always settles or survives to resume).
func waitSettled(t *testing.T, q *tasks.Queue, id string) *tasks.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		task, ok := q.Get(id)
		if ok && !task.Active() {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s never settled", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTasksListFiltersAndPolling covers §5.7: search and filters on the
// canonical URL, the pending badge, and the auto-terminating load-polling
// — the hx-get attribute is re-emitted ONLY while a task is active.
func TestTasksListFiltersAndPolling(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// Queue not started: the created task stays pending (active).
	task, err := q.Create(tasks.TypeUnitImport, "docker.io/library/redis:7.2", "alexis",
		[]tasks.Item{{Name: "linux/amd64", Digest: "sha256:0123"}})
	if err != nil {
		t.Fatal(err)
	}

	w := get(t, mux, c, "/tasks", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/tasks = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		task.ID, "docker.io/library/redis:7.2",
		"t-badge--pending",
		`hx-trigger="every 3s"`, // active task: polling armed
		`hx-swap="morph:outerHTML"`,
		`id="task-rows"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/tasks misses %q", want)
		}
	}

	// Search and filters mirror the API parameters (?q=, ?status=, ?type=).
	w = get(t, mux, c, "/tasks?q=redis", nil)
	if !strings.Contains(w.Body.String(), task.ID) {
		t.Error("?q=redis misses the matching task")
	}
	w = get(t, mux, c, "/tasks?q=nothing-matches", nil)
	body = w.Body.String()
	if strings.Contains(body, task.ID) || !strings.Contains(body, `href="/tasks"`) {
		t.Error("no-result state misses the reset link or leaks a task")
	}
	w = get(t, mux, c, "/tasks?status=done", nil)
	if strings.Contains(w.Body.String(), task.ID) {
		t.Error("?status=done leaks a pending task")
	}
	w = get(t, mux, c, "/tasks?status=pending&type=unit-import", nil)
	if !strings.Contains(w.Body.String(), task.ID) {
		t.Error("?status=pending&type=unit-import misses the task")
	}

	// The filter form swaps only the results region on the canonical URL.
	w = get(t, mux, c, "/tasks?q=redis", map[string]string{
		"HX-Request": "true", "HX-Target": "tasks-results"})
	frag := w.Body.String()
	if strings.Contains(frag, "<!DOCTYPE html>") || strings.Contains(frag, "<form") {
		t.Error("filter fragment carries the document or the form")
	}
	if !strings.Contains(frag, `id="tasks-results"`) {
		t.Error("filter fragment misses the results region")
	}

	// The poll targets the row body and gets only the tbody fragment.
	w = get(t, mux, c, "/tasks", map[string]string{
		"HX-Request": "true", "HX-Target": "task-rows"})
	frag = w.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(frag), "<tbody") {
		t.Errorf("poll fragment is not the row body: %.80s", frag)
	}

	// The nav badge counts the active task and re-arms its own polling.
	w = get(t, mux, c, "/tasks/badge", map[string]string{"HX-Request": "true"})
	badge := w.Body.String()
	if !strings.Contains(badge, ">1</span>") || !strings.Contains(badge, `hx-trigger="every 30s"`) {
		t.Errorf("active badge = %q", badge)
	}
}

// TestTasksPollingStopsWhenSettled: once every task settled, the server
// stops re-emitting every polling attribute (list rows, nav badge, log
// sentinel) — the §8 stop decision is server-side.
func TestTasksPollingStopsWhenSettled(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	ref := seedImportUpstream(t, "linux/amd64")
	startQueue(t, q)

	form := map[string][]string{"ref": {ref}, "platform": {"linux/amd64"}}
	w := postForm(t, u, mux, c, form, map[string]string{"HX-Request": "true"})
	id := strings.TrimPrefix(w.Header().Get("HX-Redirect"), "/tasks/")
	task := waitSettled(t, q, id)
	if task.Status != tasks.StatusDone {
		t.Fatalf("import settled as %s: %+v", task.Status, task)
	}

	w = get(t, mux, c, "/tasks", nil)
	body := w.Body.String()
	if strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Error("settled list still re-emits the polling attribute")
	}
	if !strings.Contains(body, "t-badge--done") {
		t.Error("settled list misses the done badge")
	}

	w = get(t, mux, c, "/tasks/badge", map[string]string{"HX-Request": "true"})
	if badge := w.Body.String(); strings.Contains(badge, "hx-trigger") {
		t.Errorf("idle badge still polls: %q", badge)
	}

	w = get(t, mux, c, "/tasks/"+id, nil)
	body = w.Body.String()
	if strings.Contains(body, "logs=1") {
		t.Error("settled detail still re-emits the log sentinel")
	}
	// The finished task links back to the imported artifact (loop closed).
	if !strings.Contains(body, "/-/tags/7.2") {
		t.Error("finished detail misses the /content artifact link")
	}
	if !strings.Contains(body, "docker pull ") {
		t.Error("finished detail misses the pull command")
	}
	if !strings.Contains(body, "/api/v1/tasks/"+id+"/logs?format=raw") {
		t.Error("finished detail misses the raw log download link")
	}
}

// TestTaskDetailStates covers §5.8: the taxonomized 404, the failed task
// with its per-item error disclosure and principal code, the retry deep
// link, and the incremental log sentinel while active.
func TestTaskDetailStates(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// Unknown identifier: taxonomized 404 (TBY-TSK-001).
	w := get(t, mux, c, "/tasks/tsk_ghost", nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-TSK-001") {
		t.Errorf("unknown task = %d, want taxonomized 404", w.Code)
	}

	// Partial failure: one real platform, one absent digest.
	ref := seedImportUpstream(t, "linux/amd64")
	startQueue(t, q)
	rep := inspectRef(t, u, ref)
	created, err := q.Create(tasks.TypeUnitImport, ref, "alexis", []tasks.Item{
		{Name: "linux/amd64", Digest: rep["linux/amd64"]},
		{Name: "linux/arm64", Digest: "sha256:" + strings.Repeat("ab", 32)},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := waitSettled(t, q, created.ID)
	if task.Status != tasks.StatusFailed {
		t.Fatalf("task settled as %s, want failed", task.Status)
	}

	w = get(t, mux, c, "/tasks/"+task.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("detail = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		task.ID, task.RunID,
		`data-copy="` + task.RunID + `"`, // run chip is copiable
		"done with errors · 1/2",         // failed + Done>0 rendering
		"<details",                       // per-item progressive disclosure
		`role="alert"`,                   // full R-03 block inside
		"/import?ref=",                   // "Retry" deep link
		`href="/import"`,                 // "New import" always offered
		`role="log"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failed detail misses %q", want)
		}
	}

	// Active task: the log sentinel re-emits itself with a cursor.
	pending, err := q.Create(tasks.TypeUnitImport, "docker.io/library/hold:1", "alexis",
		[]tasks.Item{{Name: "linux/amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	w = get(t, mux, c, "/tasks/"+pending.ID+"?logs=1&after=0",
		map[string]string{"HX-Request": "true"})
	frag := w.Body.String()
	if !strings.Contains(frag, `id="log-cursor"`) {
		t.Error("log fragment misses the sentinel")
	}

	// The settled task's log fragment carries the chunk but no re-poll.
	w = get(t, mux, c, "/tasks/"+task.ID+"?logs=1&after=0",
		map[string]string{"HX-Request": "true"})
	frag = w.Body.String()
	if !strings.Contains(frag, `hx-swap-oob="beforeend:#log-pre"`) {
		t.Error("log fragment misses the out-of-band chunk")
	}
	if strings.Contains(frag, "hx-trigger") {
		t.Error("settled log fragment still re-emits the sentinel polling")
	}
}

// inspectRef maps platform name → digest against the same store the UI
// inspects with, so tests pin digests the way the screen does.
func inspectRef(t *testing.T, u *UI, ref string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, err := importer.Inspect(ctx, ref, u.store)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for i := range rep.Platforms {
		out[rep.Platforms[i].Name()] = rep.Platforms[i].Digest
	}
	return out
}

// TestTasksUIAPIParity is the R-06/FR-061 invariant on the task surface:
// the same ?q=&status= selects the same tasks on the screen URL and on
// /api/v1/tasks; aggregates come from the same computation.
func TestTasksUIAPIParity(t *testing.T) {
	u, q, st := newTestUIWithQueue(t)
	uiMux := mount(u)
	c := login(t, uiMux, "alexis", "pw-admin")

	if _, err := q.Create(tasks.TypeUnitImport, "docker.io/library/redis:7.2", "alexis",
		[]tasks.Item{{Name: "linux/amd64", Digest: "sha256:0123"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Create(tasks.TypeUnitImport, "quay.io/argoproj/argocd:v2.11.3", "alexis",
		[]tasks.Item{{Name: "linux/arm64", Digest: "sha256:4567"}}); err != nil {
		t.Fatal(err)
	}

	restAPI := api.New(u.authn, slog.New(slog.DiscardHandler))
	api.RegisterTasks(restAPI, q, st, time.Second, nil)
	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/", restAPI.Handler())

	const query = "?q=argocd&status=pending"
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks"+query, http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	wAPI := httptest.NewRecorder()
	apiMux.ServeHTTP(wAPI, r)
	if wAPI.Code != http.StatusOK {
		t.Fatalf("api = %d: %s", wAPI.Code, wAPI.Body.String())
	}
	var resp struct {
		Tasks []struct {
			ID         string `json:"id"`
			Reference  string `json:"reference"`
			ItemsTotal int    `json:"items_total"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(wAPI.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].Reference != "quay.io/argoproj/argocd:v2.11.3" ||
		resp.Tasks[0].ItemsTotal != 1 {
		t.Fatalf("api selection = %+v", resp.Tasks)
	}

	wUI := get(t, uiMux, c, "/tasks"+query, nil)
	body := wUI.Body.String()
	if !strings.Contains(body, resp.Tasks[0].ID) {
		t.Error("UI misses the API-selected task")
	}
	if strings.Contains(body, "docker.io/library/redis") {
		t.Error("UI shows a task absent from the API selection")
	}
}

// TestDashboardRecentTasks: the dashboard tile lists the latest real
// tasks with their badges once the queue is non-empty.
func TestDashboardRecentTasks(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// Empty queue: the gated empty state stays.
	w := get(t, mux, c, "/", nil)
	if !strings.Contains(w.Body.String(), "No task yet.") {
		t.Error("empty dashboard misses the empty state")
	}

	task, err := q.Create(tasks.TypeUnitImport, "docker.io/library/redis:7.2", "alexis",
		[]tasks.Item{{Name: "linux/amd64", Digest: "sha256:0123"}})
	if err != nil {
		t.Fatal(err)
	}
	w = get(t, mux, c, "/", nil)
	body := w.Body.String()
	if !strings.Contains(body, `href="/tasks/`+task.ID+`"`) {
		t.Error("dashboard misses the latest task link")
	}
	if !strings.Contains(body, "t-badge--pending") {
		t.Error("dashboard misses the task badge")
	}
	if strings.Contains(body, "No task yet.") {
		t.Error("dashboard keeps the empty state with a non-empty queue")
	}
}
