// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Accounts & tokens screen (R-01, FR-072, FR-073, FR-074, UI-SPEC §5.9):
// the admin-gated table of local accounts with their full lifecycle —
// creation, role change, removal — and the static API token lifecycle:
// creation with the secret displayed exactly once, revocation through the
// native <dialog> danger-zone component. Every mutation emits an FR-094
// audit event with the real authenticated identity and network origin. The
// page is excluded from the htmx history cache (ADR-0015 §5).
//
// Password hashes are computed by the tool and never submitted (FR-066):
// the creation form carries a clear password over the authenticated
// connection and auth.Store derives the argon2id hash — there is no field
// anywhere that accepts a hash.

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

	// AccountErr renders the taxonomized failure of an account-lifecycle
	// action inline, above the accounts table (TBY-AUTH-008 to
	// TBY-AUTH-011); AccountName preserves the rejected creation input.
	AccountErr  *ErrView
	AccountName string
	// Self is the acting administrator's own login, so the table can mark
	// its row and keep the operator from mistaking it for someone else's.
	Self string
}

// adminScreenData snapshots the store for the page. A nil store (FR-075
// authentication override) renders empty tables: the permanent banner
// already explains that no account gate exists.
func (u *UI) adminScreenData(r *http.Request) *adminData {
	d := &adminData{}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		d.Self = id.Name
	}
	if st := u.authn.Store; st != nil {
		d.Accounts = st.Accounts()
		d.Tokens = st.Tokens()
	}
	return d
}

// adminAccounts serves GET /admin/accounts.
func (u *UI) adminAccounts(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "admin-accounts", u.adminScreenData(r))
}

