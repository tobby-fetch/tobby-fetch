// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Task lifecycle tests (2026-08 audit): finished-task retention, the
// scheduler's coalescence guard, the no-orphan Create on a full queue,
// and the paginated listing shared by the /tasks screen and its API
// mirror (FR-061).

package tasks

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock makes creation times strictly increasing so the retention
// ordering (newest kept) is deterministic under a fast test.
func fakeClock(q *Queue) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var tick atomic.Int64
	q.Now = func() time.Time { return base.Add(time.Duration(tick.Add(1)) * time.Second) }
}

// instantRunner settles every item as done.
func instantRunner(_ context.Context, tk *Task, _ *slog.Logger, save func()) error {
	for i := range tk.Items {
		tk.Items[i].Status = StatusDone
	}
	save()
	return nil
}

// taskFiles reports whether the task's .json and .log files exist.
func taskFiles(t *testing.T, q *Queue, id string) (json, log bool) {
	t.Helper()
	_, jerr := os.Stat(strings.TrimSuffix(q.LogPath(id), ".log") + ".json")
	_, lerr := os.Stat(q.LogPath(id))
	return jerr == nil, lerr == nil
}

// TestRetentionPurgesAtCreate: with WithRetention(2), each creation keeps
// only the 2 most recent finished tasks — the older ones leave the map
// AND the disk, .log included (the amortized purge of the 2026-08 audit).
func TestRetentionPurgesAtCreate(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, slog.New(slog.DiscardHandler), WithRetention(2))
	if err != nil {
		t.Fatal(err)
	}
	fakeClock(q)
	q.Register(TypeUnitImport, instantRunner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	ids := make([]string, 0, 5)
	for range 5 {
		tk, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "alexis",
			[]Item{{Name: "linux/amd64"}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, q, tk.ID)
		ids = append(ids, tk.ID)
	}

	// Creations 4 and 5 each found 3 finished tasks and purged the oldest:
	// t1 and t2 are gone, t3..t5 remain.
	for _, id := range ids[:2] {
		if _, ok := q.Get(id); ok {
			t.Errorf("purged task %s still answers Get", id)
		}
		if js, lg := taskFiles(t, q, id); js || lg {
			t.Errorf("purged task %s leaves files behind: json=%v log=%v", id, js, lg)
		}
	}
	for _, id := range ids[2:] {
		if _, ok := q.Get(id); !ok {
			t.Errorf("retained task %s vanished", id)
		}
		if js, lg := taskFiles(t, q, id); !js || !lg {
			t.Errorf("retained task %s misses files: json=%v log=%v", id, js, lg)
		}
	}
}

// TestRetentionPurgesPanickedTasks: a task failed by the runProtected
// panic barrier is finished like any other and must age out the same way
// — the recover fix must not create immortal failure records.
func TestRetentionPurgesPanickedTasks(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, slog.New(slog.DiscardHandler), WithRetention(1))
	if err != nil {
		t.Fatal(err)
	}
	fakeClock(q)
	q.Register(TypeSync, func(context.Context, *Task, *slog.Logger, func()) error {
		panic("pathological manifest")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	first, err := q.Create(TypeSync, "oci://cookbook.example/retriever:1", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, q, first.ID); got.Status != StatusFailed {
		t.Fatalf("panicked task settled as %s, want failed", got.Status)
	}
	second, err := q.Create(TypeSync, "oci://cookbook.example/retriever:1", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, q, second.ID)

	// Creating a third purges the first (keep=1 counts only finished).
	third, err := q.Create(TypeSync, "oci://cookbook.example/retriever:1", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Get(first.ID); ok {
		t.Error("panicked task escaped the retention purge")
	}
	if js, lg := taskFiles(t, q, first.ID); js || lg {
		t.Errorf("panicked task leaves files behind: json=%v log=%v", js, lg)
	}
	// Let the last task settle before the TempDir cleanup races its log.
	wait(t, q, third.ID)
}

// TestRetentionPurgesAtOpen: history accumulated before the policy (or
// beyond it) is trimmed at the next start, and active tasks survive it —
// FR-029 owns them, whatever the retention says.
func TestRetentionPurgesAtOpen(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir) // no retention: everything accumulates
	fakeClock(q)
	q.Register(TypeUnitImport, instantRunner)
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)

	finished := make([]string, 0, 4)
	for range 4 {
		tk, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "alexis",
			[]Item{{Name: "linux/amd64"}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, q, tk.ID)
		finished = append(finished, tk.ID)
	}
	cancel() // stop the worker: the next task stays pending
	time.Sleep(20 * time.Millisecond)
	pending, err := q.Create(TypeUnitImport, "quay.io/argoproj/argocd:v2.11.3", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}

	q2, err := Open(dir, slog.New(slog.DiscardHandler), WithRetention(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range finished[:2] {
		if _, ok := q2.Get(id); ok {
			t.Errorf("Open kept %s beyond the retention", id)
		}
		if js, lg := taskFiles(t, q2, id); js || lg {
			t.Errorf("Open left files of %s behind: json=%v log=%v", id, js, lg)
		}
	}
	for _, id := range finished[2:] {
		if _, ok := q2.Get(id); !ok {
			t.Errorf("Open purged %s, one of the %d most recent finished tasks", id, 2)
		}
	}
	got, ok := q2.Get(pending.ID)
	if !ok {
		t.Fatal("the active task was purged — retention must never touch pending/running (FR-029)")
	}
	if got.Status != StatusPending {
		t.Errorf("active task reloaded as %s, want pending", got.Status)
	}
}

// TestCreateOnFullQueueLeavesNoOrphan (2026-08 audit): a Create refused
// because the pending channel is full must leave NO trace — the old
// order persisted first, and the orphaned task came back from disk at
// the next start although its creator had been told "queue is full".
func TestCreateOnFullQueueLeavesNoOrphan(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir) // worker never started: the channel only fills
	for range 256 {       // the channel capacity
		if _, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "a", nil); err != nil {
			t.Fatal(err)
		}
	}
	over, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "a", nil)
	if err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("overflow create: task=%v err=%v, want the full-queue refusal", over, err)
	}
	if n := len(q.List("", "", "")); n != 256 {
		t.Errorf("map holds %d tasks after the refusal, want 256", n)
	}

	// The next start re-queues exactly the accepted tasks: no orphan file.
	q2 := newQueue(t, dir)
	if n := len(q2.List("", "", "")); n != 256 {
		t.Errorf("reloaded %d tasks, want 256 (an orphan survived on disk)", n)
	}
}

