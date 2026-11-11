// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package check

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check/checktest"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestASynchronizationLandsWhileAVerificationRuns: a promotion service
// cannot stall for hours behind a disk walk. The verification runs on a
// lane of its own; a synchronization queued while it is in flight reaches
// done on the serial worker, and the verification is still running.
// It is then cancelled: the task ends failed with TBY-INT-006, and the
// FR-094 event is written with the outcome it had.
func TestASynchronizationLandsWhileAVerificationRuns(t *testing.T) {
	f := checktest.New(t)
	f.AddRecipe("alpha", f.KP)
	var logs bytes.Buffer
	q, err := tasks.Open(t.TempDir(), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{}) // never closed: the walk is held until cancelled
	svc := NewService(f.St, q, nil, nil)
	svc.walkGate = gate
	svc.Register()
	syncRan := make(chan struct{})
	q.Register(tasks.TypeSync, func(context.Context, *tasks.Task, *slog.Logger, func()) error {
		close(syncRan)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	t.Cleanup(func() { cancel(); q.Wait() })

	v, err := svc.Start("alexis", "10.0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the verification to be running", func() bool {
		cur, _ := q.Get(v.ID)
		return cur.Status == tasks.StatusRunning
	})

	sync, err := q.Create(tasks.TypeSync, "retriever", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-syncRan:
	case <-time.After(10 * time.Second):
		t.Fatal("the synchronization did not start while a verification was running: it waits behind it")
	}
	waitFor(t, "the synchronization to finish", func() bool {
		cur, _ := q.Get(sync.ID)
		return cur.Status == tasks.StatusDone
	})
	if cur, _ := q.Get(v.ID); cur.Status != tasks.StatusRunning {
		t.Fatalf("the verification is %s: the test proved nothing about overlap", cur.Status)
	}

	if !svc.Cancel() {
		t.Fatal("Cancel found nothing to cancel")
	}
	waitFor(t, "the cancelled verification to settle", func() bool {
		cur, _ := q.Get(v.ID)
		return !cur.Active()
	})
	cur, _ := q.Get(v.ID)
	if cur.Status != tasks.StatusFailed || cur.Error == nil || cur.Error.Code != taxonomy.CodeIntegrityCancelled {
		t.Fatalf("cancelled task = %s / %+v, want failed with %s", cur.Status, cur.Error, taxonomy.CodeIntegrityCancelled)
	}
	if svc.Cancel() {
		t.Error("Cancel reported a verification in flight after it ended")
	}
	// The event of a verification that did not reach a verdict is written.
	var ev map[string]any
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, `"action":"store.verify"`) {
			if err := json.Unmarshal([]byte(l), &ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ev == nil || ev["outcome"] != "failure" || ev["actor"] != "alexis" || ev["origin"] != "10.0.0.9" {
		t.Errorf("audit event = %v, want a failure by alexis from 10.0.0.9", ev)
	}
	// And a new one is accepted once the cancelled one has ended.
	svc.walkGate = nil
	if _, err := svc.Start("alexis", ""); err != nil {
		t.Errorf("a verification after the cancelled one was refused: %v", err)
	}
}
