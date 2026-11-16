// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// When /files/ re-resolves what it serves (FR-047, B-058).
//
// The answer used to be "at startup and after a synchronization", which
// left every other way content reaches the store unserved until a
// restart: a unit import, a media or layout import, a FileSet packed from
// the screen or the API, a push through /v2/. And on the destination side
// of a physical transfer the startup extraction wrote into the medium's
// store from blobs no verification had checked yet (FR-054).
//
// The refresher is the one place that decides, and it answers from the
// write side wherever the instance owns the write:
//
//   - after every task that can bring content into the store (sync, unit
//     import, media import, layout import), inside the task, so the task
//     settles with its FileSets already served;
//   - after a successful pack, before the screen or the API answers;
//   - after a manifest write or delete through /v2/ on the repository of
//     a declared FileSet, in the background, so a client's push is never
//     held up by an extraction — and a newer version pushed over a
//     served one is picked up, which no read-side trigger could notice;
//   - on a miss: a GET for a DECLARED FileSet that is not served waits for
//     the resolution in flight, or starts one, before answering — every
//     concurrent miss shares that one resolution (B-061). This is the backstop for writes the instance
//     does not see (another process writing the store directory) and for
//     the instant between a write and its background refresh. It is
//     bounded twice: undeclared names never trigger anything, and a
//     declared name triggers at most once per fileSetMissCooldown, so a
//     client polling a FileSet whose content has not arrived costs one
//     resolution per cooldown, not one per request.
//
// On a guarded destination instance every one of those is a no-op while
// the media gate is closed: nothing is written under the medium's cache
// before a verification has cleared it. The gate's transition to open
// triggers the first extraction, and that first extraction starts from an
// empty cache — the tree the medium carried was extracted elsewhere, and
// is not what the verification checked (fileserve.Server.Discard).
type fileSetRefresher struct {
	base   context.Context
	fsrv   *fileserve.Server
	st     *store.Store
	cfg    *config.Config
	logger *slog.Logger
	gate   *mediagate.Gate

	// declared and repos are fixed at startup: the files.filesets names,
	// and the relocated repositories they resolve in.
	declared map[string]bool
	repos    map[string]bool

	// mu serializes refreshes; deferred (under mu) makes the "deferred
	// behind the gate" line appear once, not once per trigger.
	mu       sync.Mutex
	deferred bool
	// untrusted says the cache was not built by THIS instance from blobs
	// it verified: true from startup on a guarded instance, and again
	// each time the gate closes. Atomic because the gate's callback sets
	// it and must not wait for a refresh in progress.
	untrusted atomic.Bool

	// kmu guards the background refresh, the flight and the miss
	// cooldowns.
	kmu    sync.Mutex
	queued bool
	closed bool
	wg     sync.WaitGroup
	misses map[string]time.Time
	now    func() time.Time
	// flight is closed when every refresh begun while it was open has
	// finished; nil when none is running. A miss waits on it rather than
	// answering a 404 that the refresh under way is about to make wrong
	// (B-061). pending counts the refreshes it covers.
	flight  chan struct{}
	pending int
}

// fileSetMissWait bounds how long a request for a declared FileSet waits
// for a resolution in flight before it is answered with what is there.
const fileSetMissWait = 30 * time.Second

// fileSetMissCooldown is the shortest interval between two resolutions
// triggered by misses on the same declared FileSet.
const fileSetMissCooldown = 10 * time.Second

// newFileSetRefresher wires the refresher. ctx is the instance's
// lifetime: background refreshes and miss resolutions run under it,
// never under a client's request, so a client hanging up cannot abort an
// extraction other clients are waiting on.
func newFileSetRefresher(ctx context.Context, fsrv *fileserve.Server, st *store.Store, cfg *config.Config,
	gate *mediagate.Gate, logger *slog.Logger) *fileSetRefresher {
	r := &fileSetRefresher{
		base: ctx, fsrv: fsrv, st: st, cfg: cfg, logger: logger, gate: gate,
		declared: map[string]bool{}, repos: map[string]bool{},
		misses: map[string]time.Time{}, now: time.Now,
	}
	for _, f := range cfg.Files.FileSets {
		r.declared[f.Name] = true
		if repo, err := relocate.PathWithBase(cfg.Storage.BasePrefix, f.Ref); err == nil {
			r.repos[repo] = true
		}
	}
	r.untrusted.Store(gate.Guarded())
	gate.SetTransition(r.onTransition)
	return r
}

