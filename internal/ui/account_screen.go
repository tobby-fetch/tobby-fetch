// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Account screen (R-34, FR-061): the signed-in user's profile card and the
// self-service password change, open to every authenticated role — the
// admin surface (/admin/accounts) manages OTHER accounts and is untouched.
// Failures re-render the page with the taxonomized block inline
// (admin-screen pattern); every attempt emits an FR-094 audit event with
// the session identity and the real network origin. On success every other
// session of the account is closed — the one that performed the change
// survives. The page is excluded from the htmx history cache (ADR-0015 §5).

package ui

import (
	"errors"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// accountData feeds the /account page.
type accountData struct {
	// Err re-renders the password form with the taxonomized failure
	// (TBY-AUTH-006 / TBY-AUTH-007) inline; the profile card comes from the
	// View's Identity.
	Err *ErrView
}

// accountScreen serves GET /account: profile and password form, every
// authenticated role including viewer.
func (u *UI) accountScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "account", &accountData{})
}

// accountPassword serves POST /account/password: verify the current
// password, validate the new one, replace the hash, close the other
// sessions, confirm with a toast. Runs behind RequireSession, so CSRF is
// already checked (NFR-012).
func (u *UI) accountPassword(w http.ResponseWriter, r *http.Request) {
	st := u.authn.Store
	if st == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no account store: authentication is disabled (FR-075)")))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	current := r.PostFormValue("current")
	next := r.PostFormValue("new")

	// Cheap shape checks first, the argon2id verification after — same
	// order as the API mirror so the same request yields the same code
	// (FR-061). The comparison uses the submitted current value: nothing
	// about the stored password leaks before it is verified.
	if next == "" || next == current || next != r.PostFormValue("confirm") {
		u.auditPassword(r, id.Name, audit.OutcomeFailure)
		u.renderAccountError(w, r, taxonomy.New(taxonomy.CodePasswordInvalid, nil))
		return
	}
	if _, ok := st.VerifyPassword(id.Name, current, u.now()); !ok {
		u.auditPassword(r, id.Name, audit.OutcomeFailure)
		u.renderAccountError(w, r, taxonomy.New(taxonomy.CodePasswordCurrent, nil))
		return
	}
	if err := st.SetPassword(id.Name, next, u.now()); err != nil {
		u.auditPassword(r, id.Name, audit.OutcomeFailure)
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	// Sessions opened with the old credential must not survive; the session
	// that performed the change does (R-34).
	if sess, ok := sessionFrom(r.Context()); ok {
		u.authn.Sessions.DeleteOthers(sess.Account, sess.ID)
	}
	u.auditPassword(r, id.Name, audit.OutcomeSuccess)
	v := u.render.view(r, &accountData{})
	v.Toasts = append(v.Toasts, v.T("account.pw_changed"))
	u.render.render(w, r, "account", http.StatusOK, v)
}

// auditPassword emits the FR-094 record of one password-change attempt:
// actor and target are the session identity — self-service only.
func (u *UI) auditPassword(r *http.Request, name, outcome string) {
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: name, Action: audit.ActionAccountPasswd, Target: name,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// renderAccountError re-renders the page with the taxonomized block inline
// and the entry's real HTTP status (admin-screen pattern).
func (u *UI) renderAccountError(w http.ResponseWriter, r *http.Request, e *taxonomy.Error) {
	ev := errView(requestLang(r), e)
	u.render.render(w, r, "account", ev.Status, u.render.view(r, &accountData{Err: ev}))
}
