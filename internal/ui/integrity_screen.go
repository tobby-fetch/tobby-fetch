// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The Store integrity screen (FR-057, roadmap R-31): the interface half of
// `tobby store verify`, in both modes — an instance's store and a
// transportable one are verified the same way, and the screen says which.
//
// It adds no verification logic of its own. Starting one enqueues a
// tracked task through the same service the REST mirror uses
// (/api/v1/store/verify, FR-061), and the report it renders is the one
// /api/v1/store/verification serves: the last verification task's, whole.
// While a verification runs the body polls on the canonical URL and stops
// polling the moment it finishes (UI-SPEC §8.3, B-002).
//
// The screen never offers to repair or delete anything. The report names
// what is wrong; fixing it is done with the tools the product already has
// (re-synchronize, prune), and a button here that did it would be a
// second, unreviewed implementation of both.

package ui

import (
	"errors"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// IntegrityVerifier is the verification service as this screen needs it.
// *check.Service implements it; nil leaves the screen reporting that this
// instance cannot verify.
type IntegrityVerifier interface {
	Start(actor, origin string) (*tasks.Task, error)
	Latest() (*tasks.Task, bool)
}

// integrityDefectView is one defect or finding, localized.
type integrityDefectView struct {
	Err  *ErrView
	Path string
}

// integrityData feeds /integrity.
type integrityData struct {
	// Available is false on an instance with no task queue to verify on.
	Available bool
	CanVerify bool
	// Has says a verification exists; Running that it is pending or in
	// flight, in which case Report is the progress snapshot.
	Has     bool
	Running bool
	TaskID  string
	Status  tasks.Status
	Report  *integrity.Report
	// Failed is true when the verdict is "defects".
	Failed bool

	Progress *integrity.Progress
	// Percent is the current stage's own progress, 0 when its total is
	// not known yet.
	Percent int

	Defects  []integrityDefectView
	Findings []integrityDefectView
	// Failure is why the last request to start one was refused
	// (TBY-INT-004: one is already running).
	Failure *ErrView

	ReportHref string
}

// integrityScreen serves GET /integrity. The polled body swaps on the same
// canonical URL, told apart by the HX-Target header (ADR-0015 §1).
func (u *UI) integrityScreen(w http.ResponseWriter, r *http.Request) {
	d := u.integrityData(r)
	if isFragment(r) && r.Header.Get("HX-Target") == "integrity-body" {
		u.render.Fragment(w, r, "integrity", "integrity-body", d)
		return
	}
	u.render.Page(w, r, "integrity", d)
}

// integrityVerify serves POST /integrity/verify: enqueue one verification
// and show it running. A second one while one is in flight is refused with
// TBY-INT-004, rendered where the operator is looking.
func (u *UI) integrityVerify(w http.ResponseWriter, r *http.Request) {
	d := u.integrityData(r)
	status := http.StatusOK
	switch u.integrity {
	case nil:
		d.Failure = errView(requestLang(r), taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance has no task queue to run a store verification on",
		}))
		status = http.StatusInternalServerError
	default:
		id, _ := auth.IdentityFrom(r.Context())
		if _, err := u.integrity.Start(id.Name, auth.ClientOrigin(r)); err != nil {
			var te *taxonomy.Error
			if !errors.As(err, &te) {
				te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
			}
			d.Failure = errView(requestLang(r), te)
			status = d.Failure.Status
		} else {
			// What the operator sees now is the verification just
			// started, not the one before it.
			d = u.integrityData(r)
		}
	}
	if isFragment(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		u.render.Fragment(w, r, "integrity", "integrity-body", d)
		return
	}
	u.render.Page(w, r, "integrity", d)
}

// integrityData assembles the screen from the last verification task.
func (u *UI) integrityData(r *http.Request) *integrityData {
	lang := requestLang(r)
	d := &integrityData{ReportHref: "/api/v1/store/verification", Available: u.integrity != nil}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		d.CanVerify = id.Role.AtLeast(auth.RoleOperator)
	}
	if u.integrity == nil {
		return d
	}
	t, ok := u.integrity.Latest()
	if !ok {
		return d
	}
	d.Has, d.TaskID, d.Status, d.Report = true, t.ID, t.Status, t.Verification
	d.Running = t.Active()
	if t.Verification == nil {
		// A task that failed before it produced a report (the walk itself
		// could not run): the task-level error is the whole story.
		if t.Error != nil {
			d.Failure = itemErrView(lang, t.Error, t.RunID)
		}
		return d
	}
	rep := t.Verification
	d.Progress = rep.Progress
	if p := rep.Progress; p != nil && p.Total > 0 {
		d.Percent = min(100, p.Done*100/p.Total)
	}
	d.Failed = rep.Verdict == integrity.VerdictDefects
	for i := range rep.Defects {
		d.Defects = append(d.Defects, integrityDefectView{Err: errView(lang, rep.Defects[i].Error()), Path: rep.Defects[i].Path})
	}
	for i := range rep.Findings {
		d.Findings = append(d.Findings, integrityDefectView{Err: errView(lang, rep.Findings[i].Error()), Path: rep.Findings[i].Path})
	}
	return d
}
