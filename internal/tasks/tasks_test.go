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
