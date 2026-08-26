// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// FileSets screen (FR-047 inventory, FR-048 packing): what this instance
// holds as file sets, which of them it serves under /files/, and the form
// that packages a directory of the host into one.
//
// It is the interface half of `tobby fileset pack` (internal/cli/
// filesetcmd.go) and the mirror of /api/v1/filesets (internal/api/
// filesets.go), action for action (FR-061). All three go through the same
// fileserve.Surface, so none of them can answer differently.
//
// Three things this screen must never blur.
//
//   - It opens no upload surface. The form submits a PATH on the instance
//     host, never a file: writing into the store goes through the OCI
//     import path and nothing else (FR-048, SRS §5.2). Which paths it may
//     reach is files.packRoots, and with none configured the form is
//     rendered inert rather than hidden (FR-075).
//   - A packed file set is UNSIGNED. Tobby holds no private key
//     (ADR-0007), so the listing marks manual imports as such and the
//     success state says it in words.
//   - Serving is a separate, explicit step. FR-047 enablement lives in
//     the configuration file, so the success state hands over the exact
//     block to add rather than editing anything behind the operator's
//     back.

package ui

import (
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// fileSetsData feeds the /filesets page.
type fileSetsData struct {
	Entries []fileserve.Entry
	// PackEnabled says files.packRoots names at least one directory. When
	// false the form is disabled and explains itself.
	PackEnabled bool
	// Source, Name and Version are preserved across a refusal so a
	// rejected submission is edited rather than retyped.
	Source  string
	Name    string
	Version string
	// Packed is the result of a packing that just completed: the digest,
	// the counts, and the configuration block that would serve it.
	Packed *fileserve.PackResult
}

// ServeSnippet is the files.filesets block that turns the just-packed
// FileSet into a served one (FR-047). A method on the data rather than a
// template loop: the exact indentation of a YAML block is not something
// to reconstruct in a template, and this is text an operator copies and
// pastes verbatim.
func (d *fileSetsData) ServeSnippet() string {
	if d.Packed == nil {
		return ""
	}
	return "files:\n  filesets:\n    - name: " + d.Packed.Name +
		"\n      ref: " + d.Packed.Reference +
		"\n      version: " + d.Packed.Version + "\n"
}

// filesets serves GET /filesets: the inventory, readable by every signed-in
// role like the other listings.
func (u *UI) filesetsScreen(w http.ResponseWriter, r *http.Request) {
	d, err := u.fileSetsScreenData(r)
	if err != nil {
		v := u.render.view(r, d)
		v.Err = errView(v.Lang, taxonomy.New(taxonomy.CodeStoreRead,
			taxonomy.Params{"detail": err.Error()}).WithCause(err))
		u.render.render(w, r, "filesets", v.Err.Status, v)
		return
	}
	u.render.Page(w, r, "filesets", d)
}

// fileSetsScreenData builds the page payload. An unreachable store yields
// the page with its error block rather than a bare 500: the form and the
// explanations are still worth showing.
func (u *UI) fileSetsScreenData(r *http.Request) (*fileSetsData, error) {
	d := &fileSetsData{}
	if u.fileSets == nil {
		return d, nil
	}
	d.PackEnabled = u.fileSets.PackEnabled
	entries, err := u.fileSets.Inventory(r.Context())
	if err != nil {
		return d, err
	}
	d.Entries = entries
	return d, nil
}

// filesetsPack serves POST /filesets/pack (FR-048): package a directory of
// the instance host as a FileSet imported in the store. Admin-gated —
// it reads the host filesystem and puts unsigned content in the store —
// behind the session CSRF check, audited either way (FR-094).
func (u *UI) filesetsPack(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimSpace(r.PostFormValue("source"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	version := strings.TrimSpace(r.PostFormValue("version"))
	id, _ := auth.IdentityFrom(r.Context())
	target := fileserve.PackReference(name) + ":" + version

	if u.fileSets == nil {
		u.packRefusal(w, r, source, name, version, taxonomy.New(taxonomy.CodeInternal, nil))
		return
	}
	res, err := u.fileSets.Pack(r.Context(), fileserve.PackRequest{
		Source: source, Name: name, Version: version,
	})
	if err != nil {
		e := fileserve.PackProblem(err)
		u.auditPack(r, id.Name, target, publishOutcome(e))
		u.packRefusal(w, r, source, name, version, e)
		return
	}
	u.auditPack(r, id.Name, res.Reference+":"+res.Version, audit.OutcomeSuccess)

	d, _ := u.fileSetsScreenData(r)
	d.Packed = res
	v := u.render.view(r, d)
	v.Toasts = append(v.Toasts, v.T("filesets.toast_packed", "Reference", res.Reference))
	u.render.render(w, r, "filesets", http.StatusOK, v)
}

// packRefusal re-renders the screen with the taxonomized block and the
// entry's real HTTP status, the submitted values preserved.
func (u *UI) packRefusal(w http.ResponseWriter, r *http.Request, source, name, version string, e *taxonomy.Error) {
	d, _ := u.fileSetsScreenData(r)
	d.Source, d.Name, d.Version = source, name, version
	v := u.render.view(r, d)
	v.Err = errView(v.Lang, e)
	u.render.render(w, r, "filesets", v.Err.Status, v)
}

func (u *UI) auditPack(r *http.Request, actor, target, outcome string) {
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, Action: audit.ActionFileSetPack, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}
