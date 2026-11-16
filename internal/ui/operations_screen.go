// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Operations screens of milestone 7, track B: the diagnostic bundle
// (FR-095, R-30), the state-directory inventory (FR-097, R-27) and the
// clock banner (FR-096, R-32). Each is the screen half of an endpoint of
// internal/api/operations.go (FR-061): both read the same service.

package ui

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

// BundleGenerator is the diagnostic service as the screen needs it.
// *diag.Service implements it.
type BundleGenerator interface {
	Generate(ctx context.Context, actor, origin string) (*diag.Bundle, error)
}

// diagnosticsData feeds /admin/diagnostics.
type diagnosticsData struct {
	// Available is false on an instance wired without the service.
	Available bool
	// Members lists what an archive holds, with the purpose of each.
	Members []string
}

// adminDiagnostics serves GET /admin/diagnostics.
func (u *UI) adminDiagnostics(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "admin-diagnostics", &diagnosticsData{
		Available: u.diagnostics != nil,
		Members:   diag.Members(),
	})
}

// adminDiagnosticsBundle serves POST /admin/diagnostics/bundle: the archive
// itself, as a download. It is built and checked in memory first, so a
// bundle the redaction check refused renders as an error page — never as
// a half-sent file.
func (u *UI) adminDiagnosticsBundle(w http.ResponseWriter, r *http.Request) {
	if u.diagnostics == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{
			"detail": "this instance has no diagnostic service wired",
		}))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	b, err := u.diagnostics.Generate(r.Context(), id.Name, auth.ClientOrigin(r))
	if err != nil {
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			te = taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()})
		}
		u.render.Error(w, r, te)
		return
	}
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}))
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+diag.ArchiveName(b.Manifest.Generated)+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// stateData feeds /admin/state.
type stateData struct {
	// Configured is false on an instance running without a state
	// directory.
	Configured bool
	Inventory  *statedir.Inventory
	// Broad names the secret entries readable by every user of the host —
	// an instance would refuse to start on them.
	Broad []string
}

// adminState serves GET /admin/state.
func (u *UI) adminState(w http.ResponseWriter, r *http.Request) {
	d := &stateData{Configured: u.state != nil}
	if u.state != nil {
		inv, err := u.state()
		if err != nil {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
			return
		}
		d.Inventory = inv
		for i := range inv.Items {
			if it := &inv.Items[i]; it.Present && it.Secret && it.Access == statedir.AccessBroad {
				d.Broad = append(d.Broad, it.Name)
			}
		}
	}
	u.render.Page(w, r, "admin-state", d)
}

// clockBanners turns the implausible clock verdicts into banners (FR-096):
// persistent by construction — read at every page — so the banner goes
// away on its own when a later check is plausible, with no dismissal and
// no restart.
func (rd *Renderer) clockBanners(v *View) []Banner {
	if rd.Clock == nil {
		return nil
	}
	var out []Banner
	for _, rec := range rd.Clock.Implausible() {
		var anchor string
		code := taxonomy.CodeClockBeforeBuild
		if rec.Anchor == clock.AnchorMedium {
			code = taxonomy.CodeClockBeforeMedium
			anchor = v.T("banner.clock_anchor_medium", "Medium", rec.Medium, "At", rec.At.UTC().Format(time.RFC3339))
		} else {
			anchor = v.T("banner.clock_anchor_build", "At", rec.At.UTC().Format(time.RFC3339))
		}
		out = append(out, Banner{
			Kind: "warning",
			Code: string(code),
			Text: v.T("banner.clock_implausible",
				"Now", rec.Now.UTC().Format(time.RFC3339), "Gap", rec.Gap.Round(time.Second).String(), "Anchor", anchor),
		})
	}
	return out
}
