// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// accountsFile is the file holding accounts and tokens under the state
// directory. Mode 0600: it contains password hashes and token digests
// (NFR-018).
const accountsFile = "accounts.yaml"

// fileSchema is the on-disk format. Versioned like every persisted schema.
type fileSchema struct {
	Version  int       `yaml:"version"`
	Accounts []Account `yaml:"accounts"`
	Tokens   []Token   `yaml:"tokens,omitempty"`
}

// Store is the file-backed account and token store. Safe for concurrent
// use; every mutation is written atomically (temp file + rename).
type Store struct {
	path string

	mu   sync.Mutex
	data fileSchema
	// verified is the short-lived cache of successful password checks
	// (v0.4.2 hardening; see VerifyPassword). Guarded by mu.
	verified []verifiedCred
}

// Success caching for password verification.
//
// WHY: the machine surfaces are stateless — docker, helm, oras and any
// API client re-present the same Basic credential on EVERY request, and
// a single `docker pull` of a multi-layer image is dozens of them. Each
// used to cost a full argon2id computation (64 MiB, tens of ms): the
// legitimate client was paying the price designed for the attacker. The
// cache remembers a successful verification for a short TTL, keyed by
// SHA-256(account NUL password) — the password itself is never stored
// (NFR-015), and a preimage of the digest is exactly the credential, so
// the cache leaks nothing the accounts file does not.
//
// What a hit returns is looked up FRESH from the account table: the
// cache answers only "this exact pair verified moments ago", never what
// the account's role is — so a role change applies immediately, without
// invalidation. Password changes and account removals DO invalidate
// (SetPassword, DeleteAccount): a replaced password must stop working
// the moment the change lands, TTL or not.
const (
	// verifyCacheTTL is deliberately short: one minute converts "one
	// argon2id per request" into "one per client per minute" — the whole
	// win — while keeping the window in which a revoked password still
	// answers (already closed by explicit invalidation) as the only
	// residual exposure for out-of-band edits of the accounts file.
	verifyCacheTTL = time.Minute
	// verifyCacheCap bounds the cache. It only fills through SUCCESSFUL
	// verifications, so unlike the failure tables it is not
	// attacker-growable, but a bound costs nothing and removes the
	// reasoning burden.
	verifyCacheCap = 1024
)

// verifiedCred is one cached success: the credential digest, the account
// it verified for, and when the entry lapses.
type verifiedCred struct {
	sum     [sha256.Size]byte
	name    string
	expires time.Time
}

// credSum fingerprints an account/password pair. The NUL separator makes
// the encoding injective ("ab"+"c" and "a"+"bc" must not collide);
// account names cannot contain NUL.
func credSum(name, password string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(password))
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// ErrNotFound reports an unknown account or token name.
var ErrNotFound = errors.New("not found")

// ErrExists reports a duplicate account or token name.
var ErrExists = errors.New("already exists")

// ErrLastAdmin refuses a removal or a demotion that would leave the
// instance with no administrator. The invariant is enforced here, under
// the store lock, rather than at the call sites: it is the only place
// where the check and the write are atomic, and it is what makes the rule
// impossible to forget on a surface added later (FR-073, FR-074). An
// instance without an admin is unmanageable, and FR-005 makes it refuse to
// start with no account at all — the file must never reach that state.
var ErrLastAdmin = errors.New("last administrator account")

// Open loads (or initializes empty) the account store under stateRoot. The
// directory is created 0700 if missing.
func Open(stateRoot string) (*Store, error) {
	if stateRoot == "" {
		return nil, errors.New("auth: state root is required")
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("auth: creating state directory: %w", err)
	}
	s := &Store{path: filepath.Join(stateRoot, accountsFile), data: fileSchema{Version: 1}}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("auth: reading %s: %w", s.path, err)
	}
	if err := yaml.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("auth: parsing %s: %w", s.path, err)
	}
	if s.data.Version != 1 {
		return nil, fmt.Errorf("auth: %s has unsupported version %d (this build supports 1)", s.path, s.data.Version)
	}
	return s, nil
}

