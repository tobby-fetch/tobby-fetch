// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tasks

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestPublishedTaskIsIsolatedFromTheRunner is the B-016 regression lock,
// queue side. The runner mutates its task clone — items, per-blob
// progress rows, resolutions — under its own lock and calls save() while
// still holding it, which is the corrected engine pattern. The queue
// must publish a snapshot that shares NO mutable memory with the clone:
// before the fix, publish and Get took shallow item copies whose Blobs
// backing arrays the runner kept mutating through TrackBlob, so the
// UI-polling readers (Get, List) and persist's marshalling raced with
// the runner's next progress tick. Run with -race: the shallow copy
// fails here, the deep one passes.
func TestPublishedTaskIsIsolatedFromTheRunner(t *testing.T) {
	q := newQueue(t, t.TempDir())
	const workers = 3
	q.Register(TypeSync, func(_ context.Context, tk *Task, _ *slog.Logger, save func()) error {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for g := range workers {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 1; i <= 100; i++ {
					mu.Lock()
					item := &tk.Items[g]
					item.TrackBlob("sha256:blob", int64(i), 1000, false, false)
					item.SizeBytes = int64(i)
					tk.Resolutions = append(tk.Resolutions, Resolution{
						Recipe: "r@1", Ingredient: item.Name, Requested: "1", Resolved: "1",
					})
					// save() under the mutation lock: the writer half of the
					// B-016 contract. The reader half — what this test pins —
					// is that everything published from here is a deep copy.
					save()
					mu.Unlock()
				}
			}(g)
		}
		wg.Wait()
		mu.Lock()
		for i := range tk.Items {
			tk.Items[i].Status = StatusDone
		}
		save()
		mu.Unlock()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	tk, err := q.Create(TypeSync, "oci://cookbook.example/retriever:1", "alexis",
		[]Item{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if err != nil {
		t.Fatal(err)
	}

	// The UI's polling loop: read every deep field of the published task
	// while the runner still mutates its clone.
	var done *Task
	for done == nil {
		got, ok := q.Get(tk.ID)
		if !ok {
			t.Fatal("task disappeared")
		}
		var received int64
		for i := range got.Items {
			for _, b := range got.Items[i].Blobs {
				received += b.ReceivedBytes
			}
		}
		_ = received
		for _, l := range q.List("", TypeSync, "") {
			_ = l.Aggregate()
		}
		if !got.Active() {
			done = got
		}
	}
	if done.Status != StatusDone {
		t.Fatalf("status = %s, want done (err=%+v)", done.Status, done.Error)
	}
	if len(done.Resolutions) != workers*100 {
		t.Errorf("resolutions = %d, want %d", len(done.Resolutions), workers*100)
	}
}

// TestRunnerPanicFailsTheTaskOnce: a panicking runner — a third-party
// parser on a pathological manifest is enough — must fail the task with
// the internal taxonomy code and leave the process alive; and because a
// task found active on disk is re-queued at the next start (FR-029), the
// failure must be terminal, or the restart would replay the panic
// forever: a permanent crash loop with no operator escape.
func TestRunnerPanicFailsTheTaskOnce(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)
	q.Register(TypeSync, func(context.Context, *Task, *slog.Logger, func()) error {
		panic("pathological manifest")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	tk, err := q.Create(TypeSync, "oci://cookbook.example/retriever:1", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, q, tk.ID)
	if done.Status != StatusFailed || done.Error == nil || done.Error.Code != taxonomy.CodeInternal {
		t.Fatalf("panicked task = status %s, error %+v; want failed with %s",
			done.Status, done.Error, taxonomy.CodeInternal)
	}

	// The stack is in the task log: the only artifact an operator can
	// correlate (FR-090) when the panic came from a dependency.
	chunk, _, err := q.ReadLog(tk.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chunk), "pathological manifest") || !strings.Contains(string(chunk), "goroutine") {
		t.Errorf("task log misses the panic value or the stack:\n%s", chunk)
	}

	// The next instance start must NOT re-queue the task: failed is
	// terminal, so the panic is never replayed.
	q2 := newQueue(t, dir)
	got, ok := q2.Get(tk.ID)
	if !ok {
		t.Fatal("task lost across restart")
	}
	if got.Status != StatusFailed || got.Resumed {
		t.Fatalf("after restart: status=%s resumed=%v, want failed and not resumed", got.Status, got.Resumed)
	}
}

// TestBoundaryHookRunsOncePerSettledTask is the queue half of FR-056: the
// transport medium's log is fsync'd at task boundaries, and the queue is
// what knows where those are.
//
// The hook must fire AFTER the task has settled and its records are
// written — a flush taken mid-run would leave the "task finished" record,
// the one an operator looks for, on the wrong side of the barrier. It
// must also fire on a task that FAILED: an operation refused by a
// corrupted medium is exactly the one whose trail must survive the medium
// being pulled out in annoyance.
//
// The task type is incidental — the boundary belongs to the queue, not to
// any runner — so the oldest one is used rather than the newest.
func TestBoundaryHookRunsOncePerSettledTask(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	var seen []Status
	settled := make(chan struct{}, 4)

	// The hook reads each task's state through the queue's own accessor,
	// so a flush taken before finish() would observe "running" — which is
	// the regression this test exists to catch.
	var q *Queue
	hook := func() {
		mu.Lock()
		known := append([]string(nil), ids...)
		mu.Unlock()
		for _, id := range known {
			if task, ok := q.Get(id); ok && !task.Active() {
				mu.Lock()
				seen = append(seen, task.Status)
				mu.Unlock()
			}
		}
		settled <- struct{}{}
	}

	var err error
	q, err = Open(t.TempDir(), slog.New(slog.DiscardHandler), WithBoundary(hook))
	if err != nil {
		t.Fatal(err)
	}
	q.Register(TypeSync, func(_ context.Context, task *Task, _ *slog.Logger, _ func()) error {
		if task.Reference == "doomed" {
			return taxonomy.New(taxonomy.CodeInternal, nil)
		}
		return nil
	})

	for _, ref := range []string{"good", "doomed"} {
		task, cerr := q.Create(TypeSync, ref, "local", nil)
		if cerr != nil {
			t.Fatal(cerr)
		}
		mu.Lock()
		ids = append(ids, task.ID)
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	for range 2 {
		select {
		case <-settled:
		case <-time.After(5 * time.Second):
			t.Fatal("the boundary hook did not fire for every task")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("the hook observed %v: it must run once per SETTLED task, failures included", seen)
	}
	for _, s := range seen {
		if s == StatusRunning || s == StatusPending {
			t.Errorf("the boundary hook saw a %s task: it must run once the task has settled", s)
		}
	}
	if !containsStatus(seen, StatusFailed) {
		t.Error("no failed task reached the boundary hook: a refused import's trail must be flushed too")
	}
}

func containsStatus(ss []Status, want Status) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
