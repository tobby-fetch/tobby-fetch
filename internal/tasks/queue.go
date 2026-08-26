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
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
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

// pendingCapacity bounds the worker's hand-off channel. It is a buffer,
// not the queue's real depth: what is waiting to run lives in q.tasks
// with StatusPending, and the channel only carries the ids the worker has
// not picked up yet. Create refuses beyond it — "queue is full", with no
// trace left (2026-08 audit) — because a caller told to retry later is a
// better answer than an unbounded backlog.
const pendingCapacity = 256

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
	// keepFinished bounds how many finished tasks the queue retains
	// (WithRetention); 0 keeps everything.
	keepFinished int
	// boundary runs once per settled task (WithBoundary): the FR-056
	// fsync point of the transport medium's log. Nil does nothing.
	boundary func()

	mu      sync.Mutex
	tasks   map[string]*Task
	runners map[string]Runner
	pending chan string
	// resuming is the FR-029 backlog: the tasks Open found active, oldest
	// first, waiting for the worker to reach them (B-019). It is a slice
	// and not the channel on purpose — the channel is bounded and nothing
	// consumes it during Open, so re-queuing through it wedged startup on
	// any store carrying more active tasks than it holds. Nothing bounds
	// this backlog either, and nothing should: these tasks already exist
	// on disk, and refusing to remember them would be losing them.
	resuming []string
	// worker tracks the goroutine Start launched, so Wait can await it.
	worker sync.WaitGroup
	// Now injects time in tests.
	Now func() time.Time
}

// QueueOption adjusts the queue at Open time.
type QueueOption func(*Queue)

// WithRetention keeps only the `keep` most recent FINISHED tasks (done or
// failed — a task failed by the runProtected panic barrier ages out like
// any other). Older ones are removed from memory and their .json and .log
// files deleted. Pending and running tasks are NEVER purged: FR-029's
// resume contract owns them. 0 disables the purge and keeps the full
// history — the pre-retention behavior.
//
// Why this exists (2026-08 audit): every task lived forever — one map
// entry in RAM plus two files on disk — and a passthrough instance on a
// 10-minute cycle mints ~52 000 sync tasks a year, all reloaded by every
// Open.
func WithRetention(keep int) QueueOption {
	return func(q *Queue) { q.keepFinished = keep }
}

// WithBoundary installs a callback the worker runs at every task
// boundary — once a task has settled and every record it produced has
// been written.
//
// It exists for FR-056: the operation log on a transport medium is
// fsync'd here and nowhere else. The boundary is the granularity the
// requirement fixes, and it belongs to the queue because the queue is
// what knows when a task ended: an engine that flushed on its own last
// line would miss the queue's own "task finished" record, which is
// precisely the one an operator looks for.
//
// A boundary callback must not panic and must not block: it runs on the
// single worker goroutine, between two tasks.
func WithBoundary(f func()) QueueOption {
	return func(q *Queue) { q.boundary = f }
}