// begin registers one refresh in the current flight.
func (r *fileSetRefresher) begin() {
	r.kmu.Lock()
	defer r.kmu.Unlock()
	r.beginLocked()
}

// beginLocked registers one refresh in the current flight and returns
// it. Callers hold kmu.
func (r *fileSetRefresher) beginLocked() chan struct{} {
	if r.flight == nil {
		r.flight = make(chan struct{})
	}
	r.pending++
	return r.flight
}

// end closes the flight once its last refresh is over.
func (r *fileSetRefresher) end() {
	r.kmu.Lock()
	defer r.kmu.Unlock()
	r.pending--
	if r.pending == 0 {
		close(r.flight)
		r.flight = nil
	}
}

// Refresh re-resolves the declared FileSets and syncs the server. It is
// safe to call from any goroutine; calls are serialized.
func (r *fileSetRefresher) Refresh(ctx context.Context) {
	r.begin()
	defer r.end()
	r.refresh(ctx)
}

// refresh is Refresh without the flight bookkeeping.
func (r *fileSetRefresher) refresh(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.gate.Serving() {
		// FR-054: verification precedes any local write. The extraction
		// happens on the gate's transition to open (onTransition).
		if !r.deferred {
			r.deferred = true
			r.logger.LogAttrs(ctx, slog.LevelInfo, "fileset extraction deferred until the medium is verified",
				slog.String("requirement", "FR-054"))
		}
		return
	}
	if r.untrusted.Load() {
		if err := r.fsrv.Discard(); err != nil {
			// Never fall through to a Sync that would trust what is left:
			// the next trigger tries again.
			r.logger.LogAttrs(ctx, slog.LevelWarn, "the carried fileset cache could not be discarded: nothing is served from it",
				slog.String("error", err.Error()),
				slog.String("action", "the cache is discarded again on the next refresh"),
				slog.String("requirement", "FR-054"))
			return
		}
		r.untrusted.Store(false)
	}
	syncFileSets(ctx, r.fsrv, r.st, r.cfg, r.logger)
}

// AfterReset follows a store reset (FR-046, B-059): the reset removes the
// content and keeps meta/, where the extracted cache lives, so the cache
// is discarded outright — the content it was extracted from is gone, and
// nothing about it is worth keeping, gate open or not — before the
// declared FileSets are resolved again against the emptied store.
func (r *fileSetRefresher) AfterReset(ctx context.Context) {
	r.begin()
	defer r.end()
	r.mu.Lock()
	err := r.fsrv.Discard()
	r.mu.Unlock()
	if err != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "the fileset cache was not fully discarded after the store reset",
			slog.String("error", err.Error()),
			slog.String("action", "nothing is served from it; the leftovers are purged on the next refresh"),
			slog.String("requirement", "FR-046"))
	}
	r.refresh(ctx)
}

// onTransition is the media gate's callback (mediagate.Gate.SetTransition).
// It never blocks: closing only marks the cache untrusted, opening
// schedules the extraction in the background.
func (r *fileSetRefresher) onTransition(serving bool) {
	if !serving {
		r.untrusted.Store(true)
		return
	}
	r.kick()
}

// kick schedules one background refresh. Triggers arriving while one is
// already queued coalesce into it; a trigger arriving while one is
// running queues exactly one more, so the last write is always followed
// by a refresh that sees it.
func (r *fileSetRefresher) kick() {
	r.kmu.Lock()
	if r.closed || r.queued {
		r.kmu.Unlock()
		return
	}
	r.queued = true
	r.wg.Add(1)
	r.kmu.Unlock()
	// Registered in the flight BEFORE the goroutine starts, so a request
	// arriving right after the trigger already finds something to wait on.
	r.begin()
	go func() {
		defer r.wg.Done()
		defer r.end()
		r.kmu.Lock()
		r.queued = false
		r.kmu.Unlock()
		r.refresh(r.base)
	}()
}

