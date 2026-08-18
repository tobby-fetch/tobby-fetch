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
			return s.save()
		}
	}
	return fmt.Errorf("auth: account %q: %w", name, ErrNotFound)
}

// VerifyPassword checks name/password. On success it records the login
// time (best effort) and returns the account.
func (s *Store) VerifyPassword(name, password string, now time.Time) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].Name != name {
			continue
		}
		if !verifyPassword(password, s.data.Accounts[i].Hash) {
			return Account{}, false
		}
		s.data.Accounts[i].LastLogin = now.UTC()
		_ = s.save() // best effort: a failed timestamp write must not fail the login
		return s.data.Accounts[i], true
	}
	// Unknown account: burn comparable time so name probing reads the same
	// as a wrong password (NFR-015).
	verifyPassword(password, burnHash)
	return Account{}, false
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