// Open loads the queue from the store root, re-queuing interrupted tasks
// (FR-029): a task found pending or running is marked Resumed and runs
// again — the import pipeline is incremental by digest, so a re-run only
// transfers what is missing. No task is ever left orphaned.
func Open(storeRoot string, logger *slog.Logger, opts ...QueueOption) (*Queue, error) {
	dir := filepath.Join(storeRoot, tasksDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("tasks: creating %s: %w", dir, err)
	}
	q := &Queue{
		dir:     dir,
		logger:  logger,
		tasks:   map[string]*Task{},
		runners: map[string]Runner{},
		pending: make(chan string, pendingCapacity),
		Now:     time.Now,
	}
	for _, opt := range opts {
		opt(q)
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
	// Persist the resumed marker, oldest first, and hand the whole set to
	// the worker's backlog — never to the channel (B-019): the worker
	// only starts after Open returns, so a blocking send on a bounded
	// channel made a store with more active tasks than it holds wedge the
	// instance at startup. Retention could not save it either: FR-029
	// owns active tasks and they are deliberately never purged.
	sort.Slice(resumed, func(i, j int) bool {
		return q.tasks[resumed[i]].Created.Before(q.tasks[resumed[j]].Created)
	})
	for _, id := range resumed {
		q.persist(q.tasks[id])
		logger.LogAttrs(context.Background(), slog.LevelInfo, "task resumed after interruption",
			slog.String("task_id", id), slog.String("run_id", q.tasks[id].RunID))
	}
	q.resuming = resumed
	// Retention applies to what was just reloaded too: an instance that
	// accumulated history before the policy existed (or with a larger
	// keep) trims it on the next start, not only as new tasks arrive.
	q.purgeFinished()
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

// WithLayout carries the parameters of an OCI image layout operation
// (FR-051) onto the task, so a resumed run (FR-029) reads them back from
// the persisted file rather than from a request that is long gone.
func WithLayout(l *Layout) TaskOption {
	return func(t *Task) {
		cp := *l
		t.Layout = &cp
	}
}

// WithPrune states what this synchronization does about content the
// resolved Retriever no longer references (FR-045). Explicit at every
// call site on purpose: a removal that happens because nobody passed an
// option is a removal nobody decided.
func WithPrune(enabled bool) TaskOption {
	return func(t *Task) { t.Prune = enabled }
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
	// Reserve the worker slot BEFORE anything is persisted (2026-08
	// audit): the old order persisted first and only then discovered the
	// full channel, leaving an orphaned pending task — in the map, on
	// disk, and re-queued by the next Open (FR-029) although its creator
	// was told "queue is full". Refusing first leaves no trace. The
	// worker cannot observe the id before the map insert below: execute
	// takes q.mu, which this function holds until it returns.
	select {
	case q.pending <- t.ID:
	default:
		return nil, errors.New("tasks: queue is full")
	}
	q.tasks[t.ID] = t
	q.persist(t)
	// Amortized retention: one purge per creation keeps the finished
	// history bounded without a background sweeper.
	q.purgeFinished()
	return t, nil
}

// purgeFinished enforces the WithRetention policy: keep the keepFinished
// most recent finished tasks (by creation time — the listing order) and
// delete the rest, memory and files alike. Active tasks never count and
// are never touched. Callers hold q.mu (Open runs single-threaded before
// the queue is shared).
func (q *Queue) purgeFinished() {
	if q.keepFinished <= 0 {
		return
	}
	finished := make([]*Task, 0, len(q.tasks))
	for _, t := range q.tasks {
		if !t.Active() {
			finished = append(finished, t)
		}
	}
	if len(finished) <= q.keepFinished {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].Created.After(finished[j].Created) })
	for _, t := range finished[q.keepFinished:] {
		delete(q.tasks, t.ID)
		for _, path := range []string{
			filepath.Join(q.dir, t.ID+".json"),
			filepath.Join(q.dir, t.ID+".log"),
		} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				// A file that resists deletion is worth a line, not a
				// failure: the map entry is gone either way, and the next
				// purge retries the file.
				q.logger.LogAttrs(context.Background(), slog.LevelWarn, "purging task file",
					slog.String("task_id", t.ID), slog.String("file", path),
					slog.String("error", err.Error()))
			}
		}
	}
	q.logger.LogAttrs(context.Background(), slog.LevelDebug, "purged finished tasks",
		slog.Int("purged", len(finished)-q.keepFinished),
		slog.Int("kept", q.keepFinished))
}

// HasPending reports whether a task of taskType is already waiting to
// run — the scheduler's coalescence guard (2026-08 audit). The worker is
// serial, so once one sync cycle outlasts the interval, an unconditional
// Create per tick piles identical tasks onto the queue; a pending sync
// already reads the latest desired state when it finally runs, so a
// second one adds nothing. Running tasks deliberately do not count: the
// state THEY read may already be stale, and one queued follow-up is
// exactly what reconciles it.
func (q *Queue) HasPending(taskType string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.tasks {
		if t.Type == taskType && t.Status == StatusPending {
			return true
		}
	}
	return false
}

