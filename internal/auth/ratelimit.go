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
	// burst and refill override failureBurst and failureRefill when set
	// (OriginLimiter); zero keeps the failed-authentication budget.
	burst  float64
	refill time.Duration
}

// budget is the limiter's burst and refill period.
func (l *failureLimiter) budget() (float64, time.Duration) {
	if l.burst > 0 && l.refill > 0 {
		return l.burst, l.refill
	}
	return failureBurst, failureRefill
}

// failureBucket tracks one origin: how much failure budget remains, and
// when it was last updated (refill is computed lazily on access — no
// background goroutine to manage or leak).
type failureBucket struct {
	remaining float64
	updated   time.Time
}

// refreshLocked applies the elapsed refill. Callers hold l.mu.
func (b *failureBucket) refreshLocked(now time.Time, burst float64, refill time.Duration) {
	if elapsed := now.Sub(b.updated); elapsed > 0 {
		b.remaining += float64(elapsed) / float64(refill)
		if b.remaining > burst {
			b.remaining = burst
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
	burst, refill := l.budget()
	b.refreshLocked(now, burst, refill)
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
	burst, refill := l.budget()
	b, ok := l.buckets[origin]
	if !ok {
		l.sweepLocked(now)
		b = &failureBucket{remaining: burst, updated: now}
		l.buckets[origin] = b
	}
	b.refreshLocked(now, burst, refill)
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
	burst, refill := l.budget()
	for origin, b := range l.buckets {
		b.refreshLocked(now, burst, refill)
		if b.remaining >= burst {
			delete(l.buckets, origin)
		}
	}
	if len(l.buckets) >= failureOrigins {
		l.buckets = make(map[string]*failureBucket)
	}
}

// OriginLimiter is the same per-origin token bucket, for another cost an
// unauthenticated request can make the instance pay — starting a
// federated sign-in, which fills server memory and may contact the
// identity provider (federation package). Safe for concurrent use.
type OriginLimiter struct{ l failureLimiter }

// NewOriginLimiter allows burst requests per origin, then one per refill.
func NewOriginLimiter(burst int, refill time.Duration) *OriginLimiter {
	return &OriginLimiter{l: failureLimiter{burst: float64(burst), refill: refill}}
}

// Take spends one unit of origin's budget if any is left, and reports
// whether it did. exhausted is true on the request that spent the last
// unit — log that transition once, not every refused request.
func (o *OriginLimiter) Take(origin string, now time.Time) (ok, exhausted bool) {
	if !o.l.allowed(origin, now) {
		return false, false
	}
	return true, o.l.record(origin, now)
}

// Refill is the refill period: the Retry-After a refused request gets.
func (o *OriginLimiter) Refill() time.Duration {
	_, refill := o.l.budget()
	return refill
}
