// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/runid"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// tasksDir is Tobby's own area inside the store (FR-050: the store is
// self-contained — artifacts, recipes, operation logs). The "_tobby"
// prefix cannot collide with a repository name (underscore-leading path
// segments are invalid in OCI names).
const tasksDir = "_tobby/tasks"

// Runner executes one task type. It mutates t's items as it progresses and
// calls save() after every meaningful step so an interruption never loses
// more than one step (FR-029). The returned error is the task-level
// failure, persisted as code + parameters.
type Runner func(ctx context.Context, t *Task, logger *slog.Logger, save func()) error

// Queue is the persistent task queue: one serial worker at this milestone —
// unit imports are serial operations, and honesty beats fake parallelism.
type Queue struct {
	dir    string
	logger *slog.Logger

	mu      sync.Mutex
	tasks   map[string]*Task
	runners map[string]Runner
	pending chan string
	// Now injects time in tests.
	Now func() time.Time
}

// Open loads the queue from the store root, re-queuing interrupted tasks
// (FR-029): a task found pending or running is marked Resumed and runs
// again — the import pipeline is incremental by digest, so a re-run only
// transfers what is missing. No task is ever left orphaned.
func Open(storeRoot string, logger *slog.Logger) (*Queue, error) {
	dir := filepath.Join(storeRoot, tasksDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("tasks: creating %s: %w", dir, err)
	}
	q := &Queue{
		dir:     dir,
		logger:  logger,
		tasks:   map[string]*Task{},
		runners: map[string]Runner{},
		pending: make(chan string, 256),
		Now:     time.Now,
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading %s: %w", dir, err)
	}
	var resumed []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: enumerated task files under the store
		if err != nil {
			return nil, fmt.Errorf("tasks: reading %s: %w", e.Name(), err)
		}
		var t Task
		if err := json.Unmarshal(raw, &t); err != nil {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "skipping unreadable task file",
				slog.String("file", e.Name()), slog.String("error", err.Error()))
			continue
		}
		if t.Active() {
			t.Status = StatusPending
			t.Resumed = true
			resumed = append(resumed, t.ID)
		}
		q.tasks[t.ID] = &t
	}
	// Persist the resumed marker, oldest first, and re-queue.
	sort.Slice(resumed, func(i, j int) bool {
		return q.tasks[resumed[i]].Created.Before(q.tasks[resumed[j]].Created)
	})
	for _, id := range resumed {
		q.persist(q.tasks[id])
		q.pending <- id
		logger.LogAttrs(context.Background(), slog.LevelInfo, "task resumed after interruption",
			slog.String("task_id", id), slog.String("run_id", q.tasks[id].RunID))
	}
	return q, nil
}

// Register installs the runner for a task type. Call before Start.
func (q *Queue) Register(taskType string, r Runner) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runners[taskType] = r
}

// TaskOption adjusts a task at creation time (Queue.Create).
type TaskOption func(*Task)

// WithVendorDependencies enables the FR-025 dependency vendoring for this
// operation. Explicit and per-operation — never a default.
func WithVendorDependencies() TaskOption {
	return func(t *Task) { t.VendorDependencies = true }
}

// Create persists and enqueues a new task, returning it.
func (q *Queue) Create(taskType, reference, actor string, items []Item, opts ...TaskOption) (*Task, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := &Task{
		ID:        newID("tsk_"),
		RunID:     runid.New(),
		Type:      taskType,
		Reference: reference,
		Actor:     actor,
		Status:    StatusPending,
		Created:   q.Now().UTC(),
		Items:     items,
	}
	for _, opt := range opts {
		opt(t)
	}
	q.tasks[t.ID] = t
	q.persist(t)
	select {
	case q.pending <- t.ID:
	default:
		return nil, errors.New("tasks: queue is full")
	}
	return t, nil
}

// Start runs the worker until ctx is canceled. A task interrupted by
// shutdown stays running on disk and resumes on the next Open.
func (q *Queue) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-q.pending:
				q.execute(ctx, id)
			}
		}
	}()
}

