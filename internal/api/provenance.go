// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api

import (
	"net/http"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/provenance"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Controls carries what FR-098 (R-17) adds to the content and import
// endpoints: the hardening switches the import and packing endpoints
// enforce, and the scan history the repository detail reports per tag.
type Controls struct {
	Hardening hardening.Switches
	// Scans is the per-tag scan history; nil reports no scan.
	Scans *provenance.History
}

// SetControls installs the FR-098 controls. Called at wiring time, before
// the listener serves; the zero value is the default instance — both
// switches off, no scan history.
func (a *API) SetControls(c Controls) { a.controls = c }

// refuseUnitImport answers TBY-POL-005 and audits it when
// hardening.disableUnitImport is set; it reports whether it refused.
func (a *API) refuseUnitImport(w http.ResponseWriter, r *http.Request, target string) bool {
	if !a.controls.Hardening.DisableUnitImport {
		return false
	}
	id, _ := auth.IdentityFrom(r.Context())
	hardening.AuditRefused(r.Context(), a.logger, id.Name, auth.ClientOrigin(r), target, hardening.SettingDisableUnitImport)
	a.Problem(w, r, hardening.UnitImportRefusal())
	return true
}

// provenanceJSON renders a classification for the repository detail.
func provenanceJSON(c *store.Classification, last *provenance.Scan) *contentProvenance {
	out := &contentProvenance{
		Class:     string(c.Class),
		Origin:    string(c.Origin),
		Recipes:   c.Recipes,
		Assurance: c.Assurance,
	}
	if last != nil {
		out.LastScan = scanJSON(last)
	}
	return out
}

// scanJSON renders one last scan.
func scanJSON(s *provenance.Scan) *contentProvenanceScan {
	return &contentProvenanceScan{
		Verdict:            s.Verdict,
		ScannedAt:          s.ScannedAt.UTC(),
		DatabaseBuiltAt:    utcOrZero(s.DatabaseBuiltAt),
		DatabaseAgeSeconds: s.DatabaseAgeSeconds,
		DatabaseStale:      s.Stale,
	}
}

func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}
