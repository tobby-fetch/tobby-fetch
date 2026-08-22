// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"strconv"
	"sync"
	"time"
)

// Failed-authentication rate limiting (v0.4.2 hardening).
//
// WHY: every failed password verification costs one argon2id computation
// at 64 MiB (password.go) — including for accounts that do not exist
// (the burnHash timing equalizer, NFR-015). That cost is the point
// against an offline cracker, but online it is an amplification lever:
// one cheap request with a garbage credential makes THIS process spend
// 64 MiB and tens of milliseconds, and the machine surfaces (/v2/,
// /api/v1, /files/) re-present credentials on every request. The
// limiter bounds that spend per client network origin, and it is
// checked BEFORE any hash runs — a throttled origin costs a map lookup.
//
// The budget covers FAILURES only: a client presenting a valid
// credential is never slowed down by its own traffic, only by sharing
// an exhausted origin with a failing one — the inherent limit of
// keying on the network origin, accepted because the origin is the
// only pre-authentication identity that is not attacker-chosen
// (FR-094 makes the same call for audit records).
const (
	// failureBurst is how many failures one origin may accumulate before
	// it is throttled. Ten absorbs every honest mistake — a mistyped
	// `docker login` retried a few times, a CI job with one stale token —
	// while capping a naive brute force at ten hashes per window.
	failureBurst = 10
	// failureRefill is how fast the budget recovers: one attempt per
	// 30 s, ≈ 120 guesses per hour per origin. Useless against argon2id
	// as a cracking rate, negligible as a CPU cost.
	failureRefill = 30 * time.Second
	// failureOrigins bounds the tracked-origin table. Same shape and same
	// argument as the seenCap sweep in middleware.go: the table is fed by
	// attacker-influencible keys and must not become a memory sink.
	failureOrigins = 4096
)

// RetryAfter is the Retry-After value (in seconds) the 429 responses
// carry — the refill period, which is when one attempt is worth making
// again.
var RetryAfter = strconv.Itoa(int(failureRefill / time.Second))

// failureLimiter is a token-bucket limiter over failure budgets, keyed by
// client origin. The zero value is ready to use; safe for concurrent use.
type failureLimiter struct {
	mu      sync.Mutex
	buckets map[string]*failureBucket
}

// failureBucket tracks one origin: how much failure budget remains, and
// when it was last updated (refill is computed lazily on access — no
// background goroutine to manage or leak).
type failureBucket struct {
	remaining float64
	updated   time.Time
}

// refreshLocked applies the elapsed refill. Callers hold l.mu.
func (b *failureBucket) refreshLocked(now time.Time) {
	if elapsed := now.Sub(b.updated); elapsed > 0 {
		b.remaining += float64(elapsed) / float64(failureRefill)
		if b.remaining > failureBurst {
			b.remaining = failureBurst
		}
	}
	b.updated = now
}

// allowed reports whether origin still has failure budget — whether an
// authentication attempt may proceed to the expensive verification.
// It consumes nothing: only actual failures spend budget, so a stream of
// successful requests is never charged.
func (l *failureLimiter) allowed(origin string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[origin]
	if !ok {
		return true
	}
	b.refreshLocked(now)
	return b.remaining >= 1
}

// record spends one unit of origin's budget after a failed verification.
// It returns true when this failure is the one that exhausted the budget
// — the caller logs that transition once, instead of one line per
// throttled request (which would hand the attacker a log-flooding lever).
func (l *failureLimiter) record(origin string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[string]*failureBucket)
	}
	b, ok := l.buckets[origin]
	if !ok {
		l.sweepLocked(now)
		b = &failureBucket{remaining: failureBurst, updated: now}
		l.buckets[origin] = b
	}
	b.refreshLocked(now)
	before := b.remaining
	b.remaining--
	if b.remaining < 0 {
		b.remaining = 0
	}
	return before >= 1 && b.remaining < 1
}

// sweepLocked bounds the table before inserting a new origin: drop the
// entries that have fully refilled (they carry no information), and when
// every tracked origin is still hot, start over rather than grow without
// bound — the same last-resort reset as the seen table, with the same
// justification: the limiter is a cost bound, not state anyone owns.
// Callers hold l.mu.
func (l *failureLimiter) sweepLocked(now time.Time) {
	if len(l.buckets) < failureOrigins {
		return
	}
	for origin, b := range l.buckets {
		b.refreshLocked(now)
		if b.remaining >= failureBurst {
			delete(l.buckets, origin)
		}
	}
	if len(l.buckets) >= failureOrigins {
		l.buckets = make(map[string]*failureBucket)
	}
}
