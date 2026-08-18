// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package schedule_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestIntervalFallsBackToTheConfiguredValue: with no override, the
// configuration layers decide (FR-003).
func TestIntervalFallsBackToTheConfiguredValue(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if iv.Effective() != 15*time.Minute || iv.Overridden() {
		t.Errorf("effective=%s overridden=%v, want 15m and false", iv.Effective(), iv.Overridden())
	}
	if !iv.Persistent() {
		t.Error("an instance with a state directory can persist an override")
	}
}

// TestIntervalOverrideSurvivesRestart is the heart of FR-013's "changeable
// without redeployment": a value set at runtime must still be in force
// after the process dies, or the operator has not changed anything, they
// have postponed a surprise.
func TestIntervalOverrideSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	iv, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := iv.Set(45*time.Minute, "alexis", time.Now()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if iv.Effective() != 45*time.Minute || !iv.Overridden() {
		t.Fatalf("after Set: effective=%s overridden=%v", iv.Effective(), iv.Overridden())
	}
	// The configured value is still reported, unchanged: an operator must
	// be able to tell what the instance is doing from what its file says.
	if iv.Configured() != 15*time.Minute {
		t.Errorf("configured = %s, want the untouched 15m", iv.Configured())
	}

	restarted, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Effective() != 45*time.Minute || !restarted.Overridden() {
		t.Errorf("after restart: effective=%s overridden=%v, want 45m and true",
			restarted.Effective(), restarted.Overridden())
	}

	// Clearing returns to the configured value, and that too persists.
	if err := restarted.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	again, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if again.Effective() != 15*time.Minute || again.Overridden() {
		t.Errorf("after Clear and restart: effective=%s overridden=%v", again.Effective(), again.Overridden())
	}
	// Clearing an absent override is a state, not a transition.
	if err := again.Clear(); err != nil {
		t.Errorf("clearing an absent override: %v", err)
	}
}

// TestIntervalZeroStopsTheLoopWithoutClearingIt: zero is a legitimate
// override meaning "stop reconciling", and it must not read as "no
// override" — the two restore different values.
func TestIntervalZeroStopsTheLoopWithoutClearingIt(t *testing.T) {
	dir := t.TempDir()
	iv, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := iv.Set(0, "alexis", time.Now()); err != nil {
		t.Fatalf("Set(0): %v", err)
	}
	restarted, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Effective() != 0 || !restarted.Overridden() {
		t.Errorf("after restart: effective=%s overridden=%v, want 0 and true",
			restarted.Effective(), restarted.Overridden())
	}
}

// TestIntervalRefusals: the floor bounds the live control, and an
// instance with nowhere to persist says so instead of accepting a change
// that would evaporate.
func TestIntervalRefusals(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := iv.Set(time.Second, "alexis", time.Now()); !errors.Is(err, schedule.ErrTooShort) {
		t.Errorf("Set(1s) = %v, want ErrTooShort", err)
	}
	if iv.Effective() != time.Hour {
		t.Errorf("a refused change took effect: %s", iv.Effective())
	}

	stateless, err := schedule.Open("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stateless.Persistent() {
		t.Error("an instance without a state directory cannot persist an override")
	}
	if err := stateless.Set(2*time.Hour, "alexis", time.Now()); !errors.Is(err, schedule.ErrNoStateDir) {
		t.Errorf("Set without a state directory = %v, want ErrNoStateDir", err)
	}
	if err := stateless.Clear(); !errors.Is(err, schedule.ErrNoStateDir) {
		t.Errorf("Clear without a state directory = %v, want ErrNoStateDir", err)
	}
}

// TestIntervalRefusesToStartOnAMalformedOverride: falling back to the
// configured value on a parse error would change the instance's cadence
// at the one moment nobody is watching.
func TestIntervalRefusesToStartOnAMalformedOverride(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"not JSON", "{"},
		{"not a duration", `{"interval":"soon"}`},
		{"negative", `{"interval":"-5m"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "schedule.json"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := schedule.Open(dir, time.Hour); err == nil {
				t.Fatal("a malformed override must refuse to start")
			} else if !strings.Contains(err.Error(), "schedule") {
				t.Errorf("error %q does not name the subsystem", err)
			}
		})
	}
}

// TestSchedulerFiresOnTheInterval: the loop triggers once per elapsed
// interval, and never at start — a supervisor restarting a crashing
// instance must not turn into a request storm against the peer zone.
func TestSchedulerFiresOnTheInterval(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int64
	done := make(chan struct{})
	s := schedule.NewScheduler(iv, func(context.Context) error {
		if fired.Add(1) == 3 {
			close(done)
		}
		return nil
	}, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the scheduler fired %d times in 5s, want 3 at 20ms", fired.Load())
	}
}

// TestSchedulerParksOnZeroAndResumesOnChange: FR-013 asks for the
// interval to be changeable without redeployment, which includes turning
// it back ON. A loop that exited on zero would need a restart — exactly
// what the requirement rules out.
func TestSchedulerParksOnZeroAndResumesOnChange(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	fired := make(chan struct{}, 4)
	s := schedule.NewScheduler(iv, func(context.Context) error {
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	}, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	select {
	case <-fired:
		t.Fatal("a zero interval must not fire")
	case <-time.After(50 * time.Millisecond):
	}

	// The override wakes the parked loop without a restart. The floor
	// applies to Set, so the configured value is what the test lowers.
	resumed, err := schedule.Open(t.TempDir(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	s2 := schedule.NewScheduler(resumed, func(context.Context) error {
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	}, discard())
	go s2.Run(ctx)
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never fired after the interval was raised from zero")
	}
}

// TestSchedulerStopsOnShutdown is FR-093: cancellation ends the loop
// between cycles, and no cycle starts after it.
func TestSchedulerStopsOnShutdown(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int64
	stopped := make(chan struct{})
	s := schedule.NewScheduler(iv, func(context.Context) error {
		fired.Add(1)
		return nil
	}, discard())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s.Run(ctx)
		close(stopped)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler did not stop on cancellation")
	}
	settled := fired.Load()
	time.Sleep(30 * time.Millisecond)
	if fired.Load() != settled {
		t.Errorf("a cycle started after shutdown: %d then %d", settled, fired.Load())
	}
}

// TestSchedulerSurvivesAFailedTrigger: a queue that refuses work is a
// transient condition, and a promotion service that gave up permanently
// on one refusal would have silently stopped promoting.
func TestSchedulerSurvivesAFailedTrigger(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	done := make(chan struct{})
	s := schedule.NewScheduler(iv, func(context.Context) error {
		if calls.Add(1) == 3 {
			close(done)
		}
		return errors.New("queue is full")
	}, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the loop stopped after a failed trigger (%d calls)", calls.Load())
	}
}
