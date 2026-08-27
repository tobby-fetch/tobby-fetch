// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// OCI image layout screens (FR-051) and the store reset (FR-046), the
// interface half of /api/v1/oci-layout and /api/v1/store/reset (FR-061
// parity). Both go through interop.Service: the confirmation rule and the
// selection rules live there, so neither surface owns a rule the other
// does not have.
//
// Admin-gated. An export names a path on the HOST filesystem and writes
// the store's content to it; that is an administrative capability. The
// reset is admin by the letter of FR-046, and it keeps the typed
// confirmation even on an instance running under the FR-075
// authentication override — where the audit entry records the
// unauthenticated context instead of an identity.

package ui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// layoutData feeds /admin/oci-layout.
type layoutData struct {
	// Configured is false on an instance wired without the service; the
	// screen then explains rather than offering inert controls.
	Configured bool
	// Recipes lists the recorded recipes an export can be narrowed to.
	Recipes []string

	// Submitted values, preserved across a re-render.
	Output     string
	Input      string
	Repository string
	Recipe     string
	Directory  bool
	Overwrite  bool

	// Plan is the result of an estimate: the FR-055 numbers, computed by
	// the code that would do the writing.
	Plan *layoutPlanView
	// Err renders a taxonomized failure inline.
	Err *ErrView
}

// layoutPlanView is one estimate, ready to render.
type layoutPlanView struct {
	Format           string
	References       int
	Manifests        int
	Blobs            int
	TotalBytes       int64
	LargestFileBytes int64
	Missing          []string
}

// ConfirmationPhrase is the word FR-046 asks to be typed. Exposed to the
// template so the screen quotes the same string the service checks —
// never a translated one (ADR-0015 §7: a confirmation two people cannot
// talk about is not a confirmation).
func (*layoutData) ConfirmationPhrase() string { return interop.ConfirmationPhrase }

// storeData feeds /admin/store.
type storeData struct {
	Configured bool
	// Repositories and Bytes are what the store holds right now: a reset
	// confirmation that does not say what is about to go is a dialog
	// nobody can answer.
	Repositories int
	Bytes        int64
	// Done carries the outcome of a completed reset.
	Done *store.ResetResult
	Err  *ErrView
}

// ConfirmationPhrase mirrors layoutData's, for the reset form.
func (*storeData) ConfirmationPhrase() string { return interop.ConfirmationPhrase }

// layoutScreen serves GET /admin/oci-layout.
func (u *UI) layoutScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "admin-oci-layout", u.layoutScreenData())
}

// layoutScreenData snapshots what the form needs.
func (u *UI) layoutScreenData() *layoutData {
	d := &layoutData{Configured: u.interop != nil}
	records, err := u.store.RecipeRecords()
	if err != nil {
		return d
	}
	for i := range records {
		d.Recipes = append(d.Recipes, records[i].Name+"@"+records[i].Version)
	}
	return d
}

// layoutEstimate serves POST /admin/oci-layout/plan: the side-effect-free
// projection of FR-055, on the exact selection the export form carries.
func (u *UI) layoutEstimate(w http.ResponseWriter, r *http.Request) {
	d, req, ok := u.layoutExportForm(w, r)
	if !ok {
		return
	}
	plan, projection, err := u.interop.Plan(r.Context(), req)
	if err != nil {
		u.renderLayout(w, r, d, err)
		return
	}
	view := &layoutPlanView{
		Format:           string(projection.Format),
		References:       len(plan.Refs),
		Manifests:        projection.Manifests,
		Blobs:            projection.Blobs,
		TotalBytes:       projection.TotalBytes,
		LargestFileBytes: projection.LargestFileBytes,
	}
	for _, missing := range plan.Missing {
		view.Missing = append(view.Missing, missingLabel(&missing))
	}
	d.Plan = view
	u.render.Page(w, r, "admin-oci-layout", d)
}

// layoutExport serves POST /admin/oci-layout/export: enqueue and follow
// the task, like every other long operation of the product.
func (u *UI) layoutExport(w http.ResponseWriter, r *http.Request) {
	d, req, ok := u.layoutExportForm(w, r)
	if !ok {
		return
	}
	actor := layoutActor(r)
	task, err := u.interop.StartExport(actor, req)
	u.auditLayout(r, audit.ActionLayoutExport, actor, req.Output, err)
	if err != nil {
		u.renderLayout(w, r, d, err)
		return
	}
	u.redirectTo(w, r, "/tasks/"+task.ID)
}