// adminAccountCreate serves POST /admin/accounts (FR-073): create a local
// account. The tool derives the argon2id hash from the submitted password
// (FR-066) — the form has no hash field, by design. Success and failure
// are both audited (FR-094): a refused account creation on an admin
// surface is exactly the kind of attempt an operator wants to see.
func (u *UI) adminAccountCreate(w http.ResponseWriter, r *http.Request) {
	st, ok := u.accountStore(w, r)
	if !ok {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	name := strings.TrimSpace(r.PostFormValue("name"))
	role, roleErr := auth.ParseRole(r.PostFormValue("role"))
	password := r.PostFormValue("password")

	if name == "" || roleErr != nil || password == "" || password != r.PostFormValue("confirm") {
		u.auditAccount(r, audit.ActionAccountCreate, id.Name, name, audit.OutcomeFailure)
		u.renderAccountsError(w, r, name, taxonomy.New(taxonomy.CodeAccountInvalid, nil))
		return
	}
	if err := st.AddAccount(name, role, password, u.now()); err != nil {
		u.auditAccount(r, audit.ActionAccountCreate, id.Name, name, audit.OutcomeFailure)
		if errors.Is(err, auth.ErrExists) {
			u.renderAccountsError(w, r, name,
				taxonomy.New(taxonomy.CodeAccountExists, taxonomy.Params{"name": name}))
			return
		}
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	u.auditAccount(r, audit.ActionAccountCreate, id.Name, name, audit.OutcomeSuccess)
	u.renderAccountsToast(w, r, "admin.account_created", name)
}

// adminAccountDelete serves POST /admin/accounts/delete (FR-073). The
// last-administrator refusal lives in the store, under its lock: this
// handler only translates it (TBY-AUTH-011). Live sessions of the deleted
// account are closed here — the store owns the file, the UI owns the
// session table, and an account that no longer exists must not keep
// browsing on a session opened before its removal.
func (u *UI) adminAccountDelete(w http.ResponseWriter, r *http.Request) {
	st, ok := u.accountStore(w, r)
	if !ok {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	name := strings.TrimSpace(r.PostFormValue("name"))
	if err := st.DeleteAccount(name); err != nil {
		u.auditAccount(r, audit.ActionAccountDelete, id.Name, name, u.accountOutcome(err))
		u.renderAccountsError(w, r, "", accountError(name, err))
		return
	}
	u.authn.Sessions.DeleteOthers(name, "")
	u.auditAccount(r, audit.ActionAccountDelete, id.Name, name, audit.OutcomeSuccess)
	u.renderAccountsToast(w, r, "admin.account_deleted", name)
}

// adminAccountRole serves POST /admin/accounts/role (FR-074). Demoting the
// last administrator is refused by the store (TBY-AUTH-011). Every session
// of the account is closed on success, the acting one included when an
// admin demotes itself: a session carries the role it was opened with, so
// leaving it alive would keep granting a role the account no longer holds.
func (u *UI) adminAccountRole(w http.ResponseWriter, r *http.Request) {
	st, ok := u.accountStore(w, r)
	if !ok {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	name := strings.TrimSpace(r.PostFormValue("name"))
	role, err := auth.ParseRole(r.PostFormValue("role"))
	if err != nil {
		u.auditAccount(r, audit.ActionAccountRole, id.Name, name, audit.OutcomeFailure)
		u.renderAccountsError(w, r, "", taxonomy.New(taxonomy.CodeAccountInvalid, nil))
		return
	}
	if err := st.SetRole(name, role); err != nil {
		u.auditAccount(r, audit.ActionAccountRole, id.Name, name, u.accountOutcome(err))
		u.renderAccountsError(w, r, "", accountError(name, err))
		return
	}
	u.authn.Sessions.DeleteOthers(name, "")
	u.auditAccount(r, audit.ActionAccountRole, id.Name, name, audit.OutcomeSuccess)
	u.renderAccountsToast(w, r, "admin.account_role_changed", name)
}

// accountStore returns the account store, or renders the FR-075 override's
// internal error. Shared by every handler of this screen.
func (u *UI) accountStore(w http.ResponseWriter, r *http.Request) (*auth.Store, bool) {
	if st := u.authn.Store; st != nil {
		return st, true
	}
	u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
		WithCause(errors.New("no account store: authentication is disabled (FR-075)")))
	return nil, false
}

// accountError maps a store error onto its catalog entry. Unknown errors
// keep the generic internal code: inventing a message per storage failure
// is how taxonomies rot.
func accountError(name string, err error) *taxonomy.Error {
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		return taxonomy.New(taxonomy.CodeLastAdmin, taxonomy.Params{"name": name})
	case errors.Is(err, auth.ErrNotFound):
		return taxonomy.New(taxonomy.CodeAccountUnknown, taxonomy.Params{"name": name})
	default:
		return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
}

// accountOutcome distinguishes the two negative outcomes of the FR-094
// schema: a policy barrier refused the action (denied), or it simply did
// not work (failure). The last-admin invariant is a barrier.
func (u *UI) accountOutcome(err error) string {
	if errors.Is(err, auth.ErrLastAdmin) {
		return audit.OutcomeDenied
	}
	return audit.OutcomeFailure
}

// auditAccount emits one FR-094 account-lifecycle record: the acting
// administrator is the actor, the managed account the target.
func (u *UI) auditAccount(r *http.Request, action, actor, target, outcome string) {
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, Action: action, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// renderAccountsError re-renders the screen with the taxonomized block
// inline and the entry's real HTTP status (the screen's failure pattern).
func (u *UI) renderAccountsError(w http.ResponseWriter, r *http.Request, name string, e *taxonomy.Error) {
	ev := errView(requestLang(r), e)
	d := u.adminScreenData(r)
	d.AccountErr, d.AccountName = ev, name
	u.renderAdmin(w, r, ev.Status, d)
}

// renderAccountsToast re-renders the screen with the confirmation toast.
func (u *UI) renderAccountsToast(w http.ResponseWriter, r *http.Request, key, name string) {
	v := u.render.view(r, u.adminScreenData(r))
	v.Toasts = append(v.Toasts, v.T(key, "Name", name))
	u.render.render(w, r, "admin-accounts", http.StatusOK, v)
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
		d := u.adminScreenData(r)
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
			d := u.adminScreenData(r)
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
	d := u.adminScreenData(r)
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
	v := u.render.view(r, u.adminScreenData(r))
	v.Toasts = append(v.Toasts, v.T("admin.token_revoked", "Name", name))
	u.render.render(w, r, "admin-accounts", http.StatusOK, v)
}

// renderAdmin re-renders the page with a non-2xx status (form validation).
func (u *UI) renderAdmin(w http.ResponseWriter, r *http.Request, status int, d *adminData) {
	u.render.render(w, r, "admin-accounts", status, u.render.view(r, d))
}