// Start runs the worker until ctx is canceled. A task interrupted by
// shutdown stays running on disk and resumes on the next Open.
//
// The FR-029 resume backlog is drained first and before the channel
// (B-019): those tasks were created before anything this process
// accepted, and running them first is the order Open used to obtain by
// filling the channel ahead of any caller.
func (q *Queue) Start(ctx context.Context) {
	q.worker.Add(1)
	go func() {
		defer q.worker.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			if id, ok := q.nextResuming(); ok {
				q.execute(ctx, id)
				continue
			}
			select {
			case <-ctx.Done():
				return
			case id := <-q.pending:
				q.execute(ctx, id)
			}
		}
	}()
}

// Wait blocks until the worker started by Start has returned. It answers
// immediately on a queue that was never started, and may be called more
// than once.
//
// It exists because "the task is done" and "the queue has let go of its
// files" were two different instants and only the first was observable
// (B-026). The gap is invisible on Unix, where a file can be removed
// while a handle is still open on it; on Windows it is the difference
// between an operator being able to eject the transport medium after a
// clean shutdown and being told the medium is in use (NFR-018, FR-093).
// Waiting for the worker is the only way a caller can know the run
// directory is quiescent.
func (q *Queue) Wait() { q.worker.Wait() }

// nextResuming pops the oldest task of the resume backlog (B-019).
func (q *Queue) nextResuming() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.resuming) == 0 {
		return "", false
	}
	id := q.resuming[0]
	// The consumed head is cleared so the backing array does not pin the
	// whole set of identifiers for as long as one is left to run.
	q.resuming[0] = ""
	q.resuming = q.resuming[1:]
	return id, true
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

	// The FR-056 flush point, registered before the task's own log handle
	// so that defers unwind in the order the requirement describes: the
	// task's records are written and its file closed, and only then is
	// the medium flushed to stable storage.
	defer q.atBoundary()
	taskLogger, closeLog := q.taskLogger(work)
	// The deferred call is the backstop for the paths that do not reach a
	// terminal state (a panic, a shutdown). Every path that PUBLISHES one
	// closes the handle itself, first — see finish (B-026). closeLog is
	// idempotent, so both can hold.
	defer closeLog()

	save := func() {
		q.mu.Lock()
		q.publish(t, work)
		q.mu.Unlock()
	}

	if runner == nil {
		q.finish(t, work, taskLogger, closeLog, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("no runner for task type %q", t.Type)))
		return
	}
	err := runProtected(ctx, runner, work, taskLogger, save)
	if errors.Is(err, context.Canceled) {
		// Graceful shutdown caught the task mid-run: leave it running on
		// disk — the next start resumes it (FR-029). Never mark it failed
		// for being interrupted.
		taskLogger.LogAttrs(context.Background(), slog.LevelInfo,
			"task interrupted by shutdown, will resume")
		closeLog()
		save()
		return
	}
	var te *taxonomy.Error
	if err != nil && !errors.As(err, &te) {
		te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	q.finish(t, work, taskLogger, closeLog, te)
}

// atBoundary runs the WithBoundary callback, behind the same kind of
// barrier the runner gets: a durability hook that panicked would take
// down a long-lived service over a flush.
func (q *Queue) atBoundary() {
	if q.boundary == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			q.logger.LogAttrs(context.Background(), slog.LevelError, "task boundary hook panicked",
				slog.String("panic", fmt.Sprint(r)))
		}
	}()
	q.boundary()
}

