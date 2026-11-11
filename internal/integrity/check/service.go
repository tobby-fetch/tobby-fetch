// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package check

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// publishEvery paces the progress the runner publishes: a task publish
// persists the task file, and a store of a hundred thousand blobs must not
// rewrite it a hundred thousand times.
const publishEvery = 250 * time.Millisecond

// Service runs on-demand verifications as tracked tasks (FR-057, FR-062):
// the one place the API, the interface and the queue share, so the "one at
// a time" rule, the report and the audit trail cannot drift between
// surfaces (FR-061).
type Service struct {
	st      *store.Store
	queue   *tasks.Queue
	trust   media.Trust
	observe func(*integrity.Report)

	// cancelMu guards cancel, the way to stop the verification in flight.
	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// walkGate, when set, holds a verification before its walk until it is
	// closed or the verification is cancelled. A test seam: it is how the
	// tests keep a verification running while they prove what happens
	// beside it.
	walkGate <-chan struct{}

	// startMu makes "is one running?" and "create one" a single step: two
	// requests arriving together must not both pass the first.
	startMu sync.Mutex
}

// NewService builds the service over a store and its task queue. trust is
// THIS instance's trust policy; observe, when non-nil, hears every
// completed report (the metrics hook of FR-091).
func NewService(st *store.Store, queue *tasks.Queue, trust media.Trust, observe func(*integrity.Report)) *Service {
	return &Service{st: st, queue: queue, trust: trust, observe: observe}
}

// Register installs the runner for tasks.TypeStoreVerify on its OWN lane of
// the queue, so a verification of hours neither waits for a synchronization
// nor holds one up. Call before the queue starts.
func (s *Service) Register() { s.queue.RegisterLane(tasks.TypeStoreVerify, s.Runner()) }

// Start enqueues one verification and returns its task. A verification
// already pending or running is refused with TBY-INT-004 naming it: a
// second walk of the same disk would halve both and answer nothing new.
func (s *Service) Start(actor, origin string) (*tasks.Task, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if id, running := s.queue.ActiveOfType(tasks.TypeStoreVerify); running {
		return nil, taxonomy.New(taxonomy.CodeIntegrityRunning, taxonomy.Params{"task": id})
	}
	items := make([]tasks.Item, 0, len(integrity.Stages()))
	for _, st := range integrity.Stages() {
		items = append(items, tasks.Item{Name: string(st), Status: tasks.StatusPending})
	}
	t, err := s.queue.Create(tasks.TypeStoreVerify, s.st.Root(), actor, items, tasks.WithOrigin(origin))
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	// Create hands back the queue's own task, which the worker is free to
	// mutate from the moment it is queued: callers get a snapshot.
	if snap, ok := s.queue.Get(t.ID); ok {
		return snap, nil
	}
	return t, nil
}

// Cancel stops the verification in flight, if any, and reports whether
// there was one. The task ends failed with TBY-INT-006; the store, which a
// verification only reads, is untouched.
func (s *Service) Cancel() bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.cancel == nil {
		return false
	}
	s.cancel()
	return true
}

// Latest returns the most recent verification task, finished or not, and
// whether there is one.
func (s *Service) Latest() (*tasks.Task, bool) {
	all := s.queue.List("", tasks.TypeStoreVerify, "")
	if len(all) == 0 {
		return nil, false
	}
	return all[0], true
}

