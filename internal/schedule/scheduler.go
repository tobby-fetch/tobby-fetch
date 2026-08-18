// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package schedule

import (
	"context"
	"log/slog"
	"time"
)

// Trigger starts one reconciliation. It returns as soon as the work is
// enqueued: the scheduler's job is cadence, not execution — the task
// queue owns running, resuming, and reporting (FR-029).
type Trigger func(ctx context.Context) error

// Scheduler runs the periodic reconciliation of FR-013.
//
// It exists only in passthrough mode. FR-014 states that mirror-mode
// synchronization is triggered manually and SHALL NOT run unattended, so
// there is no scheduler to disable there — the caller does not build one.
// A flag would have been one boolean away from a mirror instance reaching
// across an air gap on a timer.
type Scheduler struct {
	interval *Interval
	trigger  Trigger
	logger   *slog.Logger
	// now and after are injected in tests; production uses the clock.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

// NewScheduler builds the loop over an interval and a trigger.
func NewScheduler(iv *Interval, t Trigger, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		interval: iv,
		trigger:  t,
		logger:   logger,
		now:      time.Now,
		after:    time.After,
	}
}

// Run drives the loop until ctx is canceled.
//
// The first reconciliation happens one interval after start, not at
// start: an instance restarting under a supervisor would otherwise turn a
// crash loop into a request storm against the peer zone. FR-014's manual
// trigger covers "reconcile now" for the operator who wants it.
//
// A zero interval parks the loop instead of ending it. The distinction
// matters for FR-013's "changeable without redeployment": an operator who
// sets the interval to 0 and then back to 10m gets a running loop again,
// with no restart, because the goroutine is still there waiting on the
// change channel.
//
// Shutdown (FR-093) is a plain ctx cancellation: the loop stops between
// cycles and never starts one it cannot finish. A cycle already running
// belongs to the task queue, which persists it as interrupted and resumes
// it on the next start rather than failing it.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		d := s.interval.Effective()
		changed := s.interval.Changed()

		var tick <-chan time.Time
		if d > 0 {
			tick = s.after(d)
		}
		s.logger.LogAttrs(ctx, slog.LevelDebug, "promotion cycle scheduled",
			slog.Duration("interval", d), slog.Bool("enabled", d > 0),
			slog.String("requirement", "FR-013"))

		select {
		case <-ctx.Done():
			s.logger.LogAttrs(context.Background(), slog.LevelInfo, "promotion scheduler stopped",
				slog.String("reason", "shutdown"), slog.String("requirement", "FR-093"))
			return
		case <-changed:
			// The interval moved while we were waiting: recompute rather
			// than serve out a delay chosen under the old value.
			continue
		case <-tick:
		}

		// Re-check the deadline before enqueuing: SIGTERM may have landed
		// between the timer firing and this line, and a cycle started
		// during shutdown is one the queue will have to mark interrupted a
		// moment later for nothing.
		if ctx.Err() != nil {
			return
		}
		if err := s.trigger(ctx); err != nil {
			// A failed enqueue does not stop the loop: the next interval
			// tries again. The reason a queue refuses work — it is full,
			// or the instance is shutting down — is transient by nature,
			// and a promotion service that gives up permanently on one
			// refusal has silently stopped promoting.
			s.logger.LogAttrs(ctx, slog.LevelWarn, "scheduled promotion cycle could not start",
				slog.String("error", err.Error()), slog.String("requirement", "FR-013"))
			continue
		}
		s.logger.LogAttrs(ctx, slog.LevelInfo, "scheduled promotion cycle started",
			slog.Duration("interval", d), slog.String("requirement", "FR-013"))
	}
}