// runProtected invokes the runner behind a panic barrier (2026-08
// robustness audit). A runner panic — a third-party parser choking on a
// pathological manifest is enough — must fail the TASK, never the
// process: this queue runs inside a long-lived service, and no single
// operation may take the instance down with it. The failure must also
// be terminal: a task left active on disk is re-queued at the next
// start (FR-029), and a re-queued panic replays forever — a permanent
// crash loop with no operator escape. Converting the panic to the
// internal taxonomy code settles the task as failed (finish persists
// it), which keeps it out of the resume set; the stack goes to the task
// log, where the FR-090 correlation makes it findable.
func runProtected(ctx context.Context, runner Runner, work *Task, logger *slog.Logger, save func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.LogAttrs(context.Background(), slog.LevelError, "task runner panicked",
				slog.String("panic", fmt.Sprint(r)),
				slog.String("stack", string(debug.Stack())))
			err = taxonomy.New(taxonomy.CodeInternal, nil).
				WithCause(fmt.Errorf("task runner panicked: %v", r))
		}
	}()
	return runner(ctx, work, logger, save)
}

// clone deep-copies the task for the runner's exclusive use (items and
// reports are the runner-mutated parts; persisted errors are never
// mutated in place). Every runner-mutated slice belongs here: a report
// the runner fills but clone and publish ignore is silently lost.
func (t *Task) clone() *Task {
	cp := *t
	cp.Items = cloneItems(t.Items)
	cp.ChartDependencies = append([]ChartDependency(nil), t.ChartDependencies...)
	cp.Resolutions = append([]Resolution(nil), t.Resolutions...)
	return &cp
}

// publish copies the runner's progress into the canonical task and
// persists it. Callers hold q.mu.
//
// The item copy must be DEEP (B-016): the runner keeps mutating its
// clone's per-blob progress rows after this snapshot, and a shallow
// copy would leave the canonical task sharing those rows' memory with
// it — every later reader (Get, List, persist's marshalling) would
// race with the runner. cloneItems allocates fresh rows the canonical
// side alone owns; they are never written again after this call, so
// readers under q.mu can hand them out safely.
func (q *Queue) publish(dst, src *Task) {
	dst.Status = src.Status
	dst.Error = src.Error
	dst.Finished = src.Finished
	dst.Items = cloneItems(src.Items)
	dst.ChartDependencies = append([]ChartDependency(nil), src.ChartDependencies...)
	dst.Resolutions = append([]Resolution(nil), src.Resolutions...)
	q.persist(dst)
}

// finish settles the task's final status: failed on a task-level error or
// any failed item, done otherwise.
// The order of the three steps below is the whole of B-026, and it is not
// the order that reads most naturally.
//
// The terminal status is what every observer waits on — the UI poll, the
// CLI's --wait, the retention purge, a test's settle helper — so
// publishing it is a promise that the queue is finished with this task.
// Publishing it while the task's own log file is still open makes that
// promise false: on Unix nothing notices, because a file can be removed
// or renamed out from under an open handle; on Windows the purge cannot
// delete the log it was told to drop, and an instance that has just
// reported a clean shutdown still holds the transport medium open
// (NFR-018, FR-053, FR-093).
//
// So: write the last record, close the handle, and only then say it is
// done.
func (q *Queue) finish(t, work *Task, logger *slog.Logger, closeLog func(), te *taxonomy.Error) {
	q.mu.Lock()
	if te != nil {
		work.Error = FromTaxonomy(te)
	}
	agg := work.Aggregate()
	work.Status = StatusDone
	if te != nil || agg.Failed > 0 {
		work.Status = StatusFailed
	}
	work.Finished = q.Now().UTC()
	q.mu.Unlock()

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
	closeLog()

	q.mu.Lock()
	defer q.mu.Unlock()
	q.publish(t, work)
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
	tee := logging.Tee(base, logging.New(f, slog.LevelDebug)).
		With(slog.String("task_id", t.ID), slog.String("run_id", t.RunID))
	// Idempotent: execute closes the handle explicitly before it publishes
	// a terminal status and again from a deferred backstop, and a double
	// Close would otherwise report "file already closed" on the paths that
	// took both (B-026).
	var once sync.Once
	return tee, func() { once.Do(func() { _ = f.Close() }) }
}

