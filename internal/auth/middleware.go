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
	// Observer receives the tobby_auth_* metric events (R-14, R-20). Nil
	// observes nothing.
	Observer Observer
	// Federated reports whether a federated method (OIDC, SAML) is
	// enabled on this instance. A local-account sign-in while it is is
	// break-glass use, audited on its own (FR-078). Nil means no.
	Federated func() bool

	// seenMu guards seen. An Authenticator is always used through a
	// pointer (one per instance), so the lock travels with it.
	seenMu sync.Mutex
	// seen maps a credential fingerprint to the instant its success was
	// last reported.
	seen map[string]time.Time

	// failures budgets failed verifications per client origin (v0.4.2
	// hardening, ratelimit.go): each failure costs an argon2id
	// computation, so unbounded failures are a DoS lever. One limiter
	// per Authenticator — the instance has one — so the /v2/, /api/v1,
	// /files/ and UI login surfaces share each origin's budget: a brute
	// force does not get a fresh allowance per surface.
	failures failureLimiter
}

// Observer is the metric side of authentication (FR-091, the
// tobby_auth_* families). The auth package only states what happened; the
// instance decides how it is counted.
type Observer interface {
	// Attempt counts one credential check or sign-in by method and
	// outcome (OutcomeSuccess, OutcomeFailure, OutcomeExpired).
	Attempt(method Method, outcome string)
	// SessionExpired counts one UI session ended by expiry.
	SessionExpired(reason ExpiryReason)
	// LocalUnderFederation counts one local-account sign-in while a
	// federated method is enabled.
	LocalUnderFederation()
}

// The outcomes of the tobby_auth_attempts_total metric. Closed set.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeExpired = "expired"
)

// SurfaceFiles names the FileSet serving surface (/files/) as the target
// of its authentication events.
const SurfaceFiles = "files"

// observe forwards one attempt to the observer, if any.
func (a *Authenticator) observe(method Method, outcome string) {
	if a.Observer != nil {
		a.Observer.Attempt(method, outcome)
	}
}

// Observe counts a sign-in a surface outside this package performed (the
// UI form, the federated callbacks).
func (a *Authenticator) Observe(method Method, outcome string) { a.observe(method, outcome) }

// FederationEnabled reports whether a federated method is enabled.
func (a *Authenticator) FederationEnabled() bool {
	return a.Federated != nil && a.Federated()
}

// LocalUnderFederation records a local-account sign-in while federation is
// enabled — the dedicated FR-078 event, filterable on its own: on an
// instance whose people sign in through the directory, a local account in
// use is break-glass (the identity provider is down, or someone bypasses
// it), and the trail must show it apart from ordinary traffic. A no-op
// when no federated method is enabled. surface is the target: "ui" for
// the sign-in form, the machine surface otherwise.
func (a *Authenticator) LocalUnderFederation(ctx context.Context, account, surface, origin string) {
	if !a.FederationEnabled() {
		return
	}
	if a.Observer != nil {
		a.Observer.LocalUnderFederation()
	}
	audit.Log(ctx, a.Logger, &audit.Event{
		Actor:   account,
		Action:  audit.ActionLocalUnderFederation,
		Target:  surface,
		Outcome: audit.OutcomeSuccess,
		Origin:  origin,
	})
}

// Check is the audited credential check of a machine surface, for the
// surfaces wired outside this package (the /files/ gate): same events,
// same throttle, same verdicts as /v2/ and /api/v1.
func (a *Authenticator) Check(r *http.Request, surface string) (Identity, Verdict) {
	return a.authenticateAudited(r, surface)
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
	id, res := a.authenticate(r)
	return id, res.state == TokenValid
}

// authResult is what one credential check found. state is TokenValid for
// any accepted credential, password included; token is set whenever a
// presented token secret matched a token — valid, revoked or expired —
// so the refusal can be named and audited.
type authResult struct {
	state TokenState
	token *Token
	// method is the method the presented credential claimed.
	method Method
}

