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
	// CSRF is the per-session anti-forgery token, embedded in every form
	// (NFR-012).
	CSRF    string
	Expires time.Time
}

// Sessions is the in-memory session table. Safe for concurrent use.
type Sessions struct {
	ttl time.Duration

	mu sync.Mutex
	m  map[string]Session
}

// NewSessions builds a session table whose sessions live for ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{ttl: ttl, m: make(map[string]Session)}
}

// Create opens a session for the account and returns it.
func (s *Sessions) Create(account string, role Role, now time.Time) Session {
	sess := Session{
		ID:      randomToken(),
		Account: account,
		Role:    role,
		CSRF:    randomToken(),
		Expires: now.Add(s.ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	s.m[sess.ID] = sess
	return sess
}

// Get returns the live session for id, if any.
func (s *Sessions) Get(id string, now time.Time) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	if !ok || now.After(sess.Expires) {
		delete(s.m, id)
		return Session{}, false
	}
	return sess, true
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
// keep, the API — whose callers hold no UI session — passes "".
func (s *Sessions) DeleteOthers(account, keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.m {
		if sess.Account == account && id != keep {
			delete(s.m, id)
		}
	}
}

// CheckCSRF verifies a submitted token against the session's, in constant
// time.
func (sess *Session) CheckCSRF(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(sess.CSRF), []byte(token)) == 1
}

// sweep drops expired sessions. Callers hold s.mu.
func (s *Sessions) sweep(now time.Time) {
	for id, sess := range s.m {
		if now.After(sess.Expires) {
			delete(s.m, id)
		}
	}
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: reading random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
