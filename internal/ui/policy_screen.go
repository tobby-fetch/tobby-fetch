// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The effective content policy screen (FR-031 amendment 2026-11-16, R-15;
// FR-098): /admin/policy, read-only, the UI half of GET /api/v1/policy
// (FR-061) — both render the same scanpolicy.View, so they cannot differ.

package ui

import (
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/scanpolicy"
)

// PolicyViewer produces the effective policy document;
// *scanpolicy.Source implements it.
type PolicyViewer interface {
	View() *scanpolicy.View
}

// policyData feeds the /admin/policy page. View is nil on an instance
// wired without a policy, which renders the screen saying so.
type policyData struct {
	View *scanpolicy.View
}

// adminPolicy serves GET /admin/policy.
func (u *UI) adminPolicy(w http.ResponseWriter, r *http.Request) {
	d := &policyData{}
	if u.policy != nil {
		d.View = u.policy.View()
	}
	u.render.Page(w, r, "admin-policy", d)
}