// save writes the store atomically with restrictive permissions (NFR-018).
// Callers hold s.mu.
func (s *Store) save() error {
	out, err := yaml.Marshal(s.data)
	if err != nil {
		return fmt.Errorf("auth: encoding accounts: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".accounts-*")
	if err != nil {
		return fmt.Errorf("auth: writing accounts: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best-effort cleanup on the error paths
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the chmod error is the one reported
		return fmt.Errorf("auth: securing accounts file: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error is the one reported
		return fmt.Errorf("auth: writing accounts: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: writing accounts: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("auth: replacing accounts file: %w", err)
	}
	return nil
}

// HasAccounts reports whether at least one account exists — the R-01
// startup gate.
func (s *Store) HasAccounts() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Accounts) > 0
}

// Accounts returns the accounts sorted by name.
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, len(s.data.Accounts))
	copy(out, s.data.Accounts)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Account returns the account named name. The password hash travels with
// it; callers outside this package only ever read the name, role, and
// timestamps (NFR-015).
func (s *Store) Account(name string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Name == name {
			return s.data.Accounts[i], true
		}
	}
	return Account{}, false
}

// adminsLocked counts the accounts holding the admin role, excluding
// except when it is non-empty. Callers hold s.mu.
func (s *Store) adminsLocked(except string) int {
	n := 0
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Role == RoleAdmin && s.data.Accounts[i].Name != except {
			n++
		}
	}
	return n
}

// AddAccount creates an account with the given role, hashing password with
// argon2id (R-01: the tool computes the hash).
func (s *Store) AddAccount(name string, role Role, password string, now time.Time) error {
	if name == "" {
		return errors.New("auth: account name is required")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.data.Accounts {
		if a.Name == name {
			return fmt.Errorf("auth: account %q: %w", name, ErrExists)
		}
	}
	s.data.Accounts = append(s.data.Accounts, Account{
		Name: name, Role: role, Hash: hash, Created: now.UTC(),
	})
	return s.save()
}

// DeleteAccount removes name (FR-073). Removing the last admin is
// refused with ErrLastAdmin — including the self-removal an administrator
// might attempt on itself, which is the most likely way to lock an
// instance out. Deleting an account does not touch its live UI sessions:
// closing them is the caller's job, on the surface that owns the session
// table.
func (s *Store) DeleteAccount(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Name != name {
			continue
		}
		if s.data.Accounts[i].Role == RoleAdmin && s.adminsLocked(name) == 0 {
			return fmt.Errorf("auth: account %q: %w", name, ErrLastAdmin)
		}
		s.data.Accounts = append(s.data.Accounts[:i], s.data.Accounts[i+1:]...)
		// A removed account's cached verifications must die with it
		// (v0.4.2: the success cache above must not outlive the account).
		s.forgetVerifiedLocked(name)
		return s.save()
	}
	return fmt.Errorf("auth: account %q: %w", name, ErrNotFound)
}

// SetRole changes name's role (FR-074). Demoting the last admin is
// refused with ErrLastAdmin, for the same reason as DeleteAccount: an
// administrator taking its own admin role away while it is the only one
// leaves nobody able to grant it back. Setting the role an account already
// holds is a no-op that still succeeds — idempotence keeps the API mirror
// honest.
func (s *Store) SetRole(name string, role Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Name != name {
			continue
		}
		if s.data.Accounts[i].Role == role {
			return nil
		}
		if s.data.Accounts[i].Role == RoleAdmin && s.adminsLocked(name) == 0 {
			return fmt.Errorf("auth: account %q: %w", name, ErrLastAdmin)
		}
		s.data.Accounts[i].Role = role
		return s.save()
	}
	return fmt.Errorf("auth: account %q: %w", name, ErrNotFound)
}

// SetPassword replaces name's password hash.
func (s *Store) SetPassword(name, password string, _ time.Time) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Name == name {
			s.data.Accounts[i].Hash = hash
			// The replaced password must stop verifying NOW, not at the
			// cache TTL (v0.4.2): sessions opened with it are already
			// closed by the caller (Sessions.DeleteOthers), and a cached
			// machine-surface success must not outlive them.
			s.forgetVerifiedLocked(name)
			return s.save()
		}
	}
	return fmt.Errorf("auth: account %q: %w", name, ErrNotFound)
}

// VerifyPassword checks name/password. On success it records the login
// time (best effort) and returns the account.
//
// A pair that verified within verifyCacheTTL answers from the cache
// without recomputing argon2id (see the cache commentary above): the
// stateless machine surfaces re-present the credential per request, and
// the hash cost belongs to attackers, not to a `docker pull`. On a
// cache hit the LastLogin timestamp is not rewritten — within the TTL
// it is at most a minute stale, and skipping the disk write per request
// is part of the point.
func (s *Store) VerifyPassword(name, password string, now time.Time) (Account, bool) {
	sum := credSum(name, password)
	s.mu.Lock()
	defer s.mu.Unlock()
	if acct, ok := s.cachedLocked(sum, name, now); ok {
		return acct, true
	}
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Name != name {
			continue
		}
		if !verifyPassword(password, s.data.Accounts[i].Hash) {
			return Account{}, false
		}
		s.data.Accounts[i].LastLogin = now.UTC()
		_ = s.save() // best effort: a failed timestamp write must not fail the login
		s.rememberLocked(sum, name, now)
		return s.data.Accounts[i], true
	}
	// Unknown account: burn comparable time so name probing reads the same
	// as a wrong password (NFR-015).
	verifyPassword(password, burnHash)
	return Account{}, false
}