func (a *Authenticator) authenticate(r *http.Request) (Identity, authResult) {
	if a.Disabled {
		return AnonymousIdentity, authResult{state: TokenValid}
	}
	refused := authResult{state: TokenUnknown, method: MethodBasic}
	header := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(header, "Bearer "):
		return a.checkToken(strings.TrimPrefix(header, "Bearer "))
	case strings.HasPrefix(header, "Basic "):
		name, pass, ok := r.BasicAuth()
		if !ok {
			return Identity{}, refused
		}
		// A token secret is accepted as the Basic password so docker/helm
		// login works with tokens too (FR-076): the token's own name wins.
		if strings.HasPrefix(pass, tokenPrefix) {
			return a.checkToken(pass)
		}
		if acct, ok := a.Store.VerifyPassword(name, pass, a.now()); ok {
			return Identity{Name: acct.Name, Role: acct.Role, Method: MethodBasic},
				authResult{state: TokenValid, method: MethodBasic}
		}
	}
	return Identity{}, refused
}

// checkToken verifies a presented token secret.
func (a *Authenticator) checkToken(secret string) (Identity, authResult) {
	tok, state := a.Store.VerifyToken(secret, a.now())
	res := authResult{state: state, method: MethodToken}
	if state != TokenUnknown {
		res.token = &tok
	}
	if state != TokenValid {
		return Identity{}, res
	}
	return Identity{Name: tok.Name, Role: tok.Role, Token: true, Method: MethodToken}, res
}

// Verdict is the outcome of one audited authentication attempt: it
// exists so the surfaces can tell a throttled origin (429, Retry-After)
// from a wrong credential (401, challenge) — collapsing both into a
// boolean is exactly what let the throttle-free path ship.
type Verdict int

const (
	// VerdictOK is a valid credential; the identity is usable.
	VerdictOK Verdict = iota
	// VerdictNone is no credential was presented at all.
	VerdictNone
	// VerdictFailed is a credential was presented and refused.
	VerdictFailed
	// VerdictThrottled is the origin exhausted its failure budget; the
	// credential was NOT verified (that is the point — no argon2id ran).
	VerdictThrottled
	// VerdictExpired is a token past its expiry (R-14) — refused with its
	// own code, TBY-AUTH-013, because the fix is a new token, not a
	// corrected one.
	VerdictExpired
)

// FailureAllowed reports whether origin still has failed-authentication
// budget (ratelimit.go). Surfaces that verify credentials outside
// authenticateAudited — the UI login form, the /files/ gate — call it
// BEFORE the verification, because the whole point is to skip the
// argon2id computation for an origin that keeps failing.
func (a *Authenticator) FailureAllowed(origin string) bool {
	return a.failures.allowed(origin, a.now())
}

// RecordFailure spends one unit of origin's failure budget after a
// verification those same surfaces performed and saw fail. Logged once
// at the transition into throttling — not per throttled request, which
// would hand the attacker a log-flooding lever.
func (a *Authenticator) RecordFailure(origin string) {
	if a.failures.record(origin, a.now()) {
		a.logThrottled(origin)
	}
}

// logThrottled emits the one operational log line (FR-090) marking an
// origin's transition into throttling. Not an audit record: the failed
// attempts that led here are each audited already (FR-094), and the
// throttle verifies nothing.
func (a *Authenticator) logThrottled(origin string) {
	if a.Logger == nil {
		return
	}
	a.Logger.LogAttrs(context.Background(), slog.LevelWarn,
		"authentication failures throttled",
		slog.String("origin", origin),
		slog.String("retry_after", RetryAfter+"s"))
}

// throttled answers a request from an exhausted origin on the plain-text
// surfaces (registry, files): 429 with the Retry-After the refill period
// justifies. The API surface answers its RFC 9457 form instead.
func throttled(w http.ResponseWriter) {
	w.Header().Set("Retry-After", RetryAfter)
	http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
}

