// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
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
	// AuthWindow bounds how long one machine credential counts as already
	// reported for FR-094 authentication-success events; zero means
	// defaultAuthWindow. See recordSuccess for why a window exists at all.
	AuthWindow time.Duration

	// seenMu guards seen. An Authenticator is always used through a
	// pointer (one per instance), so the lock travels with it.
	seenMu sync.Mutex
	// seen maps a credential fingerprint to the instant its success was
	// last reported.
	seen map[string]time.Time
}

// defaultAuthWindow is the deduplication window for successful
// authentications on machine surfaces. One hour: long enough that a CI job
// or a docker pull of a hundred blobs produces one record, short enough
// that a credential still in use tomorrow shows up again in the trail.
const defaultAuthWindow = time.Hour

// seenCap bounds the deduplication table before a sweep. A cap rather than
// a background goroutine: the table is a log-volume optimization, not
// state anyone depends on, and an unbounded map fed by attacker-chosen
// credentials would be a memory sink.
const seenCap = 4096

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
// writes need operator (ADR-0009). Authentication and refusals are audited
// (FR-094); a missing or wrong credential answers 401 with a Basic
// challenge so standard clients prompt for credentials (FR-076).
func (a *Authenticator) Registry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.authenticateAudited(r, audit.SurfaceRegistry)
		if !ok {
			if !credentialPresented(r) {
				// Nothing was verified, so nothing was audited above: the
				// refused access is the record. A wrong credential already
				// has its own auth.authenticate failure — one refusal must
				// not produce two lines.
				a.deny(r, audit.ActionRegistryAccess, audit.OutcomeDenied, "")
			}
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
			a.deny(r, audit.ActionRegistryAccess, audit.OutcomeDenied, id.Name)
			http.Error(w, "insufficient role", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// authenticateAudited resolves the request's identity and emits the FR-094
// authentication events of a machine surface.
//
// What is an event and what is traffic. The OCI and REST surfaces are
// stateless: docker, helm, oras, and any API client re-present the same
// Basic or Bearer credential on every single request — a single `docker
// pull` of a multi-layer image is dozens of them. Auditing each one would
// bury the records an operator actually reads (a refusal, a first sign-in
// from a new address) under thousands of identical lines, which is the
// opposite of what FR-094 is for. So:
//
//   - Every FAILED verification of a PRESENTED credential is audited.
//     Failures are rare in normal operation and each one is the security
//     signal itself; deduplicating them would hide exactly the
//     brute-force pattern the trail must show.
//   - A SUCCESSFUL verification is audited once per credential and client
//     origin per AuthWindow — the machine-surface equivalent of the UI's
//     session.login event. What follows inside the window is traffic, and
//     the operational log (FR-090) already carries it.
//   - A request carrying NO credential at all verifies nothing, so it
//     emits no authentication record: it is the opening move of every
//     standard OCI client, which probes /v2/ bare to collect the
//     challenge, and of every browser fragment poll carrying only a
//     session cookie. The refusal that follows is audited by the caller
//     as an authorization denial, which is what it is.
//
// The FR-075 override emits nothing here: with authentication disabled
// there is no credential to verify, and the override is already audited
// once at startup and shown by a permanent banner.
func (a *Authenticator) authenticateAudited(r *http.Request, surface string) (Identity, bool) {
	if a.Disabled {
		return AnonymousIdentity, true
	}
	if !credentialPresented(r) {
		return Identity{}, false
	}
	id, ok := a.Authenticate(r)
	if !ok {
		audit.Log(r.Context(), a.Logger, &audit.Event{
			Actor:   presentedName(r),
			Action:  audit.ActionAuthenticate,
			Target:  surface,
			Outcome: audit.OutcomeFailure,
			Origin:  ClientOrigin(r),
		})
		return id, false
	}
	if a.recordSuccess(surface, r) {
		audit.Log(r.Context(), a.Logger, &audit.Event{
			Actor:   id.Name,
			Action:  audit.ActionAuthenticate,
			Target:  surface,
			Outcome: audit.OutcomeSuccess,
			Origin:  ClientOrigin(r),
		})
	}
	return id, true
}

// recordSuccess reports whether this success is a new event rather than
// traffic, and marks it seen. The key fingerprints the credential — the
// raw Authorization header is never stored, nor logged (NFR-015) — and
// includes the surface and the client origin, so the same token used from
// a second address produces its own record.
func (a *Authenticator) recordSuccess(surface string, r *http.Request) bool {
	sum := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	key := surface + "|" + ClientOrigin(r) + "|" + hex.EncodeToString(sum[:])
	window := a.AuthWindow
	if window <= 0 {
		window = defaultAuthWindow
	}
	now := a.now()

	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	if a.seen == nil {
		a.seen = make(map[string]time.Time)
	}
	if last, ok := a.seen[key]; ok && now.Sub(last) < window {
		return false
	}
	if len(a.seen) >= seenCap {
		for k, t := range a.seen {
			if now.Sub(t) >= window {
				delete(a.seen, k)
			}
		}
		// Still full: the window is doing no filtering for this traffic
		// mix, so start over rather than grow without bound.
		if len(a.seen) >= seenCap {
			a.seen = make(map[string]time.Time)
		}
	}
	a.seen[key] = now
	return true
}

// credentialPresented reports whether the request carried a credential at
// all — the difference between "tried and failed" and "did not try".
func credentialPresented(r *http.Request) bool {
	return r.Header.Get("Authorization") != ""
}

// presentedName is the account or token name a failed request claimed, for
// the actor field: knowing which name was tried is the whole value of a
// failed-authentication record. Only the name is read — never the secret.
func presentedName(r *http.Request) string {
	if name, _, ok := r.BasicAuth(); ok && name != "" {
		return name
	}
	return "unauthenticated"
}

// deny emits the audit record of an authorization refusal on a machine
// surface — the caller was identified (or not) and the request was
// nevertheless blocked. Permitted requests are not audited here: see
// authenticateAudited for the event-versus-traffic rule.
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
		id, ok := a.authenticateAudited(r, audit.SurfaceAPI)
		if !ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			// A UI session reaching a read-only API path is not a new
			// authentication: session.login already recorded it, and the
			// browser sends the cookie on every fragment poll.
			id, ok = a.sessionIdentity(r)
		}
		if !ok {
			if !credentialPresented(r) {
				a.deny(r, audit.ActionAPIAccess, audit.OutcomeDenied, "")
			}
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
