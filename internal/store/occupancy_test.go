// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

// Store occupancy (FR-045 amendment, R-33).
//
// The measurement is the real blob tree of a real store: the requirement
// is about a volume filling up, and a mocked size would assert nothing
// about the thing that fills.

// fill writes n blobs of size bytes into repo, so the store's footprint
// grows by a knowable amount.
func fill(t *testing.T, s *Store, repo string, n, size int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		payload := bytes.Repeat([]byte{byte('a' + i)}, size)
		d := digest.FromBytes(payload)
		if err := s.WriteBlob(ctx, repo, d, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOccupancyCrossesInBothDirections is the R-33 acceptance: going over
// the threshold raises the condition and moves the observed sample, and
// coming back under it retracts both. A warning that appears and never
// clears is a warning operators learn to ignore.
func TestOccupancyCrossesInBothDirections(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	const threshold = 4096
	m := NewOccupancyMonitor(st, threshold, logger)
	var mu sync.Mutex
	var samples []Occupancy
	m.Observe(func(o Occupancy) {
		mu.Lock()
		defer mu.Unlock()
		samples = append(samples, o)
	})

	if o := m.Refresh(ctx); o.Exceeded {
		t.Fatalf("an empty store already exceeds %d bytes: %+v", threshold, o)
	}

	fill(t, st, "big/repo", 6, 2048)
	over := m.Refresh(ctx)
	if !over.Exceeded {
		t.Fatalf("a %d-byte store did not exceed the %d-byte threshold: %+v", over.Bytes, threshold, over)
	}
	if m.Current() != over {
		t.Errorf("Current() = %+v, want the sample just taken %+v", m.Current(), over)
	}
	if !strings.Contains(buf.String(), "store occupancy threshold exceeded") {
		t.Errorf("crossing up was silent:\n%s", buf.String())
	}

	// Take the content away: the blobs are unreferenced, so a sweep past
	// the grace reclaims them — the same mechanism a prune ends on.
	old := sweepGrace
	sweepGrace = -time.Second
	defer func() { sweepGrace = old }()
	if err := st.Sweep(ctx, logger); err != nil {
		t.Fatal(err)
	}

	under := m.Refresh(ctx)
	if under.Exceeded {
		t.Errorf("the store stayed over the threshold after its content went: %+v", under)
	}
	if !strings.Contains(buf.String(), "store occupancy threshold cleared") {
		t.Errorf("crossing back down was silent:\n%s", buf.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(samples) < 3 {
		t.Fatalf("observed %d samples, want one per Refresh", len(samples))
	}
	first, last := samples[0], samples[len(samples)-1]
	if first.Exceeded || !samples[1].Exceeded || last.Exceeded {
		t.Errorf("observed sequence did not go under → over → under: %+v", samples)
	}
	for _, s := range samples {
		if s.Threshold != threshold || !s.Monitored() {
			t.Errorf("sample %+v does not carry the configured threshold", s)
		}
	}
}

// TestOccupancyUnconfiguredIsReportedAsUnconfigured: no threshold means
// no measurement and no warning — and it must not read as "within
// limits", which is the opposite statement.
func TestOccupancyUnconfiguredIsReportedAsUnconfigured(t *testing.T) {
	st := openMetaTestStore(t)
	m := NewOccupancyMonitor(st, 0, slog.New(slog.DiscardHandler))
	var observed int
	m.Observe(func(Occupancy) { observed++ })

	fill(t, st, "big/repo", 4, 2048)
	o := m.Refresh(context.Background())
	if o.Monitored() || o.Exceeded || o.Bytes != 0 {
		t.Errorf("unconfigured occupancy = %+v, want an unmonitored zero", o)
	}
	if observed != 0 {
		t.Errorf("an unconfigured monitor sampled %d times, want none", observed)
	}

	// Run returns at once rather than walking the blob tree forever.
	done := make(chan struct{})
	go func() { m.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run did not return on an unconfigured monitor")
	}
}

// TestOccupancyCurrentIsSafeUnderConcurrentRefresh: the UI shell reads the
// latest sample on every request while the loop writes it. The race
// detector is the assertion.
func TestOccupancyCurrentIsSafeUnderConcurrentRefresh(t *testing.T) {
	st := openMetaTestStore(t)
	fill(t, st, "some/repo", 2, 512)
	m := NewOccupancyMonitor(st, 1024, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 50 {
			m.Refresh(ctx)
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			_ = m.Current()
		}
	}()
	wg.Wait()
}

// TestPhysicalBytesMatchesTheDashboardFigure: the banner and the store
// tile must never disagree about how full the store is, so both read the
// same measurement.
func TestPhysicalBytesMatchesTheDashboardFigure(t *testing.T) {
	st := openMetaTestStore(t)
	fill(t, st, "some/repo", 3, 1024)

	direct, err := st.PhysicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	counts, err := st.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if direct != counts.PhysicalBytes {
		t.Errorf("PhysicalBytes = %d, dashboard tile = %d: two measurements of one store",
			direct, counts.PhysicalBytes)
	}
	if direct == 0 {
		t.Error("a store holding blobs measured zero bytes")
	}
}
