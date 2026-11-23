// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/clock"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// Wiring of the operations track of milestone 7 (R-11, R-32): the metrics
// fed by a synchronization run and the clock verdicts. The packages that
// own the facts know nothing about each other; this file joins them.

// newClockMonitor builds the monitor of FR-096 and makes it feed the
// metric and the warn-and-record path. logger is read at call time: the
// instance logger is replaced by a tee onto the medium's log in mirror
// mode, and the record must reach both.
func newClockMonitor(cfg *config.Config, reg *metrics.Registry, logger func() *slog.Logger) *clock.Monitor {
	mon := clock.NewMonitor(time.Duration(cfg.Clock.SkewTolerance))
	mon.Observe(func(r *clock.Record) { clock.Report(context.Background(), logger(), r) })
	if reg != nil {
		reg.SetClock(func() []metrics.ClockReading {
			var out []metrics.ClockReading
			for _, r := range mon.Records() {
				out = append(out, metrics.ClockReading{Anchor: string(r.Anchor), Implausible: r.Implausible})
			}
			return out
		})
	}
	return mon
}

// observeMediumClock judges the clock against the medium a verification
// report describes (FR-096). A report without a manifest has no anchor,
// and a blocked one has none worth believing: the block may be the
// manifest's signature or trust failing, and a date read from such a
// manifest must neither raise a banner nor become the anchor later media
// are judged against.
func observeMediumClock(mon *clock.Monitor, rep *media.Report) {
	if mon == nil || rep == nil || rep.Media == nil || rep.Verdict == media.VerdictBlocked {
		return
	}
	mon.CheckMedium(rep.Media.MediaID, rep.Media.ResolvedAt)
}

// syncRunOf summarizes a finished synchronization task for the metrics: a
// run fails when the runner returned an error or left an item failed,
// which is also when the queue marks the task failed.
func syncRunOf(t *tasks.Task, runErr error, started, finished time.Time) metrics.SyncRun {
	ing := map[string]int{}
	for i := range t.Resolutions {
		r := &t.Resolutions[i]
		if r.Ingredient == "" {
			continue
		}
		switch r.Status {
		case "new":
			ing[metrics.IngredientNew]++
		case "outdated":
			ing[metrics.IngredientOutdated]++
		case "up-to-date":
			ing[metrics.IngredientUpToDate]++
		}
	}
	return metrics.SyncRun{
		Success:     runErr == nil && t.Aggregate().Failed == 0,
		Duration:    finished.Sub(started),
		Finished:    finished,
		Ingredients: ing,
	}
}