// layoutImport serves POST /admin/oci-layout/import.
func (u *UI) layoutImport(w http.ResponseWriter, r *http.Request) {
	if u.interop == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no interoperability service is wired on this instance")))
		return
	}
	d := u.layoutScreenData()
	d.Input = strings.TrimSpace(r.PostFormValue("input"))
	d.Repository = strings.TrimSpace(r.PostFormValue("repository"))

	actor := layoutActor(r)
	task, err := u.interop.StartImport(actor, &interop.ImportRequest{
		Input: d.Input, Repository: d.Repository,
	})
	u.auditLayout(r, audit.ActionLayoutImport, actor, d.Input, err)
	if err != nil {
		u.renderLayout(w, r, d, err)
		return
	}
	u.redirectTo(w, r, "/tasks/"+task.ID)
}

// layoutExportForm reads the export form into a request, or renders the
// inert state when no service is wired.
func (u *UI) layoutExportForm(w http.ResponseWriter, r *http.Request) (*layoutData, *interop.ExportRequest, bool) {
	if u.interop == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no interoperability service is wired on this instance")))
		return nil, nil, false
	}
	d := u.layoutScreenData()
	d.Output = strings.TrimSpace(r.PostFormValue("output"))
	d.Recipe = strings.TrimSpace(r.PostFormValue("recipe"))
	d.Directory = r.PostFormValue("directory") != ""
	d.Overwrite = r.PostFormValue("overwrite") != ""

	req := &interop.ExportRequest{
		Output:    d.Output,
		Format:    ocilayout.FormatTar,
		Overwrite: d.Overwrite,
	}
	if d.Directory {
		req.Format = ocilayout.FormatDirectory
	}
	if d.Recipe != "" {
		req.Recipes = []string{d.Recipe}
	}
	return d, req, true
}

// storeScreen serves GET /admin/store.
func (u *UI) storeScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "admin-store", u.storeScreenData(r))
}

func (u *UI) storeScreenData(r *http.Request) *storeData {
	d := &storeData{Configured: u.interop != nil}
	if counts, err := u.store.Counts(r.Context()); err == nil {
		d.Repositories = counts.Repositories
		d.Bytes = counts.PhysicalBytes
	}
	return d
}

// storeReset serves POST /admin/store/reset (FR-046). The typed
// confirmation is checked by the service, which also writes the audit
// record — including for a refusal, because somebody typing the wrong
// word into that field is the trail's early warning.
func (u *UI) storeReset(w http.ResponseWriter, r *http.Request) {
	if u.interop == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no interoperability service is wired on this instance")))
		return
	}
	res, err := u.interop.Reset(r.Context(), layoutActor(r), auth.ClientOrigin(r),
		r.PostFormValue("confirmation"))
	if err != nil {
		d := u.storeScreenData(r)
		ev := errView(requestLang(r), asTaxonomyError(err))
		d.Err = ev
		u.render.render(w, r, "admin-store", ev.Status, u.render.view(r, d))
		return
	}
	d := u.storeScreenData(r)
	d.Done = &res
	v := u.render.view(r, d)
	v.Toasts = append(v.Toasts, v.T("store.reset_toast", "Count", res.Repositories))
	u.render.render(w, r, "admin-store", http.StatusOK, v)
}

// renderLayout re-renders the layout screen with a taxonomized failure
// inline and the entry's real HTTP status.
func (u *UI) renderLayout(w http.ResponseWriter, r *http.Request, d *layoutData, err error) {
	ev := errView(requestLang(r), asTaxonomyError(err))
	d.Err = ev
	u.render.render(w, r, "admin-oci-layout", ev.Status, u.render.view(r, d))
}

// layoutActor names who acted: the authenticated identity, or the
// explicit "anonymous" of an instance running under the FR-075 override —
// which is the unauthenticated context FR-046 asks the audit entry to
// record rather than an empty field.
func layoutActor(r *http.Request) string {
	id, _ := auth.IdentityFrom(r.Context())
	return id.Name
}

// auditLayout emits the FR-094 record of one layout operation, refusals
// included: who tried to write the store's content onto which path is
// exactly what the trail is for.
func (u *UI) auditLayout(r *http.Request, action, actor, target string, err error) {
	outcome := audit.OutcomeSuccess
	if err != nil {
		outcome = audit.OutcomeFailure
		if asTaxonomyError(err).Entry().Class == taxonomy.ClassPolicy {
			outcome = audit.OutcomeDenied
		}
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, Action: action, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// missingLabel renders one absence as the untranslated triple the API
// mirror carries (reason, repository, subject): a stable label the screen
// shows verbatim rather than a sentence the two surfaces would word
// differently.
func missingLabel(m *ocilayout.Missing) string {
	subject := m.Digest
	if subject == "" {
		subject = m.Ref.Tag
	}
	return m.Reason + " · " + m.Ref.Repo + " · " + subject
}

// asTaxonomyError unwraps a service failure; the service taxonomizes
// everything it returns.
func asTaxonomyError(err error) *taxonomy.Error {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te
	}
	return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
}
