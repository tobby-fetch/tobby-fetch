// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

// Redacted is what every serialization of a Secret shows instead of the
// value (NFR-015).
const Redacted = "REDACTED"

// Secret holds a sensitive configuration value (credential, token, key
// passphrase). It redacts itself in every serialization path — String,
// YAML, JSON, fmt verbs — so a secret reaching logs, error messages, or
// configuration dumps is impossible by construction, not by discipline
// (NFR-015). Code that legitimately needs the value calls Reveal, which is
// greppable and reviewable.
type Secret struct {
	value string
}

// NewSecret wraps a sensitive value.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the actual value. Every call site is a deliberate,
// searchable decision to use the secret.
func (s Secret) Reveal() string { return s.value }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s.value == "" }

// String implements fmt.Stringer; it never returns the value.
func (s Secret) String() string {
	if s.value == "" {
		return ""
	}
	return Redacted
}

// GoString keeps %#v from leaking the value.
func (s Secret) GoString() string { return "config.Secret{" + s.String() + "}" }

// MarshalYAML implements yaml.Marshaler; the value never serializes.
func (s Secret) MarshalYAML() (any, error) { return s.String(), nil }

// MarshalJSON implements json.Marshaler; the value never serializes.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s.value == "" {
		return []byte(`""`), nil
	}
	return []byte(`"` + Redacted + `"`), nil
}

// MarshalText implements encoding.TextMarshaler; the value never serializes.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