// execute runs one task through its registered runner. The runner works
// on its own deep copy of the task: concurrent readers (Get, List — the
// UI polling) only ever observe states published under the lock by save,
// never a mid-write item; a crash between saves loses at most one step,
// unchanged (FR-029).
func (q *Queue) execute(ctx context.Context, id string) {
	q.mu.Lock()
	t, ok := q.tasks[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	runner := q.runners[t.Type]
	t.Status = StatusRunning
	t.Started = q.Now().UTC()
	q.persist(t)
	work := t.clone()
	q.mu.Unlock()

	taskLogger, closeLog := q.taskLogger(work)
	defer closeLog()

	save := func() {
		q.mu.Lock()
		q.publish(t, work)
		q.mu.Unlock()
	}

	if runner == nil {
		q.finish(t, work, taskLogger, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("no runner for task type %q", t.Type)))
		return
	}
	err := runner(ctx, work, taskLogger, save)
	if errors.Is(err, context.Canceled) {
		// Graceful shutdown caught the task mid-run: leave it running on
		// disk — the next start resumes it (FR-029). Never mark it failed
		// for being interrupted.
		save()
		taskLogger.LogAttrs(context.Background(), slog.LevelInfo,
			"task interrupted by shutdown, will resume")
		return
	}
	var te *taxonomy.Error
	if err != nil && !errors.As(err, &te) {
		te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	q.finish(t, work, taskLogger, te)
}

// clone deep-copies the task for the runner's exclusive use (items and
// reports are the runner-mutated parts; persisted errors are never
// mutated in place). Every runner-mutated slice belongs here: a report
// the runner fills but clone and publish ignore is silently lost.
func (t *Task) clone() *Task {
	cp := *t
	cp.Items = append([]Item(nil), t.Items...)
	cp.ChartDependencies = append([]ChartDependency(nil), t.ChartDependencies...)
	cp.Resolutions = append([]Resolution(nil), t.Resolutions...)
	return &cp
}

// publish copies the runner's progress into the canonical task and
// persists it. Callers hold q.mu.
func (q *Queue) publish(dst, src *Task) {
	dst.Status = src.Status
	dst.Error = src.Error
	dst.Finished = src.Finished
	dst.Items = append([]Item(nil), src.Items...)
	dst.ChartDependencies = append([]ChartDependency(nil), src.ChartDependencies...)
	dst.Resolutions = append([]Resolution(nil), src.Resolutions...)
	q.persist(dst)
}

// finish settles the task's final status: failed on a task-level error or
// any failed item, done otherwise.
func (q *Queue) finish(t, work *Task, logger *slog.Logger, te *taxonomy.Error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if te != nil {
		work.Error = FromTaxonomy(te)
	}
	agg := work.Aggregate()
	work.Status = StatusDone
	if te != nil || agg.Failed > 0 {
		work.Status = StatusFailed
	}
	work.Finished = q.Now().UTC()
	q.publish(t, work)

	attrs := []slog.Attr{
		slog.String("task_id", work.ID),
		slog.String("run_id", work.RunID),
		slog.String("status", string(work.Status)),
		slog.Int("items_done", agg.Done),
		slog.Int("items_failed", agg.Failed),
	}
	if te != nil {
		attrs = append(attrs, slog.String("error", te.Error()))
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "task finished", attrs...)
}

// persist writes the task file atomically. Callers hold q.mu.
func (q *Queue) persist(t *Task) {
	out, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		q.logger.LogAttrs(context.Background(), slog.LevelError, "encoding task",
			slog.String("task_id", t.ID), slog.String("error", err.Error()))
		return
	}
	path := filepath.Join(q.dir, t.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o640); err == nil { //nolint:gosec // G306: task records are store content, group-readable
		if err := os.Rename(tmp, path); err != nil {
			q.logger.LogAttrs(context.Background(), slog.LevelError, "writing task",
				slog.String("task_id", t.ID), slog.String("error", err.Error()))
		}
	} else {
		q.logger.LogAttrs(context.Background(), slog.LevelError, "writing task",
			slog.String("task_id", t.ID), slog.String("error", err.Error()))
	}
}

// taskLogger builds the task's dedicated logger: JSON records carrying the
// correlation fields (FR-090), written to the instance stream AND to the
// task's own log file (readable from the detail screen, transported with
// the store).
func (q *Queue) taskLogger(t *Task) (logger *slog.Logger, closeLog func()) {
	path := filepath.Join(q.dir, t.ID+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // G302,G304: task log under the store
	base := q.logger
	if err != nil {
		q.logger.LogAttrs(context.Background(), slog.LevelError, "opening task log",
			slog.String("task_id", t.ID), slog.String("error", err.Error()))
		return base.With(slog.String("task_id", t.ID), slog.String("run_id", t.RunID)),
			func() {}
	}
	fileLogger := logging.New(f, slog.LevelDebug)
	tee := slog.New(teeHandler{a: base.Handler(), b: fileLogger.Handler()}).
		With(slog.String("task_id", t.ID), slog.String("run_id", t.RunID))
	return tee, func() { _ = f.Close() }
}

// teeHandler duplicates records to two handlers (instance stream + task
// file).
type teeHandler struct{ a, b slog.Handler }

func (h teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.a.Enabled(ctx, l) || h.b.Enabled(ctx, l)
}

func (h teeHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // hugeParam: slog.Handler's fixed signature
	err1 := h.a.Handle(ctx, r.Clone())
	err2 := h.b.Handle(ctx, r)
	return errors.Join(err1, err2)
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{a: h.a.WithAttrs(attrs), b: h.b.WithAttrs(attrs)}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{a: h.a.WithGroup(name), b: h.b.WithGroup(name)}
}

// Get returns a task by id.
func (q *Queue) Get(id string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	cp.Items = append([]Item(nil), t.Items...)
	return &cp, true
}

// List returns tasks filtered by status, type, and reference substring
// (?q=, R-06 parity), newest first.
func (q *Queue) List(status Status, taskType, query string) []*Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*Task, 0, len(q.tasks))
	for _, t := range q.tasks {
		if status != "" && t.Status != status {
			continue
		}
		if taskType != "" && t.Type != taskType {
			continue
		}
		if query != "" && !strings.Contains(t.Reference, query) {
			continue
		}
		cp := *t
		cp.Items = append([]Item(nil), t.Items...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// ActiveCount reports how many tasks still move (the nav badge).
func (q *Queue) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, t := range q.tasks {
		if t.Active() {
			n++
		}
	}
	return n
}

// LogPath returns the task's log file path.
func (q *Queue) LogPath(id string) string {
	return filepath.Join(q.dir, id+".log")
}

// ReadLog reads the task log from a byte cursor, returning the new cursor —
// the incremental follow of the detail screen (same contract a future SSE
// transport reuses, UI-SPEC §8).
func (q *Queue) ReadLog(id string, from int64) (chunk []byte, next int64, err error) {
	f, err := os.Open(q.LogPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, from, nil
	}
	if err != nil {
		return nil, from, err
	}
	defer f.Close() //nolint:errcheck // read-only file
	if from > 0 {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return nil, from, err
		}
	}
	chunk, err = io.ReadAll(f)
	if err != nil {
		return nil, from, err
	}
	return chunk, from + int64(len(chunk)), nil
}
