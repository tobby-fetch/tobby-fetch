// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tasks

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// When the queue has let go of its files (B-026).
//
// "The task is terminal" and "the queue has closed the task's log" used
// to be two different instants, and only the first was observable: the
// worker goroutine was fire-and-forget, and the log handle was released
// by a defer that ran after the terminal status had already been
// published. On Unix the gap costs nothing, because a file can be removed
// or renamed out from under an open handle. On the mirror workstation
// NFR-018 puts in the operating scope it is the difference between an
// operator ejecting the transport medium after a clean shutdown and being
// told the medium is in use (FR-053, FR-093).

// TestWaitBlocksUntilTheWorkerHasReturned is the primitive the rest of it
// rests on, and it fails on every platform if Start goes back to being
// fire-and-forget.
func TestWaitBlocksUntilTheWorkerHasReturned(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)

	release := make(chan struct{})
	var returned atomic.Bool
	q.Register(TypeUnitImport, func(runCtx context.Context, _ *Task, _ *slog.Logger, _ func()) error {
		select {
		case <-release:
		case <-runCtx.Done():
		}
		// The runner is still inside the worker here; nothing outside may
		// yet believe the worker is finished.
		time.Sleep(20 * time.Millisecond)
		returned.Store(true)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	if _, err := q.Create(TypeUnitImport, "docker.io/library/alpine:3.22.1", "alexis", nil); err != nil {
		t.Fatal(err)
	}
	// Let the worker pick the task up before cancelling, so the wait has
	// something real to wait for.
	deadline := time.Now().Add(5 * time.Second)
	for !q.busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !q.busy() {
		t.Fatal("the worker never picked the task up")
	}

	cancel()
	close(release)
	q.Wait()
	if !returned.Load() {
		t.Error("Wait returned while the worker was still running: " +
			"a caller has no way to know the queue has let go of its files (B-026)")
	}
}

// busy reports that a task is currently running. It reads the same
// published state every other observer reads.
func (q *Queue) busy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.tasks {
		if t.Status == StatusRunning {
			return true
		}
	}
	return false
}

// TestAWaitedQueueHoldsNoTaskLogOpen is the assertion an operator cares
// about: once the worker has returned, nothing under the storage root is
// still held.
//
// Removing a file is how the question is put, because it is the question
// the platform answers differently — Windows refuses to unlink a file
// another handle has open, Unix does not. So on Unix this test passes
// whatever the code does, and it is the Windows runner that gives it
// teeth. It earns its place there: this is the exact operation the FR-029
// retention purge performs on a task's log, and the one an operator
// performs on the whole medium.
func TestAWaitedQueueHoldsNoTaskLogOpen(t *testing.T) {
	dir := t.TempDir()
	q := newQueue(t, dir)
	q.Register(TypeUnitImport, func(_ context.Context, tk *Task, logger *slog.Logger, save func()) error {
		for i := range tk.Items {
			tk.Items[i].Status = StatusDone
		}
		logger.Info("something worth writing to the task log")
		save()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	for range 3 {
		if _, err := q.Create(TypeUnitImport, "docker.io/library/alpine:3.22.1", "alexis", nil); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for q.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q.ActiveCount() > 0 {
		t.Fatal("the tasks never settled")
	}
	cancel()
	q.Wait()

	entries, err := os.ReadDir(q.dir)
	if err != nil {
		t.Fatal(err)
	}
	logs := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		logs++
		if err := os.Remove(filepath.Join(q.dir, e.Name())); err != nil {
			t.Errorf("the queue still holds %s open after Wait returned: %v", e.Name(), err)
		}
	}
	if logs != 3 {
		t.Errorf("found %d task logs, want 3: the fixture did not exercise what it claims", logs)
	}
}