// Runner returns the task runner of tasks.TypeStoreVerify.
func (s *Service) Runner() tasks.Runner {
	return func(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
		// The verification is cancellable by an operator, apart from the
		// instance shutting down: its own context, whose cancel the
		// service keeps for as long as it runs.
		runCtx, cancel := context.WithCancel(ctx)
		s.cancelMu.Lock()
		s.cancel = cancel
		s.cancelMu.Unlock()
		defer func() {
			s.cancelMu.Lock()
			s.cancel = nil
			s.cancelMu.Unlock()
			cancel()
		}()
		started := time.Now().UTC()
		var lastPublish time.Time
		current := integrity.Stage("")

		progress := func(p integrity.Progress) {
			// A stage change moves the items (cheap, and worth a publish);
			// the counters within a stage are paced.
			changed := p.Stage != current
			if changed {
				current = p.Stage
				stageItems(t, p.Stage)
			}
			if !changed && time.Since(lastPublish) < publishEvery {
				return
			}
			lastPublish = time.Now()
			snap := p
			t.Verification = &integrity.Report{
				RunID: t.RunID, StartedAt: started, Progress: &snap,
				Store: integrity.Store{Root: s.st.Root()},
			}
			save()
		}

		if s.walkGate != nil {
			select {
			case <-s.walkGate:
			case <-runCtx.Done():
			}
		}
		rep, err := Verify(runCtx, s.st, Options{
			Trust: s.trust, RunID: t.RunID, Progress: progress, Logger: logger,
		})
		if err != nil {
			// Interrupted by shutdown (the queue leaves the task running
			// and resumes it, FR-029) or failed outright: either way the
			// verification did not reach a verdict, and that is audited
			// with the outcome it had — failure — never left unrecorded.
			s.audit(ctx, logger, t, audit.OutcomeFailure)
			if ctx.Err() == nil && errors.Is(err, context.Canceled) {
				// An operator cancelled it (the instance is not shutting
				// down): the task ends, it is not resumed.
				return taxonomy.New(taxonomy.CodeIntegrityCancelled, taxonomy.Params{"task": t.ID})
			}
			return err
		}
		rep.StartedAt = started
		settleItems(t, rep)
		t.Verification = rep
		save()

		if s.observe != nil {
			s.observe(rep)
		}
		outcome := audit.OutcomeSuccess
		if rep.Verdict == integrity.VerdictDefects {
			outcome = audit.OutcomeFailure
		}
		s.audit(ctx, logger, t, outcome)
		// A store with defects is a failed verification: the task carries
		// the principal defect, which is what gives it its exit class
		// (FR-066) on every surface.
		if p := rep.Principal(); p != nil {
			return p
		}
		return nil
	}
}

// stageItems moves the per-stage items to the stage the walk has reached:
// the ones before it are done, this one is running.
func stageItems(t *tasks.Task, at integrity.Stage) {
	reached := false
	for i := range t.Items {
		switch {
		case t.Items[i].Name == string(at):
			t.Items[i].Status, reached = tasks.StatusRunning, true
		case !reached:
			t.Items[i].Status = tasks.StatusDone
		}
	}
}

// settleItems closes the per-stage items from the finished report: a stage
// that found a blocking defect is failed and carries the first one.
func settleItems(t *tasks.Task, rep *integrity.Report) {
	stageOf := map[taxonomy.Code]integrity.Stage{
		taxonomy.CodeIntegrityBlobDigest: integrity.StageBlobs,
		taxonomy.CodeIntegrityIncomplete: integrity.StageManifests,
		taxonomy.CodeIntegrityUnreadable: integrity.StageManifests,
		taxonomy.CodeSignature:           integrity.StageSignatures,
	}
	first := map[integrity.Stage]*integrity.Defect{}
	for i := range rep.Defects {
		d := &rep.Defects[i]
		if st, ok := stageOf[d.Code]; ok && first[st] == nil {
			first[st] = d
		}
	}
	for i := range t.Items {
		item := &t.Items[i]
		item.Status, item.Error = tasks.StatusDone, nil
		if d := first[integrity.Stage(item.Name)]; d != nil {
			item.Status = tasks.StatusFailed
			item.Error = tasks.FromTaxonomy(d.Error())
		}
	}
}

// audit writes the FR-094 store.verify event for a verification that ended,
// whatever its outcome. It uses a context that outlives a cancelled one:
// the event of an interrupted verification is exactly the one written while
// the instance is shutting down.
func (s *Service) audit(ctx context.Context, logger *slog.Logger, t *tasks.Task, outcome string) {
	origin := t.Origin
	if origin == "" {
		origin = audit.OriginLocal
	}
	audit.Log(context.WithoutCancel(ctx), logger, &audit.Event{
		Actor: t.Actor, Action: audit.ActionIntegrityVerify,
		Target: s.st.Root(), Outcome: outcome, Origin: origin,
	})
}
