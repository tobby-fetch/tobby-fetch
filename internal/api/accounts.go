// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Account and token endpoints (FR-060): the strict mirror of the
// /admin/accounts screen (FR-061, UI-SPEC §5.9), admin-gated (ADR-0009).
// A token secret exists exactly once, in the 201 body that created it
// (FR-072); listings never carry secrets or hashes. Every mutation emits
// an FR-094 audit event with the authenticated identity and the real
// network origin.

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// RegisterAccounts mounts the account and token endpoints on the API
// surface. store may be nil under the FR-075 authentication override; the
// endpoints then answer the taxonomized internal error instead of
// panicking.
func RegisterAccounts(a *API, store *auth.Store) {
	acc := &accountsAPI{api: a, store: store}
	a.Handle("GET /api/v1/accounts", a.RequireRole(auth.RoleAdmin, acc.listAccounts))
	// Account lifecycle (FR-073, FR-074) — the strict mirror of the
	// /admin/accounts screen actions (FR-061), same rules, same taxonomy
	// codes, same last-administrator refusal.
	a.Handle("POST /api/v1/accounts", a.RequireRole(auth.RoleAdmin, acc.createAccount))
	a.Handle("PATCH /api/v1/accounts/{name}", a.RequireRole(auth.RoleAdmin, acc.updateAccount))
	a.Handle("DELETE /api/v1/accounts/{name}", a.RequireRole(auth.RoleAdmin, acc.deleteAccount))
	a.Handle("GET /api/v1/tokens", a.RequireRole(auth.RoleAdmin, acc.listTokens))
	a.Handle("POST /api/v1/tokens", a.RequireRole(auth.RoleAdmin, acc.createToken))
	a.Handle("POST /api/v1/tokens/{name}/revoke", a.RequireRole(auth.RoleAdmin, acc.revokeToken))
	// Self-service password change (R-34, FR-061 mirror of the /account
	// screen): any authenticated role — no RequireRole floor beyond the
	// surface's own authentication.
	a.Handle("POST /api/v1/account/password", acc.changePassword)
}

type accountsAPI struct {
	api   *API
	store *auth.Store
	// Now injects time in tests; nil means time.Now.
	Now func() time.Time
}

func (c *accountsAPI) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ready guards the nil-store case (FR-075 override: no account file).
func (c *accountsAPI) ready(w http.ResponseWriter, r *http.Request) bool {
	if c.store == nil {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no account store: authentication is disabled (FR-075)")))
		return false
	}
	return true
}

// accountJSON is one account of the listing — never the password hash
// (NFR-015).
type accountJSON struct {
	Name      string     `json:"name"`
	Role      auth.Role  `json:"role"`
	Created   time.Time  `json:"created"`
	LastLogin *time.Time `json:"last_login,omitempty"`
}

// listAccounts serves GET /api/v1/accounts.
func (c *accountsAPI) listAccounts(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	accounts := c.store.Accounts()
	resp := make([]accountJSON, 0, len(accounts))
	for _, a := range accounts {
		item := accountJSON{Name: a.Name, Role: a.Role, Created: a.Created}
		if !a.LastLogin.IsZero() {
			last := a.LastLogin
			item.LastLogin = &last
		}
		resp = append(resp, item)
	}
	c.api.JSON(w, http.StatusOK, map[string]any{"accounts": resp})
}

// createAccountRequest is the POST /api/v1/accounts body. There is no hash
// field, and there will not be one: the tool derives the argon2id hash
// (FR-066). The password travels in clear inside the authenticated
// request, exactly as it does on the screen's form.
type createAccountRequest struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

// createAccount serves POST /api/v1/accounts (FR-073): 201 with the
// created account. Both outcomes are audited (FR-094).
func (c *accountsAPI) createAccount(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeAccountInvalid, nil).WithCause(err))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	role, roleErr := auth.ParseRole(req.Role)
	if req.Name == "" || roleErr != nil || req.Password == "" {
		c.auditAccount(r, audit.ActionAccountCreate, id.Name, req.Name, audit.OutcomeFailure)
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeAccountInvalid, nil))
		return
	}
	if err := c.store.AddAccount(req.Name, role, req.Password, c.now()); err != nil {
		c.auditAccount(r, audit.ActionAccountCreate, id.Name, req.Name, audit.OutcomeFailure)
		c.api.Problem(w, r, accountProblem(req.Name, err))
		return
	}
	c.auditAccount(r, audit.ActionAccountCreate, id.Name, req.Name, audit.OutcomeSuccess)
	// Answer from the stored record, not from the request: the creation
	// timestamp a later GET reports must be the one this response carried.
	acct, _ := c.store.Account(req.Name)
	c.api.JSON(w, http.StatusCreated, map[string]any{
		"account": accountJSON{Name: acct.Name, Role: acct.Role, Created: acct.Created},
	})
}