// Get returns a task by id. The copy is deep on the runner-mutated
// slices (B-016): the caller reads it outside q.mu.
func (q *Queue) Get(id string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	cp.Items = cloneItems(t.Items)
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
		cp.Items = cloneItems(t.Items)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// ListPageSize is the server-side page size of the task listing — the
// same size as the /content listing (store.BrowsePageSize): FR-061 wants
// the screens and their API mirrors to paginate identically, and the two
// listings should not feel different either.
const ListPageSize = 25

// ListQuery is the filter set of the task listing. Its parameter names
// (q, status, type, page) are the shared UI/API contract (FR-061): both
// surfaces parse them with ParseListQuery, so parity holds by
// construction — the same pattern as store.ParseBrowseQuery.
type ListQuery struct {
	// Q filters by substring on the task reference (R-06 parity).
	Q string
	// Status keeps only tasks in this status.
	Status Status
	// Type keeps only tasks of this type (TypeUnitImport, TypeSync).
	Type string
	// Page is the 1-based page of ListPageSize entries.
	Page int
}

// ParseListQuery reads a ListQuery from URL parameters — the single
// parser behind the /tasks screen and /api/v1/tasks (FR-061).
func ParseListQuery(v url.Values) ListQuery {
	lq := ListQuery{Q: v.Get("q"), Status: Status(v.Get("status")), Type: v.Get("type"), Page: 1}
	if p, err := strconv.Atoi(v.Get("page")); err == nil && p > 1 {
		lq.Page = p
	}
	return lq
}

// Values renders the query back to URL parameters (pagination links).
func (lq ListQuery) Values() url.Values {
	v := url.Values{}
	if lq.Q != "" {
		v.Set("q", lq.Q)
	}
	if lq.Status != "" {
		v.Set("status", string(lq.Status))
	}
	if lq.Type != "" {
		v.Set("type", lq.Type)
	}
	if lq.Page > 1 {
		v.Set("page", strconv.Itoa(lq.Page))
	}
	return v
}

// HasFilter reports whether any filter narrows the listing — it separates
// the "no task yet" state from "no result for these filters" (UI-SPEC).
func (lq ListQuery) HasFilter() bool {
	return lq.Q != "" || lq.Status != "" || lq.Type != ""
}

// TaskPage is one page of the filtered task listing, newest first.
type TaskPage struct {
	Tasks      []*Task
	Total      int
	Page       int
	TotalPages int
}

// ListPage returns one page of tasks filtered by lq — the single
// implementation behind the /tasks screen and its API mirror (FR-061),
// shaped like store.Browse. An out-of-range page yields an empty window,
// never an error: the screen shows its no-result state and the API
// mirrors it.
func (q *Queue) ListPage(lq ListQuery) *TaskPage {
	matched := q.List(lq.Status, lq.Type, lq.Q)
	page := &TaskPage{Total: len(matched), Page: lq.Page}
	start, end, totalPages := paginate(len(matched), lq.Page, ListPageSize)
	page.TotalPages = totalPages
	page.Tasks = matched[start:end]
	return page
}

// paginate computes the half-open [start, end) window of the 1-based page
// over total entries, and the page count (at least 1). A local twin of
// the store package's helper: importing the store from here would point
// the dependency at one of this queue's own consumers.
func paginate(total, page, size int) (start, end, totalPages int) {
	totalPages = (total + size - 1) / size
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	start = (page - 1) * size
	if start > total {
		return total, total, totalPages
	}
	end = min(start+size, total)
	return start, end, totalPages
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

// OpenLog opens the task's raw log for streaming — the download endpoint
// copies from it instead of buffering the whole file (2026-08 audit: a
// verbose sync log is easily tens of megabytes, and the old ReadLog path
// held all of it in memory per download). The caller closes the reader.
// A task that never logged returns an os.ErrNotExist the caller maps to
// an empty download.
func (q *Queue) OpenLog(id string) (io.ReadCloser, error) {
	return os.Open(q.LogPath(id))
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
