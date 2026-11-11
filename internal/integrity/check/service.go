// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package check

import (
	"context"
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

// Register installs the runner for tasks.TypeStoreVerify. Call before the
// queue starts.
func (s *Service) Register() { s.queue.Register(tasks.TypeStoreVerify, s.Runner()) }

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
	return t, nil
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

		rep, err := Verify(ctx, s.st, Options{
			Trust: s.trust, RunID: t.RunID, Progress: progress, Logger: logger,
		})
		if err != nil {
			// Interrupted by shutdown: the queue leaves the task running
			// and resumes it (FR-029). Anything else is an operational
			// failure of the walk itself.
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
		origin := t.Origin
		if origin == "" {
			origin = audit.OriginLocal
		}
		audit.Log(ctx, logger, &audit.Event{
			Actor: t.Actor, Action: audit.ActionIntegrityVerify,
			Target: s.st.Root(), Outcome: outcome, Origin: origin,
		})
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