// updateAccountRequest is the PATCH /api/v1/accounts/{name} body. Role
// only: passwords are changed by their owner through
// /api/v1/account/password (R-34), and on the host with tobby user passwd.
type updateAccountRequest struct {
	Role string `json:"role"`
}

// updateAccount serves PATCH /api/v1/accounts/{name} (FR-074): change an
// account's role. Demoting the last administrator answers TBY-AUTH-011 —
// the same refusal the screen renders. Every session of the account is
// closed on success: a session carries the role it was opened with.
func (c *accountsAPI) updateAccount(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	name := r.PathValue("name")
	var req updateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeAccountInvalid, nil).WithCause(err))
		return
	}
	role, err := auth.ParseRole(req.Role)
	if err != nil {
		c.auditAccount(r, audit.ActionAccountRole, id.Name, name, audit.OutcomeFailure)
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeAccountInvalid, nil).WithCause(err))
		return
	}
	if err := c.store.SetRole(name, role); err != nil {
		c.auditAccount(r, audit.ActionAccountRole, id.Name, name, accountOutcome(err))
		c.api.Problem(w, r, accountProblem(name, err))
		return
	}
	if s := c.api.authn.Sessions; s != nil {
		s.DeleteOthers(name, "")
	}
	c.auditAccount(r, audit.ActionAccountRole, id.Name, name, audit.OutcomeSuccess)
	acct, _ := c.store.Account(name)
	c.api.JSON(w, http.StatusOK, map[string]any{
		"account": accountJSON{Name: acct.Name, Role: acct.Role, Created: acct.Created},
	})
}

// deleteAccount serves DELETE /api/v1/accounts/{name} (FR-073): 204 on
// success. Removing the last administrator answers TBY-AUTH-011.
func (c *accountsAPI) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	name := r.PathValue("name")
	if err := c.store.DeleteAccount(name); err != nil {
		c.auditAccount(r, audit.ActionAccountDelete, id.Name, name, accountOutcome(err))
		c.api.Problem(w, r, accountProblem(name, err))
		return
	}
	if s := c.api.authn.Sessions; s != nil {
		s.DeleteOthers(name, "")
	}
	c.auditAccount(r, audit.ActionAccountDelete, id.Name, name, audit.OutcomeSuccess)
	w.WriteHeader(http.StatusNoContent)
}

// accountProblem maps a store error onto its catalog entry — the same
// mapping the UI applies, so both surfaces answer the same code for the
// same request (FR-061).
func accountProblem(name string, err error) *taxonomy.Error {
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		return taxonomy.New(taxonomy.CodeLastAdmin, taxonomy.Params{"name": name})
	case errors.Is(err, auth.ErrNotFound):
		return taxonomy.New(taxonomy.CodeAccountUnknown, taxonomy.Params{"name": name})
	case errors.Is(err, auth.ErrExists):
		return taxonomy.New(taxonomy.CodeAccountExists, taxonomy.Params{"name": name})
	default:
		return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
}

// accountOutcome distinguishes the FR-094 negative outcomes: a policy
// barrier refused the action (denied), or it did not work (failure).
func accountOutcome(err error) string {
	if errors.Is(err, auth.ErrLastAdmin) {
		return audit.OutcomeDenied
	}
	return audit.OutcomeFailure
}