// cachedLocked answers a verification from the cache. The scan compares
// digests with subtle.ConstantTimeCompare — same discipline as
// VerifyToken: nothing on this path may leak through timing how close a
// guess came (NFR-015). The account is re-read from the live table so a
// hit always carries the current role. Callers hold s.mu.
func (s *Store) cachedLocked(sum [sha256.Size]byte, name string, now time.Time) (Account, bool) {
	for i := range s.verified {
		e := &s.verified[i]
		if now.After(e.expires) {
			continue
		}
		if subtle.ConstantTimeCompare(e.sum[:], sum[:]) != 1 {
			continue
		}
		if e.name != name {
			continue // digest-collision paranoia: the digest already binds the name
		}
		for j := range s.data.Accounts {
			if s.data.Accounts[j].Name == e.name {
				return s.data.Accounts[j], true
			}
		}
		return Account{}, false // account vanished; the entry is inert and will expire
	}
	return Account{}, false
}

// rememberLocked records a successful verification, sweeping expired
// entries and enforcing the cap. Callers hold s.mu.
func (s *Store) rememberLocked(sum [sha256.Size]byte, name string, now time.Time) {
	live := s.verified[:0]
	for _, e := range s.verified {
		if !now.After(e.expires) {
			live = append(live, e)
		}
	}
	s.verified = live
	if len(s.verified) >= verifyCacheCap {
		// Still full after the sweep: drop everything rather than pick
		// victims — the cache is a cost optimization, not state, and the
		// worst case of a reset is one argon2id per live client.
		s.verified = s.verified[:0]
	}
	s.verified = append(s.verified, verifiedCred{sum: sum, name: name, expires: now.Add(verifyCacheTTL)})
}

// forgetVerifiedLocked drops every cached verification of name — called
// on password change and account removal, where waiting out the TTL
// would let a replaced credential keep answering. Callers hold s.mu.
func (s *Store) forgetVerifiedLocked(name string) {
	live := s.verified[:0]
	for _, e := range s.verified {
		if e.name != name {
			live = append(live, e)
		}
	}
	s.verified = live
}

// burnHash is a throwaway argon2id hash used to equalize timing for
// unknown accounts.
var burnHash = func() string {
	h, err := hashPassword("timing-equalizer")
	if err != nil {
		panic(err)
	}
	return h
}()

// tokenPrefix marks Tobby API tokens; the part after it is the secret.
const tokenPrefix = "tby_"

// CreateToken mints a static token (FR-072). The clear secret is returned
// exactly once; only its SHA-256 is stored.
func (s *Store) CreateToken(name string, role Role, now time.Time) (string, Token, error) {
	if name == "" {
		return "", Token{}, errors.New("auth: token name is required")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", Token{}, fmt.Errorf("auth: generating token: %w", err)
	}
	secret := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(secret))

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.data.Tokens {
		if t.Name == name && !t.Revoked {
			return "", Token{}, fmt.Errorf("auth: token %q: %w", name, ErrExists)
		}
	}
	tok := Token{Name: name, Role: role, Hash: hex.EncodeToString(sum[:]), Created: now.UTC()}
	s.data.Tokens = append(s.data.Tokens, tok)
	if err := s.save(); err != nil {
		return "", Token{}, err
	}
	return secret, tok, nil
}

// RevokeToken revokes name immediately and permanently (FR-072).
func (s *Store) RevokeToken(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tokens {
		if s.data.Tokens[i].Name == name && !s.data.Tokens[i].Revoked {
			s.data.Tokens[i].Revoked = true
			return s.save()
		}
	}
	return fmt.Errorf("auth: token %q: %w", name, ErrNotFound)
}

// Tokens returns the tokens sorted by name, revoked included (the screen
// shows the full lifecycle).
func (s *Store) Tokens() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, len(s.data.Tokens))
	copy(out, s.data.Tokens)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// VerifyToken checks a presented secret against the non-revoked tokens.
func (s *Store) VerifyToken(secret string) (Token, bool) {
	sum := sha256.Sum256([]byte(secret))
	want := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.data.Tokens {
		if t.Revoked {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) == 1 {
			return t, true
		}
	}
	return Token{}, false
}