// Close waits for a background refresh in flight and refuses new ones.
// Called before the server's own Close, which releases the cache handles
// (B-024): a refresh finishing after that would open them again.
func (r *fileSetRefresher) Close() {
	r.kmu.Lock()
	r.closed = true
	r.kmu.Unlock()
	r.wg.Wait()
}

// wrapTasks decorates the registered runners of the given task types so
// that each task ends with a refresh, whatever its outcome — a task that
// failed half-way may still have landed a FileSet.
func (r *fileSetRefresher) wrapTasks(q *tasks.Queue, types ...string) {
	for _, tt := range types {
		run := q.Runner(tt)
		if run == nil {
			continue
		}
		q.Register(tt, func(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
			err := run(ctx, t, logger, save)
			r.Refresh(ctx)
			return err
		})
	}
}

// Registry wraps the /v2/ handler: a successful manifest write or delete
// on the repository of a declared FileSet schedules a refresh.
func (r *fileSetRefresher) Registry(next http.Handler) http.Handler {
	if len(r.repos) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		repo, ok := manifestWrite(req)
		if !ok || !r.repos[repo] {
			next.ServeHTTP(w, req)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, req)
		if rec.status >= 200 && rec.status < 300 {
			r.kick()
		}
	})
}

// manifestWrite returns the repository of a PUT or DELETE on
// /v2/<repo>/manifests/<reference>.
func manifestWrite(req *http.Request) (string, bool) {
	if req.Method != http.MethodPut && req.Method != http.MethodDelete {
		return "", false
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/v2/")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, "/manifests/")
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// Files wraps the /files/ handler with the bounded resolution of a miss.
// It sits behind the media gate and the authentication: a refused
// request reaches none of this.
func (r *fileSetRefresher) Files(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			rest := strings.TrimPrefix(req.URL.Path, fileserve.RoutePrefix)
			name, _, _ := strings.Cut(rest, "/")
			if r.declared[name] && !r.fsrv.Serves(name) {
				r.awaitResolution(req.Context(), name)
			}
		}
		next.ServeHTTP(w, req)
	})
}

// awaitResolution makes a miss wait for the resolution that can answer
// it: the one in flight, or — cooldown permitting — one it starts. Every
// concurrent miss joins the same flight, so a burst costs one resolution
// and none of its requests answers a transient 404 (B-061). The wait is
// bounded by fileSetMissWait and by the client hanging up; the resolution
// itself runs under the instance's lifetime, never the request's.
func (r *fileSetRefresher) awaitResolution(ctx context.Context, name string) {
	// Joining, or deciding to start, is one step under kmu: a miss that
	// read "nothing in flight" and lost the cooldown to a neighbour that
	// had not registered its flight yet would answer the very 404 this
	// exists to prevent.
	r.kmu.Lock()
	flight := r.flight
	start := false
	if flight == nil && !r.closed && r.allowMissLocked(name) {
		flight, start = r.beginLocked(), true
		r.wg.Add(1)
	}
	r.kmu.Unlock()
	if flight == nil {
		return
	}
	if start {
		go func() {
			defer r.wg.Done()
			defer r.end()
			r.refresh(r.base)
		}()
	}
	timer := time.NewTimer(fileSetMissWait)
	defer timer.Stop()
	select {
	case <-flight:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// allowMissLocked reports whether a miss on name may start a resolution
// now, and records it when it may. Callers hold kmu.
func (r *fileSetRefresher) allowMissLocked(name string) bool {
	now := r.now()
	if last, ok := r.misses[name]; ok && now.Sub(last) < fileSetMissCooldown {
		return false
	}
	r.misses[name] = now
	return true
}

// statusRecorder remembers the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection beneath.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
