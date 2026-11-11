// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package auth implements Tobby's local authentication (ADR-0009, R-01):
// file-backed accounts with argon2id password hashes computed by the tool —
// never written by hand —, static API tokens (FR-072) hashed at rest,
// in-memory UI sessions with CSRF tokens, and the role ladder
// viewer < operator < admin (FR-074, minimal slice at milestone 2).
//
// The accounts file lives in the instance state directory, strictly outside
// the transportable store (R-16): credentials never travel on a media.
// Secrets are never logged and never re-displayable (NFR-015): a token's
// clear form exists only in the HTTP response that created it.
package auth

import (
	"fmt"
	"time"
)

// Role is an account's authorization level. The set is closed at three
// (decision ADR-0009): a viewer reads, an operator imports and triggers,
// an admin administers accounts, tokens, and the instance.
type Role string

// The three roles of the closed set (ADR-0009).
const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// rank orders roles for AtLeast. Unknown roles rank below viewer.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// AtLeast reports whether r grants at least floor.
func (r Role) AtLeast(floor Role) bool { return r.rank() >= floor.rank() }

// ParseRole validates a role name from configuration or CLI input.
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleViewer, RoleOperator, RoleAdmin:
		return Role(s), nil
	}
	return "", fmt.Errorf("unknown role %q: valid roles are viewer, operator, and admin", s)
}

// Account is one local account (FR-073). The password exists only as an
// argon2id hash in PHC string form.
type Account struct {
	Name      string    `yaml:"name"`
	Role      Role      `yaml:"role"`
	Hash      string    `yaml:"hash"`
	Created   time.Time `yaml:"created"`
	LastLogin time.Time `yaml:"lastLogin,omitempty"`
}

// Token is one static API token (FR-072). Only the SHA-256 of the secret is
// stored; revocation is immediate and permanent.
type Token struct {
	Name    string    `yaml:"name"`
	Role    Role      `yaml:"role"`
	Hash    string    `yaml:"hash"`
	Created time.Time `yaml:"created"`
	Revoked bool      `yaml:"revoked,omitempty"`
	// Expires is the token's optional expiry (FR-072 amendment, R-14);
	// zero is the long-lived FR-072 token. Additive in the accounts
	// file: a store written before the field reads as "no expiry". Set at
	// creation (explicitly, or by auth.tokens.defaultLifetime) and
	// enforced by VerifyToken; a refusal of an expired token is audited
	// as token.expire (FR-094).
	Expires time.Time `yaml:"expires,omitempty"`
}

// Identity is the authenticated caller attached to a request context.
type Identity struct {
	// Name is the account or token name, or "anonymous" when
	// authentication is disabled by the FR-075 override.
	Name string
	// Role is the effective role. With authentication disabled, admin:
	// the override removes the barrier entirely and the permanent banner
	// plus the audit trail carry the visibility (FR-075).
	Role Role
	// Token is true when the caller authenticated with a static token.
	Token bool
	// Anonymous is true under the FR-075 authentication override.
	Anonymous bool
	// DisplayName is the readable name of a federated identity whose Name
	// is an opaque key (OIDC sub, SAML NameID). Display only: every
	// decision is made on Name and Role.
	DisplayName string
	// Method is how the caller authenticated: basic or token on the
	// machine surfaces, the session's method in the UI. FR-078 needs it to
	// tell a local sign-in under federation — a break-glass use, audited
	// on its own — from an ordinary one, and the account screen to know
	// that a federated identity has no local password to change. Empty
	// only under the FR-075 override.
	Method Method
}

// Label is the name to show: DisplayName when there is one, Name
// otherwise.
func (id Identity) Label() string {
	if id.DisplayName != "" {
		return id.DisplayName
	}
	return id.Name
}

// Method is an authentication method (ADR-0009: four methods side by
// side). The set is closed.
type Method string

// The four methods.
const (
	MethodBasic Method = "basic"
	MethodToken Method = "token"
	MethodOIDC  Method = "oidc"
	MethodSAML  Method = "saml"
)

// Local reports whether m is one of the two methods that work without an
// identity provider (FR-078: they stay operational when it is down).
func (m Method) Local() bool { return m == MethodBasic || m == MethodToken }
