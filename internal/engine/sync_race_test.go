// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestSyncPersistsUnderTheTaskLock is the B-016 regression lock, engine
// side. The queue's save() closure reads the whole task clone — publish
// copies its items and resolutions, persist marshals them — under the
// QUEUE's own lock, a lock the engine's ingredient goroutines never
// hold. The engine used to guard its mutations with a local mutex and
// call save() AFTER releasing it: two disjoint locks, no happens-before
// between a goroutine appending to t.Items and the queue marshalling
// them — the data race of B-016, caught by the race detector.
//
// The fixture reproduces the queue's exact reading pattern: a save()
// that marshals the task under its own, engine-invisible mutex, while a
// recipe with several ingredients transfers in parallel (Parallelism 3,
// syncCfg). Run with -race: the pre-fix engine fails here; the taskSink
// — mutation and save under one lock — passes.
func TestSyncPersistsUnderTheTaskLock(t *testing.T) {
	env := newHappyEnv(t)
	task := &tasks.Task{ID: "tsk_b016", RunID: "run_b016", Type: tasks.TypeSync, Status: tasks.StatusRunning}

	// The queue's side of the fence: q.mu in miniature. It orders save()
	// calls among themselves — exactly what Queue.execute provides — and
	// nothing else, so any engine mutation not ordered before the
	// marshal is a race, precisely as with the real queue.
	var queueMu sync.Mutex
	save := func() {
		queueMu.Lock()
		defer queueMu.Unlock()
		if _, err := json.Marshal(task); err != nil {
			t.Errorf("marshalling the task snapshot: %v", err)
		}
	}

	if err := env.eng.Runner()(context.Background(), task, discardLogger(), save); err != nil {
		t.Fatalf("run: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 || agg.Total != 4 {
		t.Errorf("aggregates = %+v, want 4 items, 0 failed", agg)
	}
}
