// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tasks

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func newQueue(t *testing.T, dir string) *Queue {
	t.Helper()
	q, err := Open(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// wait polls until the task settles or the deadline passes.
func wait(t *testing.T, q *Queue, id string) *Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tk, ok := q.Get(id); ok && !tk.Active() {
			return tk
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s never settled", id)
	return nil
}

func TestTaskLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)
	q.Register(TypeUnitImport, func(_ context.Context, tk *Task, logger *slog.Logger, save func()) error {
		for i := range tk.Items {
			tk.Items[i].Status = StatusDone
			tk.Items[i].SizeBytes = 100
			save()
		}
		logger.LogAttrs(context.Background(), slog.LevelInfo, "transferred everything")
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	tk, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "alexis",
		[]Item{{Name: "linux/amd64"}, {Name: "linux/arm64"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tk.ID, "tsk_") || tk.RunID == "" {
		t.Errorf("ids: %q %q", tk.ID, tk.RunID)
	}

	done := wait(t, q, tk.ID)
	if done.Status != StatusDone {
		t.Fatalf("status = %s, want done (err=%+v)", done.Status, done.Error)
	}
	agg := done.Aggregate()
	if agg.Total != 2 || agg.Done != 2 || agg.Failed != 0 {
		t.Errorf("aggregates = %+v", agg)
	}

	// The task log carries the correlation fields (FR-090).
	chunk, next, err := q.ReadLog(tk.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next == 0 || !strings.Contains(string(chunk), tk.RunID) || !strings.Contains(string(chunk), tk.ID) {
		t.Errorf("task log misses correlation fields:\n%s", chunk)
	}
	// Incremental cursor: nothing new after the end.
	again, next2, err := q.ReadLog(tk.ID, next)
	if err != nil || len(again) != 0 || next2 != next {
		t.Errorf("cursor read: %d bytes, next %d→%d, err=%v", len(again), next, next2, err)
	}

	// A second Open sees the finished task (persistence, FR-050).
	q2 := newQueue(t, dir)
	got, ok := q2.Get(tk.ID)
	if !ok || got.Status != StatusDone || len(got.Items) != 2 {
		t.Fatalf("reloaded task: %+v ok=%v", got, ok)
	}
}

// TestPartialFailure locks the "done with errors" contract: failed status,
// aggregates carry the split, the principal code is the per-item error,
// and the persisted error is code + params — never a sentence.
func TestPartialFailure(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)
	q.Register(TypeUnitImport, func(_ context.Context, tk *Task, _ *slog.Logger, save func()) error {
		tk.Items[0].Status = StatusDone
		tk.Items[1].Status = StatusFailed
		tk.Items[1].Error = FromTaxonomy(taxonomy.New(taxonomy.CodeRegistryAuth,
			taxonomy.Params{"host": "docker.io"}))
		save()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	tk, err := q.Create(TypeUnitImport, "docker.io/bitnami/wordpress:6.4.2", "alexis",
		[]Item{{Name: "linux/amd64"}, {Name: "linux/arm64"}})
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, q, tk.ID)
	if done.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", done.Status)
	}
	agg := done.Aggregate()
	if agg.Done != 1 || agg.Failed != 1 {
		t.Errorf("aggregates = %+v", agg)
	}
	p := done.Principal()
	if p == nil || p.Code() != taxonomy.CodeRegistryAuth {
		t.Errorf("principal = %v", p)
	}
	// Re-rendering in French works from the persisted form (FR-063).
	msg := taxonomy.Localize("fr", p)
	if !strings.Contains(msg.Cause, "docker.io") {
		t.Errorf("re-rendered cause misses params: %q", msg.Cause)
	}
}

// TestResumeAfterInterruption is the FR-029 acceptance: a task caught
// running when the instance stops is re-queued at the next start, marked
// Resumed, and no task is left orphaned.
func TestResumeAfterInterruption(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)
	var started atomic.Bool
	q.Register(TypeUnitImport, func(ctx context.Context, tk *Task, _ *slog.Logger, save func()) error {
		tk.Items[0].Status = StatusRunning
		save()
		started.Store(true)
		<-ctx.Done() // simulate an interruption mid-transfer
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	tk, err := q.Create(TypeUnitImport, "quay.io/argoproj/argocd:v2.11.3", "alexis",
		[]Item{{Name: "linux/amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("runner never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel() // "crash"
	time.Sleep(50 * time.Millisecond)

	// Next instance start: the task is pending again, flagged Resumed.
	q2 := newQueue(t, dir)
	got, ok := q2.Get(tk.ID)
	if !ok {
		t.Fatal("task lost across restart")
	}
	if got.Status != StatusPending || !got.Resumed {
		t.Fatalf("after restart: status=%s resumed=%v, want pending+resumed", got.Status, got.Resumed)
	}

	// This time the runner completes: the resumed task settles cleanly.
	q2.Register(TypeUnitImport, func(_ context.Context, tk *Task, _ *slog.Logger, save func()) error {
		for i := range tk.Items {
			tk.Items[i].Status = StatusDone
		}
		save()
		return nil
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	q2.Start(ctx2)
	done := wait(t, q2, tk.ID)
	if done.Status != StatusDone || !done.Resumed {
		t.Errorf("resumed task: status=%s resumed=%v", done.Status, done.Resumed)
	}
}

func TestListFiltersAndBadge(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)
	// No worker: everything stays pending.
	if _, err := q.Create(TypeUnitImport, "docker.io/library/redis:7.2", "a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Create(TypeUnitImport, "quay.io/argoproj/argocd:v2.11.3", "a", nil); err != nil {
		t.Fatal(err)
	}

	if n := len(q.List("", "", "")); n != 2 {
		t.Errorf("unfiltered list = %d", n)
	}
	if n := len(q.List(StatusPending, "", "redis")); n != 1 {
		t.Errorf("q=redis list = %d, want 1 (R-06 substring)", n)
	}
	if n := len(q.List(StatusDone, "", "")); n != 0 {
		t.Errorf("done list = %d", n)
	}
	if n := q.ActiveCount(); n != 2 {
		t.Errorf("active count = %d", n)
	}
}

// TestTaskErrorWithoutRunner: a task whose type has no runner fails with
// the internal taxonomy code instead of hanging.
func TestTaskErrorWithoutRunner(t *testing.T) {
	q := newQueue(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	tk, err := q.Create("unknown-type", "x", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, q, tk.ID)
	if done.Status != StatusFailed || done.Error == nil || done.Error.Code != taxonomy.CodeInternal {
		t.Errorf("no-runner task: %+v", done)
	}
}

// TestRunnerErrorMapping: a plain error from a runner persists as the
// internal code; a taxonomy error keeps its code.
func TestRunnerErrorMapping(t *testing.T) {
	q := newQueue(t, t.TempDir())
	q.Register("plain", func(context.Context, *Task, *slog.Logger, func()) error {
		return errors.New("boom")
	})
	q.Register("taxo", func(context.Context, *Task, *slog.Logger, func()) error {
		return taxonomy.New(taxonomy.CodeInspectTimeout, taxonomy.Params{"host": "h", "timeout": "20s"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	t1, _ := q.Create("plain", "r", "a", nil)
	t2, _ := q.Create("taxo", "r", "a", nil)
	if d := wait(t, q, t1.ID); d.Error == nil || d.Error.Code != taxonomy.CodeInternal {
		t.Errorf("plain error mapped to %+v", d.Error)
	}
	if d := wait(t, q, t2.ID); d.Error == nil || d.Error.Code != taxonomy.CodeInspectTimeout {
		t.Errorf("taxonomy error mapped to %+v", d.Error)
	}
}

// TestRunnerReportsSurviveTheClone: every report a runner fills on its
// task clone must reach the canonical task. Resolutions (FR-021) were
// lost this way — the runner filled them, clone and publish ignored
// them, and the report came back empty on the API and the screen.
func TestRunnerReportsSurviveTheClone(t *testing.T) {
	storeRoot := t.TempDir()
	q, err := Open(storeRoot, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	q.Register("report-check", func(_ context.Context, task *Task, _ *slog.Logger, save func()) error {
		task.Resolutions = append(task.Resolutions, Resolution{
			Recipe: "wordpress@6.8.2", Ingredient: "app",
			Requested: "12.x", Resolved: "12.4.1", Digest: "sha256:abc",
			Status: "new", Effective: "zone-a.example.com/docker.io/bitnami/wordpress",
		})
		task.ChartDependencies = append(task.ChartDependencies, ChartDependency{
			Chart: "wordpress", Name: "mariadb", Version: "16.0.0", Embedded: true,
		})
		task.Items = append(task.Items, Item{Name: "app", Status: StatusDone})
		save()
		return nil
	})
	created, err := q.Create("report-check", "oci://cookbook.example/retriever:1", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	deadline := time.Now().Add(10 * time.Second)
	var settled *Task
	for {
		got, ok := q.Get(created.ID)
		if ok && !got.Active() {
			settled = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task never settled")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(settled.Resolutions) != 1 {
		t.Fatalf("resolutions = %+v, want the runner's row (FR-021 report lost in the clone)", settled.Resolutions)
	}
	if got := settled.Resolutions[0]; got.Resolved != "12.4.1" || got.Effective == "" {
		t.Errorf("resolution = %+v, want the resolved tag and the effective endpoint", got)
	}
	if len(settled.ChartDependencies) != 1 || len(settled.Items) != 1 {
		t.Errorf("other reports lost: deps=%d items=%d", len(settled.ChartDependencies), len(settled.Items))
	}

	// The report also survives a reload from the store (FR-050: the
	// history travels with it).
	reopened, err := Open(storeRoot, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.Get(created.ID)
	if !ok || len(persisted.Resolutions) != 1 {
		t.Errorf("resolutions did not survive persistence: %+v", persisted)
	}
}

// TestBlobProgressIsKeyedByDigest: a resumed blob updates its own row
// rather than appending a second one, or the task detail would grow a new
// line per attempt and describe a restart it did not perform (FR-029).
func TestBlobProgressIsKeyedByDigest(t *testing.T) {
	const a, b = "sha256:aaa", "sha256:bbb"
	it := &Item{Name: "linux/amd64"}

	it.TrackBlob(a, 100, 1000, false, false)
	it.TrackBlob(a, 400, 1000, false, false)
	it.TrackBlob(b, 50, 200, false, false)
	if len(it.Blobs) != 2 {
		t.Fatalf("blobs = %+v, want one row per digest", it.Blobs)
	}
	if it.Blobs[0].ReceivedBytes != 400 || it.Blobs[0].SizeBytes != 1000 {
		t.Errorf("row a = %+v", it.Blobs[0])
	}

	// Once a blob has been resumed the row says so for good: it records
	// that the transfer picked up earlier bytes, not what the last chunk
	// happened to do.
	it.TrackBlob(a, 600, 1000, true, false)
	it.TrackBlob(a, 800, 1000, false, false)
	if !it.Blobs[0].Resumed {
		t.Errorf("row a = %+v, want Resumed to stick", it.Blobs[0])
	}

	it.TrackBlobDone(a)
	if !it.Blobs[0].Done || it.Blobs[0].ReceivedBytes != 1000 {
		t.Errorf("row a = %+v, want done at full size", it.Blobs[0])
	}
	// A digest that was never tracked — every blob below the resume
	// threshold — is not invented on completion.
	it.TrackBlobDone("sha256:never-seen")
	if len(it.Blobs) != 2 {
		t.Errorf("blobs = %+v, want no row for an untracked blob", it.Blobs)
	}
}
