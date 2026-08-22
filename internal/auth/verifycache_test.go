// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"strconv"
	"testing"
	"time"
)

// TestVerifyPasswordCachesSuccesses proves a cache HIT skips the argon2id
// recomputation, directly: after one successful verification the stored
// hash is corrupted under the cache's feet, and the same pair still
// verifies within the TTL — only the cache can be answering. A different
// password (different digest) must miss and fail against the corrupt
// hash, and expiry must force the recomputation again.
func TestVerifyPasswordCachesSuccesses(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("ci", RoleOperator, "pw-ci", t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyPassword("ci", "pw-ci", t0); !ok {
		t.Fatal("first verification failed")
	}

	// Sabotage the stored hash: any path that recomputes argon2id now
	// fails, so a success below can only come from the cache.
	s.mu.Lock()
	s.data.Accounts[0].Hash = "not-a-hash"
	s.mu.Unlock()

	if _, ok := s.VerifyPassword("ci", "pw-ci", t0.Add(30*time.Second)); !ok {
		t.Error("verification within the TTL recomputed the hash instead of hitting the cache")
	}
	if _, ok := s.VerifyPassword("ci", "other", t0.Add(30*time.Second)); ok {
		t.Error("a different password answered from the cache: the digest must cover the password")
	}
	if _, ok := s.VerifyPassword("intruder", "pw-ci", t0.Add(30*time.Second)); ok {
		t.Error("a different account answered from the cache: the digest must cover the account")
	}
	if _, ok := s.VerifyPassword("ci", "pw-ci", t0.Add(verifyCacheTTL+time.Second)); ok {
		t.Error("an expired entry still answered: the TTL is the bound on out-of-band edits")
	}
}

// TestVerifyCacheRoleIsFresh: a hit re-reads the account, so a role
// change applies immediately with no invalidation hook — the cache
// answers "this pair verified", never "this is the role".
func TestVerifyCacheRoleIsFresh(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("admin", RoleAdmin, "pw-a", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAccount("ci", RoleViewer, "pw-ci", t0); err != nil {
		t.Fatal(err)
	}
	if acct, ok := s.VerifyPassword("ci", "pw-ci", t0); !ok || acct.Role != RoleViewer {
		t.Fatalf("first verification: ok=%v role=%v", ok, acct.Role)
	}
	if err := s.SetRole("ci", RoleOperator); err != nil {
		t.Fatal(err)
	}
	acct, ok := s.VerifyPassword("ci", "pw-ci", t0.Add(time.Second))
	if !ok || acct.Role != RoleOperator {
		t.Errorf("cached verification: ok=%v role=%v, want operator immediately", ok, acct.Role)
	}
}

// TestVerifyCacheInvalidation is the security half of the cache contract:
// a changed password and a removed account stop verifying the moment the
// mutation lands — the TTL never extends a revoked credential.
func TestVerifyCacheInvalidation(t *testing.T) {
	t.Run("password change", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddAccount("ci", RoleOperator, "old-pw", t0); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.VerifyPassword("ci", "old-pw", t0); !ok {
			t.Fatal("seed verification failed")
		}
		if err := s.SetPassword("ci", "new-pw", t0); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.VerifyPassword("ci", "old-pw", t0.Add(time.Second)); ok {
			t.Error("the old password still verifies from the cache after SetPassword")
		}
		if _, ok := s.VerifyPassword("ci", "new-pw", t0.Add(time.Second)); !ok {
			t.Error("the new password does not verify")
		}
	})
	t.Run("account removal", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddAccount("admin", RoleAdmin, "pw-a", t0); err != nil {
			t.Fatal(err)
		}
		if err := s.AddAccount("ci", RoleOperator, "pw-ci", t0); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.VerifyPassword("ci", "pw-ci", t0); !ok {
			t.Fatal("seed verification failed")
		}
		if err := s.DeleteAccount("ci"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.VerifyPassword("ci", "pw-ci", t0.Add(time.Second)); ok {
			t.Error("a deleted account still verifies from the cache")
		}
		if n := len(s.verified); n != 0 {
			t.Errorf("cache still holds %d entries for the deleted account", n)
		}
	})
}

// TestVerifyCacheEviction pins the size bound: expired entries are swept
// on insert, and a still-full cache resets rather than grow — it is a
// cost optimization, never state.
func TestVerifyCacheEviction(t *testing.T) {
	s := newStore(t)
	// Fill to the cap with live synthetic entries (rememberLocked is the
	// insert path under test; no need to pay argon2id a thousand times).
	s.mu.Lock()
	for i := range verifyCacheCap {
		s.rememberLocked(credSum("acct-"+strconv.Itoa(i), "pw"), "acct-"+strconv.Itoa(i), t0)
	}
	if n := len(s.verified); n != verifyCacheCap {
		s.mu.Unlock()
		t.Fatalf("cache holds %d entries after filling, want %d", n, verifyCacheCap)
	}
	// One more while everything is live: the cache resets, never grows.
	s.rememberLocked(credSum("overflow", "pw"), "overflow", t0)
	if n := len(s.verified); n != 1 {
		s.mu.Unlock()
		t.Fatalf("cache holds %d entries after overflow, want 1 (reset)", n)
	}
	// Expired entries are swept before the cap forces anything.
	s.rememberLocked(credSum("late", "pw"), "late", t0.Add(verifyCacheTTL+time.Second))
	n := len(s.verified)
	s.mu.Unlock()
	if n != 1 {
		t.Errorf("cache holds %d entries after expiry sweep, want 1", n)
	}
}
