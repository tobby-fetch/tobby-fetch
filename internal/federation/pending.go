// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// A pending sign-in is the server-side half of a federated round trip:
// what Tobby must remember between sending the browser to the identity
// provider and receiving it back — the OIDC state, nonce and PKCE
// verifier, the SAML request identifier, and where to land afterwards.
//
// It is held here, in memory, and never in the browser: a verifier or a
// nonce kept client-side would be handed to whoever holds the cookie. The
// browser only carries a binding secret, in an HttpOnly cookie scoped to
// /auth/, whose digest the entry records. A callback that arrives without
// the browser that started the sign-in — a login-CSRF, a code or response
// pasted into another browser — finds no binding and is refused.
const (
	// pendingTTL bounds one round trip: long enough for a person to type
	// a password and a second factor at the provider, short enough that a
	// captured state is worthless soon after.
	pendingTTL = 10 * time.Minute
	// pendingCap bounds the table, which unauthenticated requests fill
	// (every GET /auth/oidc/login adds an entry), so it is never a memory
	// sink.
	pendingCap = 4096
	// pendingPerOrigin bounds what one network origin holds in the table:
	// beyond it, that origin's own oldest sign-in goes. With the start
	// rate bounded per origin too (startBurst, startRefill), one origin
	// filling the table evicts only its own entries — never the sign-ins
	// other people have in flight (B-049). When the table is full anyway,
	// the entry evicted is the oldest of the origin holding the most.
	pendingPerOrigin = 32
	// startBurst and startRefill bound how fast one origin may start
	// federated sign-ins: a person clicks the button a few times, a flood
	// is refused with TBY-AUTH-018 before it costs memory or a request to
	// the identity provider.
	startBurst  = 30
	startRefill = 2 * time.Second
)

// pending is one sign-in in flight.
type pending struct {
	method auth.Method
	// binding is the SHA-256 of the browser binding secret.
	binding [sha256.Size]byte
	// next is the sanitized post-login destination.
	next string
	// OIDC.
	nonce    string
	verifier string
	// SAML: the AuthnRequest ID the response must answer (InResponseTo).
	requestID string
	// asserted holds the identity a SAML response established, between
	// the assertion consumer service (a cross-site POST, which carries no
	// SameSite=Lax cookie) and the finish step (a top-level GET, which
	// does): the binding is checked at the finish.
	asserted *Result
	expires  time.Time
	created  time.Time
	// origin is the client network origin that started the sign-in
	// (auth.ClientOrigin), the key of the per-origin bound.
	origin string
}

// pendingTable is the bounded, expiring table of sign-ins in flight. Safe
// for concurrent use.
type pendingTable struct {
	mu sync.Mutex
	m  map[string]*pending
}

// put stores p, started from origin, under a fresh random key and returns
// the key. An origin at its bound loses its own oldest entry; a full
// table loses the oldest entry of the origin that holds the most.
func (t *pendingTable) put(p *pending, origin string, now time.Time) string {
	key := randomString()
	p.created = now
	p.expires = now.Add(pendingTTL)
	p.origin = origin
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = make(map[string]*pending)
	}
	t.sweepLocked(now)
	count := map[string]int{}
	for _, e := range t.m {
		count[e.origin]++
	}
	switch {
	case count[origin] >= pendingPerOrigin:
		t.evictOldestLocked(origin)
	case len(t.m) >= pendingCap:
		heaviest, n := "", 0
		for o, c := range count {
			if c > n || c == n && o < heaviest {
				heaviest, n = o, c
			}
		}
		t.evictOldestLocked(heaviest)
	}
	t.m[key] = p
	return key
}

// evictOldestLocked removes origin's oldest entry. Callers hold t.mu.
func (t *pendingTable) evictOldestLocked(origin string) {
	oldest := ""
	var at time.Time
	for k, e := range t.m {
		if e.origin == origin && (oldest == "" || e.created.Before(at)) {
			oldest, at = k, e.created
		}
	}
	if oldest != "" {
		delete(t.m, oldest)
	}
}

// take removes and returns the live entry under key, if any — a key is
// used once, whatever the outcome: a refused callback must not be
// retryable with the same state.
func (t *pendingTable) take(key string, now time.Time) (*pending, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[key]
	if !ok {
		return nil, false
	}
	delete(t.m, key)
	if now.After(p.expires) {
		return nil, false
	}
	return p, true
}

// peek returns the live entry under key without removing it (the SAML
// ACS looks the request up, then stores the assertion for the finish).
func (t *pendingTable) peek(key string, now time.Time) (*pending, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[key]
	if !ok || now.After(p.expires) {
		return nil, false
	}
	return p, true
}

// assert records the identity a SAML response established, once: an
// entry already asserted is not overwritten (the replay cache refuses a
// second response anyway; this is the belt to its braces).
func (t *pendingTable) assert(key string, r *Result, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[key]
	if !ok || now.After(p.expires) || p.asserted != nil {
		return false
	}
	p.asserted = r
	return true
}

func (t *pendingTable) sweepLocked(now time.Time) {
	for k, e := range t.m {
		if now.After(e.expires) {
			delete(t.m, k)
		}
	}
}

// bindingOf digests a browser binding secret.
func bindingOf(secret string) [sha256.Size]byte { return sha256.Sum256([]byte(secret)) }

// bound reports, in constant time, whether secret is p's binding.
func (p *pending) bound(secret string) bool {
	if secret == "" {
		return false
	}
	sum := bindingOf(secret)
	return subtle.ConstantTimeCompare(sum[:], p.binding[:]) == 1
}

// randomString is 256 bits of randomness, URL-safe.
func randomString() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("federation: reading random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
