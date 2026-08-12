// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Accounts & tokens screen (R-01, FR-072, UI-SPEC §5.9): the admin-gated
// table of local accounts (created and updated via the tobby user CLI at
// this milestone) and the static API token lifecycle — creation with the
// secret displayed exactly once, revocation through the native <dialog>
// danger-zone component. Every mutation emits an FR-094 audit event with
// the real authenticated identity and network origin. The page is excluded
// from the htmx history cache (ADR-0015 §5).

package ui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// adminData feeds the /admin/accounts page.
type adminData struct {
	Accounts []auth.Account
	Tokens   []auth.Token
	// NewSecret and NewName carry the one-time display of a just-created
	// token secret (FR-072): present only in the direct response to the
	// creation POST, never re-renderable — the store keeps a hash only.
	NewSecret string
	NewName   string
	// FormError re-renders the creation form with a localized validation
	// message (i18n key); Name preserves the rejected input.
	FormError string
	Name      string
}

// adminScreenData snapshots the store for the page. A nil store (FR-075
// authentication override) renders empty tables: the permanent banner
// already explains that no account gate exists.
func (u *UI) adminScreenData() *adminData {
	d := &adminData{}
	if st := u.authn.Store; st != nil {
		d.Accounts = st.Accounts()
		d.Tokens = st.Tokens()
	}
	return d
}

// adminAccounts serves GET /admin/accounts.
func (u *UI) adminAccounts(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "admin-accounts", u.adminScreenData())
}

// adminTokenCreate serves POST /admin/accounts/tokens: mint the token,
// audit, and re-render the page with the secret displayed exactly once
// (no redirect — the secret exists only in this response, FR-072).
func (u *UI) adminTokenCreate(w http.ResponseWriter, r *http.Request) {
	st := u.authn.Store
	if st == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no account store: authentication is disabled (FR-075)")))
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	role, roleErr := auth.ParseRole(r.PostFormValue("role"))
	if name == "" || roleErr != nil {
		// Unreachable through the form (required input, closed select):
		// hand-crafted requests get the page back with the inline message.
		d := u.adminScreenData()
		d.FormError, d.Name = "admin.err_invalid", name
		u.renderAdmin(w, r, http.StatusBadRequest, d)
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	secret, tok, err := st.CreateToken(name, role, u.now())
	if err != nil {
		if errors.Is(err, auth.ErrExists) {
			audit.Log(r.Context(), u.logger, &audit.Event{
				Actor: id.Name, Action: audit.ActionTokenCreate, Target: name,
				Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
			})
			d := u.adminScreenData()
			d.FormError, d.Name = "admin.err_name_taken", name
			u.renderAdmin(w, r, http.StatusConflict, d)
			return
		}
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionTokenCreate, Target: tok.Name,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	d := u.adminScreenData()
	d.NewSecret, d.NewName = secret, tok.Name
	u.render.Page(w, r, "admin-accounts", d)
}

// adminTokenRevoke serves POST /admin/accounts/tokens/revoke: immediate
// and permanent (FR-072), audited, confirmed by a toast on the re-rendered
// page.
func (u *UI) adminTokenRevoke(w http.ResponseWriter, r *http.Request) {
	st := u.authn.Store
	if st == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no account store: authentication is disabled (FR-075)")))
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	id, _ := auth.IdentityFrom(r.Context())
	if err := st.RevokeToken(name); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil).WithCause(err))
			return
		}
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionTokenRevoke, Target: name,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	v := u.render.view(r, u.adminScreenData())
	v.Toasts = append(v.Toasts, v.T("admin.token_revoked", "Name", name))
	u.render.render(w, r, "admin-accounts", http.StatusOK, v)
}

// renderAdmin re-renders the page with a non-2xx status (form validation).
func (u *UI) renderAdmin(w http.ResponseWriter, r *http.Request, status int, d *adminData) {
	u.render.render(w, r, "admin-accounts", status, u.render.view(r, d))
}
