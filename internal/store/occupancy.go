// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Store occupancy (FR-045 amendment, R-33).
//
// A passthrough instance reconciles unattended for months. Nothing in the
// product bounds what its transit store accumulates — prune is opt-in
// precisely because shrinking is never implied by a refresh — so the one
// thing that must not happen is the volume filling up silently. The
// threshold is the operator's statement of "past here, tell me", and it is
// reported on three channels at once: a persistent banner on every UI
// page, the same fact on the API, and a metric.
//
// Crossing is reported in BOTH directions. A warning that appears and
// never retracts is a warning operators learn to ignore; going back under
// the threshold — after a prune, after a removal — clears the banner and
// moves the metric back, and both crossings are logged.

// OccupancySampleEvery paces the monitor. Sampling means summing the whole
// blob tree, so it is deliberately not frequent: the question "is the
// volume filling up" is answered in minutes, not in seconds, and a tighter
// loop would spend the instance's I/O budget on watching itself. A
// variable so the tests do not have to wait for it.
var OccupancySampleEvery = 5 * time.Minute

// Occupancy is one sample of the store's footprint against the configured
// threshold.
type Occupancy struct {
	// Bytes is the on-disk size of the blob tree — real, deduplicated
	// bytes, the same figure the dashboard tile shows.
	Bytes int64
	// Threshold is the configured limit; zero means none is set.
	Threshold int64
	// Exceeded is the fact the banner and the metric carry.
	Exceeded bool
	// SampledAt dates the measurement, so a surface can say how fresh it
	// is rather than imply it is live.
	SampledAt time.Time
}

// Monitored reports whether a threshold is configured at all. An unset
// threshold is reported as unset everywhere, never as a satisfied one:
// "nothing configured" and "within limits" are opposite statements.
func (o Occupancy) Monitored() bool { return o.Threshold > 0 }

// OccupancyMonitor samples the store on a cadence and publishes the
// latest sample to whoever asks — the UI shell on every request, the API,
// and the metric registry through Observe.
type OccupancyMonitor struct {
	store     *Store
	threshold int64
	logger    *slog.Logger
	observe   func(Occupancy)

	mu      sync.RWMutex
	current Occupancy
}

// NewOccupancyMonitor builds the monitor. A zero threshold builds a
// monitor that samples nothing: it still answers Current, with Monitored
// false, so every surface has one thing to ask rather than a nil check.
func NewOccupancyMonitor(s *Store, threshold int64, logger *slog.Logger) *OccupancyMonitor {
	return &OccupancyMonitor{
		store:     s,
		threshold: threshold,
		logger:    logger,
		current:   Occupancy{Threshold: threshold},
	}
}

// Observe installs the sample hook — the metric gauges in production.
// It is called on every sample, not only on a crossing, so a gauge never
// has to be inferred from the absence of an event. Install it before Run.
func (m *OccupancyMonitor) Observe(fn func(Occupancy)) { m.observe = fn }

// Current returns the latest sample. Safe for concurrent use: the UI shell
// reads it on every request while the loop writes it.
func (m *OccupancyMonitor) Current() Occupancy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Refresh takes one sample now and publishes it. Called by the loop, and
// directly after anything that moves the footprint by a lot — a prune, an
// import — so that going back under the threshold retracts the warning
// promptly instead of at the next tick.
//
// A read failure leaves the previous sample in place rather than
// publishing a zero: an unreadable store is not an empty one, and a
// warning that clears because the measurement failed is worse than no
// warning at all.
func (m *OccupancyMonitor) Refresh(ctx context.Context) Occupancy {
	if m.threshold <= 0 {
		return m.Current()
	}
	bytes, err := m.store.PhysicalBytes()
	if err != nil {
		m.logger.LogAttrs(ctx, slog.LevelWarn, "store occupancy could not be sampled",
			slog.String("error", err.Error()),
			slog.String("requirement", "FR-045"))
		return m.Current()
	}
	sample := Occupancy{
		Bytes:     bytes,
		Threshold: m.threshold,
		Exceeded:  bytes > m.threshold,
		SampledAt: time.Now().UTC(),
	}

	m.mu.Lock()
	crossed := m.current.Exceeded != sample.Exceeded
	m.current = sample
	m.mu.Unlock()

	if crossed {
		level := slog.LevelWarn
		state := "exceeded"
		if !sample.Exceeded {
			level = slog.LevelInfo
			state = "cleared"
		}
		m.logger.LogAttrs(ctx, level, "store occupancy threshold "+state,
			slog.Int64("bytes", sample.Bytes),
			slog.Int64("threshold_bytes", sample.Threshold),
			slog.String("requirement", "FR-045"))
	}
	if m.observe != nil {
		m.observe(sample)
	}
	return sample
}

// Run samples now, then every OccupancySampleEvery until ctx is canceled.
// An unconfigured threshold returns immediately: there is nothing to watch
// and no reason to walk the blob tree for it.
func (m *OccupancyMonitor) Run(ctx context.Context) {
	if m.threshold <= 0 {
		return
	}
	// The first sample is immediate, unlike the reconciliation scheduler:
	// an instance restarting on an already-full volume must say so at
	// once, not one interval later.
	m.Refresh(ctx)
	ticker := time.NewTicker(OccupancySampleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Refresh(ctx)
		}
	}
}
