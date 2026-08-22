// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tasks

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

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
