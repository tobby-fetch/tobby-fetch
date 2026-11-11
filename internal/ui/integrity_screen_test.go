// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The Store integrity screen (FR-057, R-31). The walk itself is proved
// against a real store in internal/integrity/check and end to end in
// internal/cli; this file owns what the screen owes: it renders the last
// report whole, polls only while one runs, offers the verification to the
// operator and to no one below, and never offers to repair anything.

type stubIntegrityService struct {
	latest   *tasks.Task
	starts   []string
	refusal  error
	afterRun *tasks.Task
}

func (s *stubIntegrityService) Start(actor, origin string) (*tasks.Task, error) {
	if s.refusal != nil {
		return nil, s.refusal
	}
	s.starts = append(s.starts, actor+"@"+origin)
	if s.afterRun != nil {
		s.latest = s.afterRun
	}
	return s.latest, nil
}

func (s *stubIntegrityService) Cancel() bool { return true }

func (s *stubIntegrityService) Latest() (*tasks.Task, bool) { return s.latest, s.latest != nil }

func integrityUI(t *testing.T, svc IntegrityVerifier) *http.ServeMux {
	t.Helper()
	return mount(newTestUIWithOptions(t, &Options{Queue: newTestQueue(t), Integrity: svc}, nil))
}

func finishedTask(rep *integrity.Report) *tasks.Task {
	status := tasks.StatusDone
	if rep.Verdict == integrity.VerdictDefects {
		status = tasks.StatusFailed
	}
	return &tasks.Task{ID: "tsk_verify", RunID: "run_v", Type: tasks.TypeStoreVerify, Status: status, Verification: rep}
}

func TestIntegrityScreenBeforeAnyVerification(t *testing.T) {
	svc := &stubIntegrityService{}
	mux := integrityUI(t, svc)

	op := login(t, mux, "op", "pw-op")
	body := get(t, mux, op, "/integrity", nil).Body.String()
	for _, want := range []string{"No verification has been run", `action="/integrity/verify"`, "Verify the store"} {
		if !strings.Contains(body, want) {
			t.Errorf("the operator's screen lacks %q", want)
		}
	}
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Error("the screen polls with nothing running")
	}

	viewer := login(t, mux, "lecteur", "pw-view")
	body = get(t, mux, viewer, "/integrity", nil).Body.String()
	if strings.Contains(body, `action="/integrity/verify"`) {
		t.Error("a viewer is offered the verification")
	}
	if !strings.Contains(body, "operator role") {
		t.Error("the viewer is not told why there is no button")
	}
}

func TestIntegrityScreenPollsOnlyWhileRunning(t *testing.T) {
	svc := &stubIntegrityService{latest: &tasks.Task{
		ID: "tsk_verify", Status: tasks.StatusRunning,
		Verification: &integrity.Report{StartedAt: time.Now(), Progress: &integrity.Progress{Stage: integrity.StageBlobs, Done: 40, Total: 100, Bytes: 4096}},
	}}
	mux := integrityUI(t, svc)
	op := login(t, mux, "op", "pw-op")

	body := get(t, mux, op, "/integrity", nil).Body.String()
	for _, want := range []string{`hx-get="/integrity"`, `hx-trigger="every 2s"`, "Verification in progress", "Re-hashing blobs", "40 / 100", `value="40"`, "/tasks/tsk_verify"} {
		if !strings.Contains(body, want) {
			t.Errorf("the running screen lacks %q", want)
		}
	}
	if strings.Contains(body, `action="/integrity/verify"`) {
		t.Error("a second verification is offered while one runs")
	}

	// The polled fragment is served on the same canonical URL, the
	// layout-free body only (ADR-0015 §1).
	frag := get(t, mux, op, "/integrity", map[string]string{"HX-Request": "true", "HX-Target": "integrity-body"}).Body.String()
	if !strings.Contains(frag, `id="integrity-body"`) || strings.Contains(frag, "<html") {
		t.Errorf("the fragment is not the bare polled body:\n%s", frag)
	}

	// Once it finishes the polling attributes are gone (UI-SPEC §8.3).
	svc.latest = finishedTask(&integrity.Report{
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(), Verdict: integrity.VerdictClean,
		Store: integrity.Store{Root: "/srv/store"}, Checked: integrity.Totals{Blobs: 12, Bytes: 2048, Manifests: 5, Recipes: 1},
	})
	body = get(t, mux, op, "/integrity", nil).Body.String()
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Error("the screen keeps polling after the verification finished")
	}
	if !strings.Contains(body, `data-verdict="clean"`) || !strings.Contains(body, "The store is intact") {
		t.Error("a clean verification is not stated")
	}
}