// auditAccount emits one FR-094 account-lifecycle record: the acting
// administrator is the actor, the managed account the target.
func (c *accountsAPI) auditAccount(r *http.Request, action, actor, target, outcome string) {
	audit.Log(r.Context(), c.api.logger, &audit.Event{
		Actor: actor, Action: action, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// tokenJSON is one token of the listing — never the secret nor its hash.
type tokenJSON struct {
	Name    string    `json:"name"`
	Role    auth.Role `json:"role"`
	Created time.Time `json:"created"`
	Revoked bool      `json:"revoked"`
}

func newTokenJSON(t *auth.Token) tokenJSON {
	return tokenJSON{Name: t.Name, Role: t.Role, Created: t.Created, Revoked: t.Revoked}
}

// listTokens serves GET /api/v1/tokens, revoked included (the mirror shows
// the full lifecycle, UI-SPEC §5.9).
func (c *accountsAPI) listTokens(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	tokens := c.store.Tokens()
	resp := make([]tokenJSON, 0, len(tokens))
	for i := range tokens {
		resp = append(resp, newTokenJSON(&tokens[i]))
	}
	c.api.JSON(w, http.StatusOK, map[string]any{"tokens": resp})
}

// createTokenRequest is the POST /api/v1/tokens body.
type createTokenRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// createToken serves POST /api/v1/tokens: 201 with the clear secret —
// returned exactly once, only its SHA-256 is stored (FR-072). Invalid
// input and duplicate names answer the taxonomized internal error: the
// catalog is frozen this milestone and carries no dedicated
// account-validation code yet; the wrapped cause lands in the logs.
func (c *accountsAPI) createToken(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	role, err := auth.ParseRole(req.Role)
	if req.Name == "" || err != nil {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("a token name and a valid role (viewer, operator, admin) are required")))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	secret, tok, err := c.store.CreateToken(req.Name, role, c.now())
	if err != nil {
		audit.Log(r.Context(), c.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionTokenCreate, Target: req.Name,
			Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
		})
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), c.api.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionTokenCreate, Target: tok.Name,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	c.api.JSON(w, http.StatusCreated, map[string]any{
		"name": tok.Name, "role": tok.Role, "secret": secret,
	})
}

// passwordRequest is the POST /api/v1/account/password body.
type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// changePassword serves POST /api/v1/account/password: the authenticated
// account changes its OWN password — same rules and taxonomy codes as the
// /account screen (FR-061); admins change other accounts through the admin
// surface. API tokens carry no password, so a token caller fails the
// current-password check like any wrong credential. Every attempt emits an
// FR-094 audit event; on success every UI session of the account is closed
// (the API caller holds none to preserve).
func (c *accountsAPI) changePassword(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	fail := func(e *taxonomy.Error) {
		audit.Log(r.Context(), c.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionAccountPasswd, Target: id.Name,
			Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
		})
		c.api.Problem(w, r, e)
	}
	var req passwordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Undecodable body: the taxonomized internal error, per the surface's
		// convention (see createToken); the wrapped cause lands in the logs.
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	// Same check order as the UI handler, so the same request yields the
	// same code on both surfaces (FR-061). The JSON contract carries no
	// confirmation field — repeating the value is a browser-form concern.
	if req.New == "" || req.New == req.Current {
		fail(taxonomy.New(taxonomy.CodePasswordInvalid, nil))
		return
	}
	if _, ok := c.store.VerifyPassword(id.Name, req.Current, c.now()); !ok {
		fail(taxonomy.New(taxonomy.CodePasswordCurrent, nil))
		return
	}
	if err := c.store.SetPassword(id.Name, req.New, c.now()); err != nil {
		fail(taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	// Sessions opened with the old credential must not survive (R-34).
	if s := c.api.authn.Sessions; s != nil {
		s.DeleteOthers(id.Name, "")
	}
	audit.Log(r.Context(), c.api.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionAccountPasswd, Target: id.Name,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// revokeToken serves POST /api/v1/tokens/{name}/revoke: immediate and
// permanent (FR-072). Unknown names answer the shared taxonomized 404.
func (c *accountsAPI) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	name := r.PathValue("name")
	id, _ := auth.IdentityFrom(r.Context())
	if err := c.store.RevokeToken(name); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			c.api.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil).WithCause(err))
			return
		}
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), c.api.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionTokenRevoke, Target: name,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	tokens := c.store.Tokens()
	for i := range tokens {
		if tokens[i].Name == name && tokens[i].Revoked {
			c.api.JSON(w, http.StatusOK, map[string]any{"token": newTokenJSON(&tokens[i])})
			return
		}
	}
	c.api.JSON(w, http.StatusOK, map[string]any{"token": tokenJSON{Name: name, Revoked: true}})
}
