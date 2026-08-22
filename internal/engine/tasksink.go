// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"sync"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// taskSink is the single mutation surface of a running task (B-016).
//
// The queue hands the runner a task clone and a save() closure, and
// save() READS the clone — publish snapshots its items and resolutions,
// persist marshals them — under the queue's own lock, which the
// engine's ingredient goroutines never hold. The engine used to guard
// its mutations with a local mutex and call save() after releasing it:
// two disjoint locks, hence no happens-before between a goroutine
// writing the task and the queue reading it — a data race under the Go
// memory model, confirmed by the race detector (B-016).
//
// The sink closes the gap by construction: every mutation AND the
// save() that publishes it run under one lock, so the queue's read is
// ordered after the write that produced it. It also collapses the
// (task, mutex, save) triple that was threaded through four call levels
// of sync.go and promote.go into one value.
type taskSink struct {
	mu   sync.Mutex
	task *tasks.Task
	save func()
}

// newTaskSink wraps one runner invocation's task clone and save closure.
func newTaskSink(t *tasks.Task, save func()) *taskSink {
	return &taskSink{task: t, save: save}
}

// update applies fn to the task under the sink lock. fn reports whether
// it changed the task; a change is persisted by save() BEFORE the lock
// is released. That ordering is the point, not a detail: save() reads
// the task from the queue's side, and holding the lock across the call
// is what synchronizes that read with every writer going through here.
func (s *taskSink) update(fn func(t *tasks.Task) (changed bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn(s.task) && s.save != nil {
		s.save()
	}
}

// runID returns the task's correlation id (FR-090). It is immutable
// after creation, so reading it takes no lock.
func (s *taskSink) runID() string { return s.task.RunID }