// TestIntegrityScreenNamesEveryDefectAndReReportsTheAssurance: the verdict,
// each defect with its code and the object it names, the orphans as a note
// that is not a defect, and the per-recipe table with the reduced-assurance
// marker (FR-037) — in both languages.
func TestIntegrityScreenNamesEveryDefectAndReReportsTheAssurance(t *testing.T) {
	const blob = "docker/registry/v2/blobs/sha256/ab/abcd/data"
	svc := &stubIntegrityService{latest: finishedTask(&integrity.Report{
		RunID: "run_v", StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
		Verdict: integrity.VerdictDefects, Store: integrity.Store{Root: "/srv/store", Transportable: true},
		Checked: integrity.Totals{Blobs: 12, Bytes: 2048, Manifests: 5, Recipes: 2},
		Defects: []integrity.Defect{
			{Code: taxonomy.CodeIntegrityBlobDigest, Path: blob, Params: map[string]string{"path": blob, "expected": "sha256:aa", "actual": "sha256:bb"}},
			{Code: taxonomy.CodeSignature, Path: "bare@1.0.0", Params: map[string]string{"recipe": "reg/cookbook/bare:1.0.0", "fingerprints": "SHA256:xyz", "scope": "tenant-a"}},
		},
		DefectCount:  2,
		Findings:     []integrity.Defect{{Code: taxonomy.CodeIntegrityOrphan, Path: "docker.io/library/old:1.0", Params: map[string]string{"path": "docker.io/library/old:1.0"}}},
		FindingCount: 1,
		Recipes: []integrity.Recipe{
			{Name: "good", Version: "1.0.0", Complete: true, Signature: integrity.SignatureVerified, KeyFingerprint: "SHA256:abc"},
			{Name: "lab", Version: "1.0.0", Complete: true, Signature: integrity.SignatureUnsignedAdmitted, TrustScope: "lab", Assurance: assurance.ForUnsigned("lab")},
			{Name: "bare", Version: "1.0.0", Complete: false, Signature: integrity.SignatureRefused, TrustScope: "tenant-a"},
		},
	})}
	mux := integrityUI(t, svc)
	c := login(t, mux, "lecteur", "pw-view")

	body := get(t, mux, c, "/integrity", nil).Body.String()
	for _, want := range []string{
		`data-verdict="defects"`, "2 defects found",
		"TBY-INT-001", blob, "sha256:bb",
		"TBY-SIG-001", "bare@1.0.0", "trust scope tenant-a",
		"1 piece of orphaned content", "docker.io/library/old:1.0", "TBY-INT-003",
		"transportable (a medium)",
		"/api/v1/store/verification",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the report does not carry %q", want)
		}
	}
	if got := markerCount(body); got != 1 {
		t.Errorf("markers = %d, want 1 (only the recipe admitted unsigned under a scope)", got)
	}
	for _, repair := range []string{"Repair", "Delete", "Remove", "Prune now"} {
		if strings.Contains(body, repair) {
			t.Errorf("the screen offers %q: a verification reports, it never repairs or deletes", repair)
		}
	}

	fr := get(t, mux, c, "/integrity", map[string]string{"Accept-Language": "fr"}).Body.String()
	for _, want := range []string{"Intégrité du store", "2 défauts constatés", "contenu orphelin", "Assurance réduite"} {
		if !strings.Contains(fr, want) {
			t.Errorf("the French screen lacks %q", want)
		}
	}
}

func TestIntegrityVerifyStartsOneAndRefusesASecond(t *testing.T) {
	running := &tasks.Task{ID: "tsk_verify", Status: tasks.StatusPending}
	svc := &stubIntegrityService{afterRun: running}
	u := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t), Integrity: svc}, nil)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")

	w := postForm(t, mux, c, "/integrity/verify", "csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusOK || len(svc.starts) != 1 || !strings.HasPrefix(svc.starts[0], "op@") {
		t.Fatalf("POST = %d, starts %v: want one start by the operator", w.Code, svc.starts)
	}
	if !strings.Contains(w.Body.String(), "Verification in progress") {
		t.Error("the response does not show the verification just started")
	}

	svc.refusal = taxonomy.New(taxonomy.CodeIntegrityRunning, taxonomy.Params{"task": "tsk_verify"})
	w = postForm(t, mux, c, "/integrity/verify", "csrf="+csrfOf(t, u, c), map[string]string{"HX-Request": "true", "HX-Target": "integrity-body"})
	if w.Code != http.StatusConflict {
		t.Errorf("a second verification = %d, want 409", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "TBY-INT-004") || !strings.Contains(body, "tsk_verify") {
		t.Errorf("the refusal does not name the verification in flight:\n%s", body)
	}

	viewer := login(t, mux, "lecteur", "pw-view")
	if w := postForm(t, mux, viewer, "/integrity/verify", "csrf="+csrfOf(t, u, viewer), nil); w.Code != http.StatusForbidden {
		t.Errorf("a viewer starting a verification = %d, want 403", w.Code)
	}
}

func TestIntegrityScreenWithoutAServiceSaysSo(t *testing.T) {
	mux := mount(newTestUIWithOptions(t, &Options{Queue: newTestQueue(t)}, nil))
	c := login(t, mux, "op", "pw-op")
	if body := get(t, mux, c, "/integrity", nil).Body.String(); !strings.Contains(body, "no task queue") {
		t.Errorf("an instance that cannot verify does not say so:\n%s", body)
	}
}