// TestHasPending pins the coalescence guard's semantics: pending counts,
// running does not — one queued follow-up behind a running sync is
// exactly what reconciles the state it may have read stale.
func TestHasPending(t *testing.T) {
	q := newQueue(t, t.TempDir())
	if q.HasPending(TypeSync) {
		t.Error("empty queue reports a pending sync")
	}
	tk, err := q.Create(TypeSync, "oci://cookbook.example/retriever:1", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.HasPending(TypeSync) {
		t.Error("queued sync not reported pending")
	}
	if q.HasPending(TypeUnitImport) {
		t.Error("pending sync reported under another type")
	}

	// Run it: a blocked-running sync must NOT count as pending.
	release := make(chan struct{})
	started := make(chan struct{})
	q.Register(TypeSync, func(context.Context, *Task, *slog.Logger, func()) error {
		close(started)
		<-release
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	<-started
	if q.HasPending(TypeSync) {
		t.Error("running sync still reported pending")
	}
	close(release)
	wait(t, q, tk.ID)
	if q.HasPending(TypeSync) {
		t.Error("settled sync reported pending")
	}
}

// TestListPage covers the shared pagination of the task listing (FR-061,
// the /content model): 25-entry pages, metadata, and the harmless
// out-of-range page.
func TestListPage(t *testing.T) {
	q := newQueue(t, t.TempDir())
	fakeClock(q)
	for range 27 { // no worker: everything stays pending, newest first
		if _, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "a", nil); err != nil {
			t.Fatal(err)
		}
	}

	p1 := q.ListPage(ListQuery{Page: 1})
	if len(p1.Tasks) != ListPageSize || p1.Total != 27 || p1.Page != 1 || p1.TotalPages != 2 {
		t.Errorf("page 1 = %d tasks, total=%d page=%d totalPages=%d",
			len(p1.Tasks), p1.Total, p1.Page, p1.TotalPages)
	}
	p2 := q.ListPage(ListQuery{Page: 2})
	if len(p2.Tasks) != 2 || p2.Page != 2 {
		t.Errorf("page 2 = %d tasks, page=%d", len(p2.Tasks), p2.Page)
	}
	// Newest first across the page boundary: page 2 holds the oldest.
	if !p1.Tasks[0].Created.After(p2.Tasks[len(p2.Tasks)-1].Created) {
		t.Error("pagination broke the newest-first ordering")
	}
	// An out-of-range page is an empty window, never an error.
	p9 := q.ListPage(ListQuery{Page: 9})
	if len(p9.Tasks) != 0 || p9.TotalPages != 2 || p9.Total != 27 {
		t.Errorf("page 9 = %d tasks, totalPages=%d total=%d", len(p9.Tasks), p9.TotalPages, p9.Total)
	}
	// Filters compose with pagination.
	pf := q.ListPage(ListQuery{Status: StatusDone})
	if pf.Total != 0 || pf.TotalPages != 1 {
		t.Errorf("filtered page = total %d, totalPages %d", pf.Total, pf.TotalPages)
	}
}

// TestParseListQuery pins the shared UI/API parameter contract of the
// task listing (FR-061), like the store's TestParseBrowseQuery does for
// /content.
func TestParseListQuery(t *testing.T) {
	v := url.Values{}
	v.Set("q", "redis")
	v.Set("status", "failed")
	v.Set("type", TypeSync)
	v.Set("page", "3")
	lq := ParseListQuery(v)
	if lq.Q != "redis" || lq.Status != StatusFailed || lq.Type != TypeSync || lq.Page != 3 {
		t.Errorf("parsed = %+v", lq)
	}
	if !lq.HasFilter() {
		t.Error("filters not detected")
	}
	// Values renders back the same parameters (pagination links).
	if got := lq.Values().Encode(); got != v.Encode() {
		t.Errorf("roundtrip = %q, want %q", got, v.Encode())
	}

	zero := ParseListQuery(url.Values{"page": {"garbage"}})
	if zero.Page != 1 || zero.HasFilter() {
		t.Errorf("zero query = %+v, want page 1 and no filter", zero)
	}
	if enc := zero.Values().Encode(); enc != "" {
		t.Errorf("zero query renders %q, want empty", enc)
	}
}
