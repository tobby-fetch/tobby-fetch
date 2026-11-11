// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// SessionCookie is the UI session cookie name.
const SessionCookie = "tobby_session"

// Session is one signed-in UI session. Sessions live in memory only: an
// instance restart signs everyone out (documented in TBY-AUTH-005).
type Session struct {
	ID      string
	Account string
	Role    Role
	// Method is how the session was opened: basic for the local sign-in
	// form, oidc or saml for a federated one (FR-078 tells them apart).
	Method Method
	// CSRF is the per-session anti-forgery token, embedded in every form
	// (NFR-012).
	CSRF string
	// Created is when the session was opened.
	Created time.Time
	// Expires is the absolute end of the session (auth.sessionTTL): no
	// amount of activity extends it.
	Expires time.Time
	// LastSeen is the last authenticated request the session served; the
	// idle timeout (auth.session.idleTimeout, R-14) runs from it.
	LastSeen time.Time
}

// SessionState is the answer of a session lookup. A presented cookie
// that matches nothing alive is told apart from one that never existed so
// the surfaces can answer TBY-AUTH-005 with the right reason, and so that
// an expiry is audited exactly once — at the moment it is observed.
type SessionState int

const (
	// SessionLive is a session that may serve the request.
	SessionLive SessionState = iota
	// SessionUnknown is an identifier this table does not hold: never
	// issued, closed by logout, or lost with an instance restart.
	SessionUnknown
	// SessionIdle is a session ended by its idle timeout.
	SessionIdle
	// SessionLifetime is a session ended by its absolute lifetime.
	SessionLifetime
)

// ExpiryReason is the audited reason of a session expiry (R-14): the
// target of the session.expire event and the reason label of the
// tobby_auth_sessions_expired_total metric. Closed set.
type ExpiryReason string

// The two expiry reasons.
const (
	ExpiryIdle     ExpiryReason = "idle"
	ExpiryLifetime ExpiryReason = "lifetime"
)

// ExpiryHook observes each session expiry once: the instance wires the
// audit record and the metric on it. origin is the network origin of the
// request that found the session expired, or "local" when the periodic
// sweep did.
type ExpiryHook func(sess *Session, reason ExpiryReason, origin string)

// Sessions is the in-memory session table. Safe for concurrent use.
type Sessions struct {
	ttl time.Duration

	mu sync.Mutex
	m  map[string]Session
	// idle is the idle timeout; zero disables it (the table built by
	// NewSessions alone, as unit tests of other packages use it). The
	// instance always sets it: auth.session.idleTimeout must be positive.
	idle     time.Duration
	onExpire ExpiryHook
}

// NewSessions builds a session table whose sessions live for ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{ttl: ttl, m: make(map[string]Session)}
}

// SetIdleTimeout sets the idle timeout (R-14, FR-072 amendment): a session
// with no authenticated request for d ends, whatever its absolute
// lifetime says. Zero disables the idle bound.
func (s *Sessions) SetIdleTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idle = d
}

// IdleTimeout reports the configured idle timeout.
func (s *Sessions) IdleTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idle
}

// OnExpire registers the hook called once per expired session.
func (s *Sessions) OnExpire(h ExpiryHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onExpire = h
}

// Create opens a local (basic) session for the account and returns it.
func (s *Sessions) Create(account string, role Role, now time.Time) Session {
	return s.CreateWith(account, role, MethodBasic, now)
}

// CreateWith opens a session opened by method for the account.
func (s *Sessions) CreateWith(account string, role Role, method Method, now time.Time) Session {
	sess := Session{
		ID:       randomToken(),
		Account:  account,
		Role:     role,
		Method:   method,
		CSRF:     randomToken(),
		Created:  now,
		Expires:  now.Add(s.ttl),
		LastSeen: now,
	}
	s.mu.Lock()
	expired := s.sweepLocked(now)
	s.m[sess.ID] = sess
	hook := s.onExpire
	s.mu.Unlock()
	notify(hook, expired, OriginLocalSweep)
	return sess
}

