// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// throttledAuthenticator builds an Authenticator over one account with an
// adjustable clock, so refill can be tested without sleeping.
func throttledAuthenticator(t *testing.T) (a *Authenticator, advance func(d time.Duration)) {
	t.Helper()
	s := newStore(t)
	if err := s.AddAccount("viewer", RoleViewer, "pw-v", t0); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now := t0
	a = &Authenticator{
		Store:  s,
		Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
	}
	advance = func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
	return a, advance
}

// TestFailedAuthenticationIsThrottledPerOrigin is the v0.4.2 DoS fix on
// the registry surface: an origin gets failureBurst failed verifications,
// then 429 — before any argon2id runs — and the budget refills with time.
// Successes never spend budget, and a different origin keeps its own.
func TestFailedAuthenticationIsThrottledPerOrigin(t *testing.T) {
	a, advance := throttledAuthenticator(t)
	h := a.Registry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := func(origin, user, pass string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v2/", http.NoBody)
		r.RemoteAddr = origin + ":41234"
		r.SetBasicAuth(user, pass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// A success on a fresh origin spends nothing.
	if w := req("198.51.100.7", "viewer", "pw-v"); w.Code != http.StatusOK {
		t.Fatalf("valid credential = %d, want 200", w.Code)
	}
	// The full failure budget answers 401 — honest mistakes never see 429.
	for i := range failureBurst {
		if w := req("198.51.100.7", "viewer", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i+1, w.Code)
		}
	}
	// The budget is spent: everything from this origin is refused with
	// 429 and a Retry-After — even a now-correct credential, because the
	// whole point is that nothing is verified for a throttled origin.
	w := req("198.51.100.7", "viewer", "wrong")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over budget = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != RetryAfter {
		t.Errorf("Retry-After = %q, want %q", got, RetryAfter)
	}
	if w := req("198.51.100.7", "viewer", "pw-v"); w.Code != http.StatusTooManyRequests {
		t.Errorf("valid credential from a throttled origin = %d, want 429 (nothing is verified)", w.Code)
	}

	// Another origin is untouched: budgets are per origin, like FR-094
	// audit origins.
	if w := req("203.0.113.9", "viewer", "pw-v"); w.Code != http.StatusOK {
		t.Errorf("another origin = %d, want 200", w.Code)
	}

	// The budget refills with time: one attempt per failureRefill.
	advance(failureRefill)
	if w := req("198.51.100.7", "viewer", "pw-v"); w.Code != http.StatusOK {
		t.Errorf("after one refill period = %d, want 200", w.Code)
	}
}

// TestAPIThrottleAnswersProblemDocument pins the API shape of the same
// refusal: RFC 9457 with the TBY-AUTH-012 entry the UI renders too
// (FR-061 parity), plus the Retry-After the plain surfaces send.
func TestAPIThrottleAnswersProblemDocument(t *testing.T) {
	a, _ := throttledAuthenticator(t)
	h := a.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := func(pass string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/content", http.NoBody)
		r.SetBasicAuth("viewer", pass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for range failureBurst {
		if w := req("wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure = %d, want 401", w.Code)
		}
	}
	w := req("wrong")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over budget = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != RetryAfter {
		t.Errorf("Retry-After = %q, want %q", got, RetryAfter)
	}
	if !strings.Contains(w.Body.String(), "TBY-AUTH-012") {
		t.Errorf("problem body misses TBY-AUTH-012: %s", w.Body.String())
	}
}

// TestFailureLimiterTableIsBounded pins the memory bound: the tracked
// origins are attacker-influencible (spoofed sources, wide botnets), so
// the table sweeps refilled entries and, as a last resort, resets — it
// must never grow past its cap.
func TestFailureLimiterTableIsBounded(t *testing.T) {
	l := &failureLimiter{}
	now := t0
	for i := range failureOrigins + 100 {
		l.record("origin-"+strconv.Itoa(i), now)
	}
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > failureOrigins {
		t.Errorf("limiter tracks %d origins, cap is %d", n, failureOrigins)
	}
}

// TestThrottleTransitionLoggedOnce: the transition into throttling is
// logged exactly once per exhaustion — per-request records would hand a
// flooding lever to the very traffic being contained.
func TestThrottleTransitionLoggedOnce(t *testing.T) {
	l := &failureLimiter{}
	transitions := 0
	for range failureBurst * 2 {
		if l.record("o", t0) {
			transitions++
		}
	}
	if transitions != 1 {
		t.Errorf("exhaustion reported %d times, want once", transitions)
	}
}
