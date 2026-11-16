// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// Cross-site request forgery (NFR-012, amendment 2026-11-16).
//
// The rule is about AMBIENT authority: a credential the browser attaches
// on its own. The session cookie is one, so every request it authenticates
// whose method is not safe (GET, HEAD, OPTIONS) must prove it was built
// by a page of this instance — the per-session token — and, as defense in
// depth, must not come from another origin. Basic and Bearer credentials
// are attached by the client deliberately and are exempt; so are the
// federated callbacks under /auth/, protected by their own state and
// InResponseTo checks (FR-070, FR-071). Under the FR-075 override there
// is no cookie at all, yet the instance still acts on whoever reaches it
// — the classic router-CSRF shape — so the origin check applies there
// too, on every unsafe request that carries no explicit credential.

// CSRFHeader carries the session's anti-forgery token on a request that
// is not a form submission: an API call made from a signed-in page. The
// UI forms carry the same token in their "csrf" field.
const CSRFHeader = "X-CSRF-Token"

// SafeMethod reports whether m is one of the methods NFR-012 exempts.
// None of the routes the instance registers changes state on them.
func SafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// SameOrigin reports whether r may be a request built by a page of this
// instance, from the browser's own provenance headers. It never accepts a
// request the browser itself labels as coming from elsewhere; it accepts
// a request that carries no provenance at all (a non-browser client, or a
// browser too old to send either header), because the token check that
// accompanies it is the protection, and this one is depth.
//
// Order of the signals, and why:
//
//  1. Sec-Fetch-Site, when present, is the browser's own verdict on the
//     initiator versus the target URL, computed before any proxy sees the
//     request: "same-origin" and "none" (a typed address, a bookmark) pass,
//     "same-site" and "cross-site" are refused. It is immune to the
//     reverse-proxy pitfall below, which is why it is consulted first.
//     Browsers send it to potentially trustworthy origins only (HTTPS,
//     localhost), so a plain-HTTP lab instance relies on step 2.
//  2. Origin, when it names an origin, is compared with the request's own
//     host: its host[:port] against the Host header or, behind a reverse
//     proxy that rewrites Host, against X-Forwarded-Host. The scheme is
//     NOT compared: behind a TLS-terminating proxy the browser says https
//     while the listener sees plain HTTP. Default ports are normalized
//     (":80", ":443"). Trusting X-Forwarded-Host here is sound although it
//     is client-settable: the attacker of a forgery does not choose the
//     headers of the VICTIM's browser — a cross-origin page cannot set
//     X-Forwarded-Host without a CORS preflight this instance never
//     grants — and an attacker forging their own request gains nothing,
//     they hold no victim cookie.
//  3. "Origin: null" is no origin at all: browsers send it for every form
//     POST of a page served with Referrer-Policy: no-referrer (NFR-022),
//     as well as for sandboxed or opaque initiators. It is treated as
//     absent, falling back to the outcome of step 1.
func SameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		// "same-site", "cross-site", or a value no browser sends.
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	want := normalizeHost(u.Host)
	if want == normalizeHost(r.Host) {
		return true
	}
	for _, v := range r.Header.Values("X-Forwarded-Host") {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" && normalizeHost(h) == want {
				return true
			}
		}
	}
	return false
}

// normalizeHost lowercases a host[:port] and drops a default port, so
// "Tobby.example:443" and "tobby.example" compare equal. Both default
// ports are dropped whatever the scheme: the comparison is about the
// host, the scheme is not compared (see SameOrigin).
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if s, ok := strings.CutSuffix(h, ":443"); ok {
		return s
	}
	if s, ok := strings.CutSuffix(h, ":80"); ok {
		return s
	}
	return h
}

// ambientCredentialless reports whether r carries no explicit machine
// credential — the only requests the CSRF rule has to look at.
func ambientCredentialless(r *http.Request) bool {
	return r.Header.Get("Authorization") == ""
}

// ForgeryExposed reports whether r is a request the NFR-012 checks must
// examine on a surface protected by sessions or by the FR-075 override:
// an unsafe method, and no explicit credential.
func ForgeryExposed(r *http.Request) bool {
	return !SafeMethod(r.Method) && ambientCredentialless(r)
}

// authMarkKey scopes the listener's "this request was authenticated"
// marker in a request context.
type authMarkKey struct{}

// WithAuthMark returns a context carrying a fresh marker that
// WithIdentity sets when an identity is attached to a request derived
// from it. The listener's security-header middleware (NFR-022) installs
// it before routing and reads it when the response is written: every
// surface attaches its identity through WithIdentity, so this one funnel
// tells the middleware which responses answer an authenticated request
// and must carry Cache-Control: no-store — without the middleware having
// to re-implement any surface's authentication.
func WithAuthMark(ctx context.Context) (context.Context, *atomic.Bool) {
	m := new(atomic.Bool)
	return context.WithValue(ctx, authMarkKey{}, m), m
}

// markAuthenticated sets the marker of ctx, if any.
func markAuthenticated(ctx context.Context) {
	if m, ok := ctx.Value(authMarkKey{}).(*atomic.Bool); ok {
		m.Store(true)
	}
}

// maxPresentedName bounds a name a client claims before it is verified —
// the login form's username, the Basic user name. No account or token
// name comes close; anything longer is not a name.
const maxPresentedName = 256

// ValidPresentedName reports whether a claimed, not yet verified, account
// or token name is fit to be checked and recorded (NFR-023): at most 256
// bytes of valid UTF-8, no control character. The name of a failed
// attempt is the actor of its audit record; without this bound an
// unauthenticated client chose up to the whole request body as a log
// line, per attempt.
func ValidPresentedName(name string) bool {
	if len(name) > maxPresentedName || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// InvalidActor is the audit actor of an attempt whose claimed name failed
// ValidPresentedName: the record keeps the attempt, not the payload.
const InvalidActor = "invalid-name"