// OriginLocalSweep is the origin reported to the expiry hook when the
// expiry was found by a sweep rather than by a request.
const OriginLocalSweep = "local"

// Lookup resolves a presented session identifier. A live session is
// renewed — its idle timer restarts from now — and returned; an expired
// one is removed, reported to the expiry hook with origin, and its state
// returned so the caller can answer TBY-AUTH-005.
func (s *Sessions) Lookup(id, origin string, now time.Time) (Session, SessionState) {
	s.mu.Lock()
	sess, ok := s.m[id]
	if !ok {
		s.mu.Unlock()
		return Session{}, SessionUnknown
	}
	if reason, dead := s.expiredLocked(&sess, now); dead {
		delete(s.m, id)
		hook := s.onExpire
		s.mu.Unlock()
		notify(hook, []expiredSession{{sess, reason}}, origin)
		if reason == ExpiryIdle {
			return Session{}, SessionIdle
		}
		return Session{}, SessionLifetime
	}
	sess.LastSeen = now
	s.m[id] = sess
	s.mu.Unlock()
	return sess, SessionLive
}

// Get returns the live session for id, if any, renewing it like Lookup.
func (s *Sessions) Get(id string, now time.Time) (Session, bool) {
	sess, st := s.Lookup(id, OriginLocalSweep, now)
	return sess, st == SessionLive
}

// Sweep removes every expired session and reports each to the expiry
// hook. The instance runs it periodically, so that a session abandoned
// for good still leaves its session.expire record (R-14) instead of
// vanishing silently at the next sign-in.
func (s *Sessions) Sweep(now time.Time) {
	s.mu.Lock()
	expired := s.sweepLocked(now)
	hook := s.onExpire
	s.mu.Unlock()
	notify(hook, expired, OriginLocalSweep)
}

// Delete closes a session (logout).
func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

// DeleteOthers closes every session of account except keep. After a
// password change (R-34, FR-061), sessions opened with the old credential
// must not survive; the UI passes the session that performed the change as
// keep, the API — whose callers hold no UI session — passes "". Only
// local sessions are concerned: a federated identity carrying the same
// name is another principal, and its sessions were not opened with the
// password that changed.
func (s *Sessions) DeleteOthers(account, keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.m {
		if sess := s.m[id]; sess.Account == account && id != keep && sess.Method.Local() {
			delete(s.m, id)
		}
	}
}

// CheckCSRF verifies a submitted token against the session's, in constant
// time.
func (sess *Session) CheckCSRF(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(sess.CSRF), []byte(token)) == 1
}

// expiredSession is one session found expired, with its reason.
type expiredSession struct {
	sess   Session
	reason ExpiryReason
}

// expiredLocked reports whether sess has expired at now, and why. The
// absolute lifetime wins when both bounds have passed: it is the one no
// activity could have prevented. Callers hold s.mu.
func (s *Sessions) expiredLocked(sess *Session, now time.Time) (ExpiryReason, bool) {
	if now.After(sess.Expires) {
		return ExpiryLifetime, true
	}
	if s.idle > 0 && now.Sub(sess.LastSeen) > s.idle {
		return ExpiryIdle, true
	}
	return "", false
}

// sweepLocked drops expired sessions and returns them. Callers hold s.mu.
func (s *Sessions) sweepLocked(now time.Time) []expiredSession {
	var out []expiredSession
	for id := range s.m {
		sess := s.m[id]
		if reason, dead := s.expiredLocked(&sess, now); dead {
			delete(s.m, id)
			out = append(out, expiredSession{sess, reason})
		}
	}
	return out
}

// notify calls hook for each expiry, outside the table lock: the hook
// writes an audit record and must never be able to deadlock the table.
func notify(hook ExpiryHook, expired []expiredSession, origin string) {
	if hook == nil {
		return
	}
	for i := range expired {
		hook(&expired[i].sess, expired[i].reason, origin)
	}
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: reading random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
