// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// ctxKey scopes the identity in a request context.
type ctxKey struct{}

// WithIdentity attaches id to ctx.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// IdentityFrom returns the authenticated identity of the request, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// AnonymousIdentity is the identity used under the FR-075 authentication
// override: full access, explicitly named, visible in every audit record.
var AnonymousIdentity = Identity{Name: "anonymous", Role: RoleAdmin, Anonymous: true}

// Authenticator authenticates machine surfaces (the /api/v1 API and the
// /v2/ registry) with the Basic scheme — account:password or
// token-name:token-secret — or a bearer token (FR-072, FR-076). The web UI
// authenticates with sessions, wired in the ui package.
type Authenticator struct {
	Store    *Store
	Sessions *Sessions
	// Disabled short-circuits every check under the FR-075 override.
	Disabled bool
	Logger   *slog.Logger
	// Now injects time in tests; nil means time.Now.
	Now func() time.Time
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Authenticate resolves the request's identity from Basic or Bearer
// credentials. It does not write a response.
func (a *Authenticator) Authenticate(r *http.Request) (Identity, bool) {
	if a.Disabled {
		return AnonymousIdentity, true
	}
	header := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(header, "Bearer "):
		secret := strings.TrimPrefix(header, "Bearer ")
		if tok, ok := a.Store.VerifyToken(secret); ok {
			return Identity{Name: tok.Name, Role: tok.Role, Token: true}, true
		}
	case strings.HasPrefix(header, "Basic "):
		name, pass, ok := r.BasicAuth()
		if !ok {
			return Identity{}, false
		}
		// A token secret is accepted as the Basic password so docker/helm
		// login works with tokens too (FR-076): the token's own name wins.
		if strings.HasPrefix(pass, tokenPrefix) {
			if tok, ok := a.Store.VerifyToken(pass); ok {
				return Identity{Name: tok.Name, Role: tok.Role, Token: true}, true
			}
			return Identity{}, false
		}
		if acct, ok := a.Store.VerifyPassword(name, pass, a.now()); ok {
			return Identity{Name: acct.Name, Role: acct.Role}, true
		}
	}
	return Identity{}, false
}

// Registry protects the embedded OCI registry (/v2/): reads need viewer,
// writes need operator (ADR-0009). Failures are audited (FR-094) and answer
// 401 with a Basic challenge so standard clients prompt for credentials.
func (a *Authenticator) Registry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Authenticate(r)
		if !ok {
			a.deny(r, "registry.access", audit.OutcomeDenied, "")
			w.Header().Set("WWW-Authenticate", `Basic realm="tobby"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		floor := RoleViewer
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			floor = RoleOperator
		}
		if !id.Role.AtLeast(floor) {
			a.deny(r, "registry.access", audit.OutcomeDenied, id.Name)
			http.Error(w, "insufficient role", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// deny emits the audit record of a refused machine-surface access. Success
// is not audited per request — the volume would drown the trail; session
// creations and token lifecycle events carry the positive side (R-12 note:
// full authentication audit coverage lands at milestone 4).
func (a *Authenticator) deny(r *http.Request, action, outcome, actor string) {
	if actor == "" {
		actor = "unauthenticated"
	}
	audit.Log(r.Context(), a.Logger, &audit.Event{
		Actor:   actor,
		Action:  action,
		Target:  r.URL.Path,
		Outcome: outcome,
		Origin:  ClientOrigin(r),
	})
}

// ClientOrigin extracts the client network origin for audit records
// (FR-094: the real network origin — the peer address, not client-supplied
// forwarding headers, which are trivially spoofable).
func ClientOrigin(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// API protects the /api/v1 surface: Basic or Bearer (FR-072). A valid UI
// session additionally authenticates GET and HEAD — read-only, so no CSRF
// exposure — which keeps browser-clickable API links (log download,
// OpenAPI document) working from a signed-in page: one URL serves both
// surfaces (FR-061). Mutating API calls always require Basic or Bearer.
// Failures answer RFC 9457 problem documents in the negotiated language —
// the same taxonomy entry the UI renders. Role enforcement is
// per-endpoint, in the api package.
func (a *Authenticator) API(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Authenticate(r)
		if !ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			id, ok = a.sessionIdentity(r)
		}
		if !ok {
			a.deny(r, "api.access", audit.OutcomeDenied, "")
			w.Header().Set("WWW-Authenticate", `Basic realm="tobby", Bearer`)
			taxonomy.WriteProblem(w, r.Header.Get("Accept-Language"),
				taxonomy.New(taxonomy.CodeAuthFailed, nil))
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// sessionIdentity resolves a UI session cookie into an identity, for the
// read-only API paths.
func (a *Authenticator) sessionIdentity(r *http.Request) (Identity, bool) {
	if a.Sessions == nil {
		return Identity{}, false
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return Identity{}, false
	}
	sess, ok := a.Sessions.Get(c.Value, a.now())
	if !ok {
		return Identity{}, false
	}
	return Identity{Name: sess.Account, Role: sess.Role}, true
}
