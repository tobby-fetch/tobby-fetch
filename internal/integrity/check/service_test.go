// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package check_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// serviceFixture wires a queue and the service over the fixture store.
func serviceFixture(t *testing.T, f *fixture, logw *bytes.Buffer, observe func(*integrity.Report)) (*check.Service, *tasks.Queue) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if logw != nil {
		logger = slog.New(slog.NewJSONHandler(logw, nil))
	}
	q, err := tasks.Open(t.TempDir(), logger)
	if err != nil {
		t.Fatal(err)
	}
	svc := check.NewService(f.st, q, trust{keys: f.keys}, observe)
	svc.Register()
	return svc, q
}

func startQueue(t *testing.T, q *tasks.Queue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	t.Cleanup(func() { cancel(); q.Wait() })
}

func settle(t *testing.T, q *tasks.Queue, id string) *tasks.Task {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := q.Get(id); ok && !task.Active() {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s never settled", id)
	return nil
}

// TestOneVerificationAtATime is FR-057's "one at a time": a second request
// while one is pending or running is refused with TBY-INT-004, which names
// the one in flight — and a new one is accepted once it has finished.
func TestOneVerificationAtATime(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)
	svc, q := serviceFixture(t, f, nil, nil)

	first, err := svc.Start("alexis", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Start("alexis", "10.0.0.1") // the queue is not running: the first is still pending
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeIntegrityRunning {
		t.Fatalf("second Start = %v, want %s", err, taxonomy.CodeIntegrityRunning)
	}
	if got := taxonomy.Localize("en", te).Cause; !strings.Contains(got, first.ID) {
		t.Errorf("the refusal does not name the verification in flight (%s): %q", first.ID, got)
	}

	startQueue(t, q)
	settle(t, q, first.ID)
	if _, err := svc.Start("alexis", "10.0.0.1"); err != nil {
		t.Errorf("a verification after the first finished was refused: %v", err)
	}
}

// TestCleanVerificationAsATask: the task ends done, carries the finished
// report, settles every stage item, feeds the metrics hook once and writes
// the FR-094 audit event with the requester and the origin the task kept.
func TestCleanVerificationAsATask(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)
	var heard atomic.Int32
	var logs bytes.Buffer
	svc, q := serviceFixture(t, f, &logs, func(r *integrity.Report) {
		if r.Verdict != integrity.VerdictClean {
			t.Errorf("observer heard verdict %q", r.Verdict)
		}
		heard.Add(1)
	})
	startQueue(t, q)

	created, err := svc.Start("alexis", "10.0.0.7")
	if err != nil {
		t.Fatal(err)
	}
	task := settle(t, q, created.ID)
	if task.Status != tasks.StatusDone || task.Error != nil {
		t.Fatalf("task = %s / %+v, want done", task.Status, task.Error)
	}
	if task.Verification == nil || task.Verification.Running() || task.Verification.Verdict != integrity.VerdictClean ||
		task.Verification.RunID != task.RunID || task.Verification.Progress != nil {
		t.Errorf("verification = %+v, want the finished, correlated, clean report", task.Verification)
	}
	if len(task.Items) != len(integrity.Stages()) {
		t.Fatalf("items = %+v, want one per stage", task.Items)
	}
	for _, it := range task.Items {
		if it.Status != tasks.StatusDone {
			t.Errorf("stage %s = %s, want done", it.Name, it.Status)
		}
	}
	if heard.Load() != 1 {
		t.Errorf("the metrics hook heard %d reports, want 1", heard.Load())
	}
	ev := auditEvent(t, logs.String())
	for k, want := range map[string]string{
		"actor": "alexis", "action": "store.verify", "outcome": "success", "origin": "10.0.0.7", "target": f.root,
	} {
		if ev[k] != want {
			t.Errorf("audit event %s = %v, want %q", k, ev[k], want)
		}
	}
}

func auditEvent(t *testing.T, logs string) map[string]any {
	t.Helper()
	for _, l := range strings.Split(logs, "\n") {
		if !strings.Contains(l, `"action":"store.verify"`) {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	t.Fatalf("no store.verify audit event in the logs:\n%s", logs)
	return nil
}

// TestDefectiveVerificationFailsTheTaskWithItsPrincipalCode: a store with
// a damaged blob is a failed verification — the task carries the principal
// defect (the exit class on every surface), the failed stage names it, and
// the audit event says "failure".
func TestDefectiveVerificationFailsTheTaskWithItsPrincipalCode(t *testing.T) {
	f := newFixture(t)
	_, layer := f.addRecipe("alpha", f.kp)
	p := filepath.Join(f.root, filepath.FromSlash(blobPathOf(layer)))
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc, q := serviceFixture(t, f, &logs, nil)
	startQueue(t, q)

	created, err := svc.Start("alexis", "")
	if err != nil {
		t.Fatal(err)
	}
	task := settle(t, q, created.ID)
	if task.Status != tasks.StatusFailed || task.Error == nil || task.Error.Code != taxonomy.CodeIntegrityBlobDigest {
		t.Fatalf("task = %s / %+v, want failed with %s", task.Status, task.Error, taxonomy.CodeIntegrityBlobDigest)
	}
	if got := task.Principal(); got == nil || got.Entry().Class.ExitCode() != taxonomy.ExitVerification {
		t.Errorf("principal = %v, want the verification exit class", got)
	}
	var blobs *tasks.Item
	for i := range task.Items {
		if task.Items[i].Name == string(integrity.StageBlobs) {
			blobs = &task.Items[i]
		}
	}
	if blobs == nil || blobs.Status != tasks.StatusFailed || blobs.Error == nil || blobs.Error.Code != taxonomy.CodeIntegrityBlobDigest {
		t.Errorf("blobs stage = %+v, want failed naming the defect", blobs)
	}
	if task.Verification == nil || task.Verification.Verdict != integrity.VerdictDefects {
		t.Errorf("verification = %+v, want defects", task.Verification)
	}
	if ev := auditEvent(t, logs.String()); ev["outcome"] != "failure" || ev["origin"] != "local" {
		t.Errorf("audit event = %v, want failure from the local origin", ev)
	}

	latest, ok := svc.Latest()
	if !ok || latest.ID != created.ID {
		t.Errorf("Latest = %v, %v, want the task just run", latest, ok)
	}
}
