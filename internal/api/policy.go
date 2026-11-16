// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The effective content policy (FR-031 amendment 2026-11-16, R-15;
// FR-098): the API mirror of the /admin/policy screen (FR-061), and what
// `tobby scan exceptions --instance` reads. Read-only — the policy lives
// in the configuration file and in the Retriever; nothing here changes it.

package api

import (
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/scanpolicy"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// PolicyViewer produces the effective policy document; *scanpolicy.Source
// implements it.
type PolicyViewer interface {
	View() *scanpolicy.View
}

// RegisterPolicy mounts GET /api/v1/policy. A viewer's business: the
// exceptions say which vulnerabilities this instance tolerates and why,
// which every role that reads the scan reports already sees per item; no
// secret is in it.
func RegisterPolicy(a *API, p PolicyViewer) {
	a.Handle(RoutePolicy, a.RequireRole(auth.RoleViewer, func(w http.ResponseWriter, r *http.Request) {
		if p == nil {
			a.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
				"detail": "this instance was wired without its scan policy",
			}))
			return
		}
		a.JSON(w, http.StatusOK, p.View())
	}))
}
