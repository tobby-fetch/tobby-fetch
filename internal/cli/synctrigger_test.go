// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestSyncTriggerCoalesces (2026-08 audit): the scheduler's trigger must
// not pile identical sync tasks onto the serial queue — a tick that finds
// a sync already pending skips, because that pending task will read the
// latest desired state anyway. A RUNNING sync does not block the next
// tick: the state it read may already be stale, and one queued follow-up
// is exactly what reconciles it.
func TestSyncTriggerCoalesces(t *testing.T) {
	q, err := tasks.Open(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	const source = "oci://cookbook.example/retriever:1"
	trigger := syncTrigger(q, source, false, slog.New(slog.DiscardHandler))

	// Worker not started: the first tick enqueues, the next ones coalesce.
	for range 3 {
		if err := trigger(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(q.List("", tasks.TypeSync, "")); n != 1 {
		t.Fatalf("after 3 ticks: %d sync tasks, want 1 (coalesced)", n)
	}

	// Start the worker on a runner that blocks: the pending task turns
	// running, and the next tick may enqueue one — and only one — again.
	started := make(chan struct{}, 2) // buffered: the runner may run both tasks
	release := make(chan struct{})
	q.Register(tasks.TypeSync, func(context.Context, *tasks.Task, *slog.Logger, func()) error {
		started <- struct{}{}
		<-release
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner never started")
	}
	for range 2 {
		if err := trigger(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(q.List("", tasks.TypeSync, "")); n != 2 {
		t.Errorf("running + ticks: %d sync tasks, want 2 (one running, one pending)", n)
	}
	close(release)

	// Let both tasks settle before the TempDir cleanup races their logs.
	deadline := time.Now().Add(5 * time.Second)
	for q.ActiveCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("tasks never settled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
