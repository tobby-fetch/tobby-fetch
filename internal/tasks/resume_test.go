// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Resume tests (FR-029): what an instance does at startup with the tasks
// a previous run left active.

package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTaskFile plants a task record in the store the way an instance
// that was interrupted would have left it.
func writeTaskFile(t *testing.T, root string, tk *Task) {
	t.Helper()
	dir := filepath.Join(root, tasksDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(tk, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tk.ID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOpenResumesMoreTasksThanTheChannelHolds is B-019: Open re-queued
// every active task with a BLOCKING send on a bounded channel, and no
// worker consumes it before Open returns — so a store carrying more
// pending-or-running tasks than the channel holds wedged the instance at
// startup, silently and forever. Retention never bounded that set: FR-029
// owns active tasks and they are deliberately never purged.
//
// The invariant has two halves, and the first without the second would be
// satisfied by simply dropping the overflow: Open returns, AND every
// resumed task still runs.
func TestOpenResumesMoreTasksThanTheChannelHolds(t *testing.T) {
	dir := t.TempDir()
	const active = pendingCapacity + 40
	for i := range active {
		id := fmt.Sprintf("tsk_%04d", i)
		writeTaskFile(t, dir, &Task{
			ID: id, RunID: "run_" + id, Type: TypeUnitImport,
			Reference: "example.test/app:1.0.0", Actor: "alexis",
			Status:  StatusRunning,
			Created: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second),
			Items:   []Item{{Name: "linux/amd64", Status: StatusPending}},
		})
	}

	type opened struct {
		q   *Queue
		err error
	}
	done := make(chan opened, 1)
	go func() {
		q, err := Open(dir, slog.New(slog.DiscardHandler))
		done <- opened{q, err}
	}()
	var got opened
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("Open never returned with %d active tasks in the store (B-019)", active)
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	q := got.q

	if n := q.ActiveCount(); n != active {
		t.Fatalf("active tasks after Open = %d, want %d", n, active)
	}

	// And they all run: the backlog is a deferral, not a loss (FR-029).
	q.Register(TypeUnitImport, instantRunner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	deadline := time.Now().Add(60 * time.Second)
	for q.ActiveCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d resumed tasks never ran", q.ActiveCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := range active {
		id := fmt.Sprintf("tsk_%04d", i)
		tk, ok := q.Get(id)
		if !ok || tk.Status != StatusDone || !tk.Resumed {
			t.Fatalf("task %s = %+v (found %v), want a resumed task settled done", id, tk, ok)
		}
	}
}