// Registry protects the embedded OCI registry (/v2/): reads need viewer,
// writes need operator (ADR-0009). Authentication and refusals are audited
// (FR-094); a missing or wrong credential answers 401 with a Basic
// challenge so standard clients prompt for credentials (FR-076); an
// origin over its failure budget answers 429 before anything is verified.
func (a *Authenticator) Registry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, v := a.authenticateAudited(r, audit.SurfaceRegistry)
		if v == VerdictThrottled {
			throttled(w)
			return
		}
		if v == VerdictExpired {
			// Plain-text surface: the same 401 and challenge as a wrong
			// credential — standard clients prompt again — with the
			// reason in the body, which `docker login` prints.
			w.Header().Set("WWW-Authenticate", `Basic realm="tobby"`)
			http.Error(w, "the token has expired (TBY-AUTH-013)", http.StatusUnauthorized)
			return
		}
		if v != VerdictOK {
			if v == VerdictNone {
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
//
// The failure budget is consulted BEFORE the verification (v0.4.2
// hardening): the throttle exists to stop the argon2id spend, so an
// exhausted origin must not reach Authenticate at all. Throttled
// requests are not audited individually — the failures that exhausted
// the budget each were, and per-request records would let the attacker
// keep writing the trail; the transition is logged once (logThrottled).
func (a *Authenticator) authenticateAudited(r *http.Request, surface string) (Identity, Verdict) {
	if a.Disabled {
		return AnonymousIdentity, VerdictOK
	}
	if !credentialPresented(r) {
		return Identity{}, VerdictNone
	}
	origin := ClientOrigin(r)
	if !a.failures.allowed(origin, a.now()) {
		return Identity{}, VerdictThrottled
	}
	id, res := a.authenticate(r)
	if res.state != TokenValid {
		a.RecordFailure(origin)
		actor := presentedName(r)
		if res.token != nil {
			// A matched token is named: the refusal of a revoked or expired
			// token is a lifecycle event of THAT token (FR-072 amendment).
			actor = res.token.Name
		}
		if res.state == TokenExpired {
			a.observe(res.method, OutcomeExpired)
			audit.Log(r.Context(), a.Logger, &audit.Event{
				Actor:   actor,
				Action:  audit.ActionTokenExpire,
				Target:  res.token.Name,
				Outcome: audit.OutcomeDenied,
				Origin:  origin,
			})
			return Identity{}, VerdictExpired
		}
		a.observe(res.method, OutcomeFailure)
		audit.Log(r.Context(), a.Logger, &audit.Event{
			Actor:   actor,
			Action:  audit.ActionAuthenticate,
			Target:  surface,
			Outcome: audit.OutcomeFailure,
			Origin:  origin,
		})
		return Identity{}, VerdictFailed
	}
	a.observe(id.Method, OutcomeSuccess)
	if a.recordSuccess(surface, r) {
		audit.Log(r.Context(), a.Logger, &audit.Event{
			Actor:   id.Name,
			Action:  audit.ActionAuthenticate,
			Target:  surface,
			Outcome: audit.OutcomeSuccess,
			Origin:  origin,
		})
		if id.Method == MethodBasic {
			// Same event-versus-traffic rule as the record above: once per
			// credential, origin and window.
			a.LocalUnderFederation(r.Context(), id.Name, surface, origin)
		}
	}
	return id, VerdictOK
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
		id, v := a.authenticateAudited(r, audit.SurfaceAPI)
		if v == VerdictThrottled {
			// Same taxonomy entry the UI renders for the same condition
			// (FR-061 parity), in the RFC 9457 form of this surface. No
			// session fallback: a throttled origin presented a credential.
			w.Header().Set("Retry-After", RetryAfter)
			taxonomy.WriteProblem(w, r.Header.Get("Accept-Language"),
				taxonomy.New(taxonomy.CodeAuthRateLimited, nil))
			return
		}
		if v == VerdictExpired {
			w.Header().Set("WWW-Authenticate", `Basic realm="tobby", Bearer`)
			taxonomy.WriteProblem(w, r.Header.Get("Accept-Language"),
				taxonomy.New(taxonomy.CodeTokenExpired, nil))
			return
		}
		ok := v == VerdictOK
		sessState := SessionUnknown
		if !ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			// A UI session reaching a read-only API path is not a new
			// authentication: session.login already recorded it, and the
			// browser sends the cookie on every fragment poll.
			id, sessState = a.sessionIdentity(r)
			ok = sessState == SessionLive
		}
		if !ok {
			if v == VerdictNone {
				a.deny(r, audit.ActionAPIAccess, audit.OutcomeDenied, "")
			}
			if sessState == SessionIdle || sessState == SessionLifetime {
				// The cookie was a session that just expired: say so, with
				// the code the UI uses for the same condition (FR-061).
				taxonomy.WriteProblem(w, r.Header.Get("Accept-Language"),
					taxonomy.New(taxonomy.CodeSessionExpired, nil))
				return
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
// read-only API paths. The lookup renews the session like any other
// authenticated request (R-14).
func (a *Authenticator) sessionIdentity(r *http.Request) (Identity, SessionState) {
	if a.Sessions == nil {
		return Identity{}, SessionUnknown
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return Identity{}, SessionUnknown
	}
	sess, st := a.Sessions.Lookup(c.Value, ClientOrigin(r), a.now())
	if st != SessionLive {
		return Identity{}, st
	}
	return Identity{Name: sess.Account, Role: sess.Role, Method: sess.Method}, SessionLive
}
