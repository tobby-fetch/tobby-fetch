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
