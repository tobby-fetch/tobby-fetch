// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Operations endpoints of milestone 7, track B: the diagnostic bundle
// (FR-095, R-30), the clock verdicts (FR-096, R-32) and the state-directory
// inventory (FR-097, R-27) — the API mirrors of the admin screens of
// package ui (FR-061).

package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/clock"
	"github.com/tobby-fetch/tobby-fetch/internal/diag"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// BundleGenerator is the diagnostic service as this surface needs it.
// *diag.Service implements it.
type BundleGenerator interface {
	Generate(ctx context.Context, actor, origin string) (*diag.Bundle, error)
}

// OperationsOptions is what RegisterOperations needs wired.
type OperationsOptions struct {
	// Diagnostics produces bundles; nil answers 409 (the instance has no
	// way to).
	Diagnostics BundleGenerator
	// Clock reads the latest clock verdicts; nil reports none.
	Clock *clock.Monitor
	// State inspects the state directory; nil when the instance runs
	// without one.
	State func() (*statedir.Inventory, error)
	// Now injects the clock under test.
	Now func() time.Time
}

// RegisterOperations mounts the three endpoints.
//
// The bundle and the inventory are administrator's: the first gathers
// logs and configuration, the second names every file of the state
// directory (never its content). The clock verdict is a viewer's read —
// the same fact the banner shows every signed-in user.
func RegisterOperations(a *API, opts *OperationsOptions) {
	o := &operationsAPI{api: a, opts: opts}
	a.Handle(RouteDiagnosticsBundle, a.RequireRole(auth.RoleAdmin, o.bundle))
	a.Handle(RouteClock, a.RequireRole(auth.RoleViewer, o.clock))
	a.Handle(RouteState, a.RequireRole(auth.RoleAdmin, o.state))
}

type operationsAPI struct {
	api  *API
	opts *OperationsOptions
}

func (o *operationsAPI) now() time.Time {
	if o.opts.Now != nil {
		return o.opts.Now()
	}
	return time.Now()
}

// bundle serves POST /api/v1/diagnostics/bundle: the archive itself in the
// response body, built in memory and checked before the first byte is
// sent — a bundle that would carry a secret is a problem document, never
// a partial download.
func (o *operationsAPI) bundle(w http.ResponseWriter, r *http.Request) {
	if o.opts == nil || o.opts.Diagnostics == nil {
		o.api.Problem(w, r, taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{
			"detail": "this instance has no diagnostic service wired",
		}))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	b, err := o.opts.Diagnostics.Generate(r.Context(), id.Name, auth.ClientOrigin(r))
	if err != nil {
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			te = taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()})
		}
		o.api.Problem(w, r, te)
		return
	}
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		o.api.Problem(w, r, taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}))
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+diag.ArchiveName(b.Manifest.Generated)+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// clockVerdict is one anchor of GET /api/v1/clock.
type clockVerdict struct {
	Anchor string `json:"anchor"`
	// Medium names the medium of a "medium" verdict.
	Medium string `json:"medium,omitempty"`
	// AnchorTime is the date the clock was compared with; Now the local
	// clock at that moment.
	AnchorTime time.Time `json:"anchor_time"`
	Now        time.Time `json:"now"`
	// GapSeconds is how far the clock is behind the anchor (zero or
	// negative when it is not).
	GapSeconds       int64 `json:"gap_seconds"`
	ToleranceSeconds int64 `json:"tolerance_seconds"`
	Implausible      bool  `json:"implausible"`
	// Code is TBY-CLK-001 or TBY-CLK-002 when implausible.
	Code string `json:"code,omitempty"`
}

// clock serves GET /api/v1/clock.
func (o *operationsAPI) clock(w http.ResponseWriter, _ *http.Request) {
	out := struct {
		Now      time.Time      `json:"now"`
		Verdicts []clockVerdict `json:"verdicts"`
	}{Now: o.now().UTC(), Verdicts: []clockVerdict{}}
	if o.opts != nil && o.opts.Clock != nil {
		for _, rec := range o.opts.Clock.Records() {
			v := clockVerdict{
				Anchor: string(rec.Anchor), Medium: rec.Medium, AnchorTime: rec.At.UTC(), Now: rec.Now.UTC(),
				GapSeconds: int64(rec.Gap / time.Second), ToleranceSeconds: int64(rec.Tolerance / time.Second),
				Implausible: rec.Implausible,
			}
			if te := rec.Error(); te != nil {
				v.Code = string(te.Code())
			}
			out.Verdicts = append(out.Verdicts, v)
		}
	}
	o.api.JSON(w, http.StatusOK, out)
}

// state serves GET /api/v1/state: names, classes, sizes and modes of the
// state directory — never contents.
func (o *operationsAPI) state(w http.ResponseWriter, r *http.Request) {
	if o.opts == nil || o.opts.State == nil {
		o.api.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance runs without a state directory (state.root)",
		}))
		return
	}
	inv, err := o.opts.State()
	if err != nil {
		o.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	o.api.JSON(w, http.StatusOK, inv)
}
