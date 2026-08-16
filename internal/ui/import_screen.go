// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Unit-import screen (FR-023, UI-SPEC §5.6): inspect-before-import on one
// page. GET /import?ref=… is the single addressable contract — it
// pre-fills the form and triggers the inspection on load — closing three
// paths at once: the deep link from the manifest detail, the "retry" of a
// failed task, and a refresh in the middle of step 2. The POST never
// trusts the client: it re-inspects server-side and pins the digests the
// registry serves now (FR-026).

package ui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// importData feeds the /import page shell (step 1).
type importData struct {
	Ref string
	// InspectHref triggers the load-time inspection of a deep link
	// (hx-trigger="load"), carrying the ?platform= pre-selection through.
	InspectHref string
}

// importPlatform is one selectable row of step 2.
type importPlatform struct {
	Name    string
	Digest  string
	Size    int64
	Status  importer.PlatformStatus
	Checked bool
}

// inspectData feeds the step-2 fragment.
type inspectData struct {
	Ref       string
	Report    *importer.Report
	Platforms []importPlatform
	// SelectedCount and SelectedBytes summarize the checked set (the
	// button states what it will do: "Import (N platforms, ~X)").
	SelectedCount int
	SelectedBytes int64
	// AllUpToDate switches the "nothing to do" state on: direct link to
	// the artifact plus the inline pull command (FR-026 idempotence).
	AllUpToDate  bool
	ArtifactHref string
	PullCommand  string
}

// importScreen serves GET /import: the full page, or — for an htmx
// fragment request with a reference — the step-2 inspection on the same
// canonical URL (ADR-0015 §1).
func (u *UI) importScreen(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := strings.TrimSpace(q.Get("ref"))
	if ref != "" && isFragment(r) {
		u.importInspect(w, r, ref, q["platform"])
		return
	}
	data := importData{Ref: ref}
	if ref != "" {
		v := url.Values{"ref": {ref}}
		for _, p := range q["platform"] {
			v.Add("platform", p)
		}
		v.Set("inspect", "1")
		data.InspectHref = "/import?" + v.Encode()
	}
	u.render.Page(w, r, "import", data)
}

// importInspect runs the bounded remote inspection (import.inspectTimeout)
// and renders step 2. Failures render the R-03 block with the entry's real
// HTTP status, inside the swap zone so the form can retry (ADR-0015 §6).
func (u *UI) importInspect(w http.ResponseWriter, r *http.Request, ref string, preselect []string) {
	ctx, cancel := context.WithTimeout(r.Context(), u.inspectTimeout)
	defer cancel()
	rep, err := importer.Inspect(ctx, ref, u.store, importer.WithInsecureHosts(u.insecureHosts))
	if err != nil {
		u.importError(w, r, ref, err)
		return
	}
	u.render.Fragment(w, r, "import", "import-inspect", buildInspectData(r, ref, rep, preselect))
}

// buildInspectData decorates the inspection report for step 2: up-to-date
// platforms stay unchecked by default (no useless transfer, FR-026), and a
// ?platform= deep link narrows the pre-selection ("/" or "-" separated,
// the manifest-detail link format).
func buildInspectData(r *http.Request, ref string, rep *importer.Report, preselect []string) *inspectData {
	sel := map[string]bool{}
	for _, p := range preselect {
		sel[p] = true
	}
	d := &inspectData{Ref: ref, Report: rep, AllUpToDate: len(rep.Platforms) > 0}
	for i := range rep.Platforms {
		p := &rep.Platforms[i]
		pname := p.Name()
		checked := p.Status != importer.StatusUpToDate
		if checked {
			d.AllUpToDate = false
		}
		if len(sel) > 0 {
			checked = checked && (sel[pname] || sel[strings.ReplaceAll(pname, "/", "-")])
		}
		if checked {
			d.SelectedCount++
			d.SelectedBytes += p.SizeBytes
		}
		d.Platforms = append(d.Platforms, importPlatform{
			Name: pname, Digest: p.Digest, Size: p.SizeBytes, Status: p.Status, Checked: checked,
		})
	}
	if d.AllUpToDate {
		d.ArtifactHref = "/content/" + rep.Repository + "/-/tags/" + rep.Tag
		d.PullCommand = pullByTag(store.Kind(rep.Kind), r.Host, rep.Repository, rep.Tag)
	}
	return d
}

// importSubmit serves POST /import: re-inspect server-side (the digests
// come from the registry, never from the client), build one task item per
// checked platform, enqueue, audit (FR-094), and redirect to the task —
// HX-Redirect for htmx, 303 otherwise (UI-SPEC §5.6).
func (u *UI) importSubmit(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.PostFormValue("ref"))
	wanted := map[string]bool{}
	for _, p := range r.PostForm["platform"] {
		wanted[p] = true
	}

	ctx, cancel := context.WithTimeout(r.Context(), u.inspectTimeout)
	defer cancel()
	rep, err := importer.Inspect(ctx, ref, u.store, importer.WithInsecureHosts(u.insecureHosts))
	if err != nil {
		u.importError(w, r, ref, err)
		return
	}
	var items []tasks.Item
	for i := range rep.Platforms {
		p := &rep.Platforms[i]
		if wanted[p.Name()] {
			items = append(items, tasks.Item{Name: p.Name(), Digest: p.Digest})
		}
	}
	if len(items) == 0 {
		// Nothing (still) importable in the selection: back to step 2.
		u.redirectTo(w, r, "/import?ref="+url.QueryEscape(ref))
		return
	}

	if u.queue == nil {
		u.importError(w, r, ref, errors.New("task queue not configured"))
		return
	}
	// The FR-025 vendoring opt-in — a checkbox only rendered for charts,
	// never a default; the runner keeps refusing missing dependencies
	// (TBY-CHT-001) without it.
	var copts []tasks.TaskOption
	if r.PostFormValue("vendor") == "1" {
		copts = append(copts, tasks.WithVendorDependencies())
	}
	id, _ := auth.IdentityFrom(r.Context())
	t, err := u.queue.Create(tasks.TypeUnitImport, ref, id.Name, items, copts...)
	if err != nil {
		u.importError(w, r, ref, err)
		return
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor:   id.Name,
		Action:  audit.ActionImportCreate,
		Target:  ref,
		Outcome: audit.OutcomeSuccess,
		Origin:  auth.ClientOrigin(r),
	})
	u.redirectTo(w, r, "/tasks/"+t.ID)
}

// redirectTo sends the client to target: HX-Redirect for any htmx request
// (systematic after a POST — UI-SPEC §5.6), 303 See Other for plain HTTP.
func (u *UI) redirectTo(w http.ResponseWriter, r *http.Request, target string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// importError renders an import failure as its taxonomy entry with the
// real HTTP status: for a fragment, the R-03 block wrapped in the swap
// zone (role="alert", the retry keeps its target); otherwise the full page
// in its error state (ADR-0015 §6).
func (u *UI) importError(w http.ResponseWriter, r *http.Request, ref string, err error) {
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	v := u.render.view(r, importData{Ref: ref})
	v.Err = errView(v.Lang, te)
	if isFragment(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(v.Err.Status)
		if terr := u.render.templates["import"].ExecuteTemplate(w, "import-error", v); terr != nil {
			u.logger.LogAttrs(r.Context(), slog.LevelError, "rendering import error fragment",
				slog.String("error", terr.Error()))
		}
		return
	}
	u.render.render(w, r, "import", v.Err.Status, v)
}
