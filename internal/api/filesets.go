// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FileSet endpoints (FR-047 inventory, FR-048 packing) — the API half
// of the FileSets screen, action for action (FR-061).

type fileSetsAPI struct {
	api     *API
	surface *fileserve.Surface
}

// RegisterFileSets mounts the FileSet endpoints. Reading the inventory is
// a viewer action like any other listing; packing is admin, because it
// reads a directory of the host filesystem and puts unsigned content in
// the store — the same floor as content removal and store reset.
func RegisterFileSets(a *API, surface *fileserve.Surface) {
	f := &fileSetsAPI{api: a, surface: surface}
	a.Handle("GET /api/v1/filesets", a.RequireRole(auth.RoleViewer, f.list))
	a.Handle("POST /api/v1/filesets/pack", a.RequireRole(auth.RoleAdmin, f.pack))
}

// fileSetJSON is one inventory entry. `provenance` is the FR-048 marking:
// "manual-import" is a FileSet packed on this host, and `signed` states
// plainly that such a FileSet carries no signature (ADR-0007).
type fileSetJSON struct {
	Name       string   `json:"name"`
	Reference  string   `json:"reference"`
	Repository string   `json:"repository"`
	Versions   []string `json:"versions"`
	Version    string   `json:"version,omitempty"`
	Digest     string   `json:"digest,omitempty"`
	Provenance string   `json:"provenance"`
	Signed     bool     `json:"signed"`
	Declared   bool     `json:"declared"`
	Served     bool     `json:"served"`
	Anonymous  bool     `json:"anonymous"`
	URL        string   `json:"url,omitempty"`
}

type fileSetListJSON struct {
	FileSets []fileSetJSON `json:"filesets"`
	// PackEnabled mirrors the screen's disabled form: the API says why an
	// action it advertises would be refused, rather than only refusing.
	PackEnabled bool `json:"packEnabled"`
}

// list serves GET /api/v1/filesets: every FileSet this instance holds or
// declares, served or not, with its provenance.
func (f *fileSetsAPI) list(w http.ResponseWriter, r *http.Request) {
	entries, err := f.surface.Inventory(r.Context())
	if err != nil {
		f.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreRead,
			taxonomy.Params{"detail": err.Error()}).WithCause(err))
		return
	}
	out := fileSetListJSON{FileSets: make([]fileSetJSON, 0, len(entries)), PackEnabled: f.surface.PackEnabled}
	for i := range entries {
		e := &entries[i]
		out.FileSets = append(out.FileSets, fileSetJSON{
			Name:       e.Name,
			Reference:  e.Reference,
			Repository: e.Repository,
			Versions:   nonNil(e.Versions),
			Version:    e.Version,
			Digest:     e.Digest,
			Provenance: e.Provenance,
			Signed:     e.Signed,
			Declared:   e.Declared,
			Served:     e.Served,
			Anonymous:  e.Anonymous,
			URL:        e.URL,
		})
	}
	f.api.JSON(w, http.StatusOK, out)
}

// packRequest is the body of POST /api/v1/filesets/pack: a directory of
// the instance host, and the name and version the FileSet lands under.
// There is no file upload here and there never will be — FR-048 writes
// through the OCI import path only (SRS §5.2).
type packRequest struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// pack serves POST /api/v1/filesets/pack: package a local directory as a
// FileSet imported in the store (FR-048), the mirror of the screen's
// form. Audited on both outcomes (FR-094): the content is unsigned and of
// local origin, so the trail is the only record of who put it there.
func (f *fileSetsAPI) pack(w http.ResponseWriter, r *http.Request) {
	var req packRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		f.api.Problem(w, r, taxonomy.New(taxonomy.CodePackInput,
			taxonomy.Params{"detail": err.Error()}).WithCause(err))
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)

	id, _ := auth.IdentityFrom(r.Context())
	target := fileserve.PackReference(req.Name) + ":" + req.Version
	record := func(outcome string) {
		audit.Log(r.Context(), f.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionFileSetPack, Target: target,
			Outcome: outcome, Origin: auth.ClientOrigin(r),
		})
	}

	res, err := f.surface.Pack(r.Context(), fileserve.PackRequest{
		Source: req.Source, Name: req.Name, Version: req.Version,
	})
	if err != nil {
		te := fileserve.PackProblem(err)
		record(packOutcome(te))
		f.api.Problem(w, r, te)
		return
	}
	record(audit.OutcomeSuccess)
	f.api.JSON(w, http.StatusCreated, map[string]any{"fileset": res})
}

// packOutcome separates a refusal from a failure in the audit trail: a
// tree Tobby declined to pack, or a path this surface may not reach, is
// denied — nothing broke.
func packOutcome(err error) string {
	var te *taxonomy.Error
	if errors.As(err, &te) && te.Entry().Class == taxonomy.ClassPolicy {
		return audit.OutcomeDenied
	}
	return audit.OutcomeFailure
}

// nonNil keeps an empty list encoding as [] rather than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
