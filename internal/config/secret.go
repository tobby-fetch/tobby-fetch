// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"errors"

	"gopkg.in/yaml.v3"
)

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

// UnmarshalYAML implements yaml.Unmarshaler: a secret is written as an
// ordinary scalar in the configuration file and becomes unprintable the
// moment it is read.
//
// The error deliberately does not quote the offending node. Every other
// configuration error names the value it rejected, which is how an
// operator finds the typo; here the value is the secret, and a type
// mismatch is not worth leaking it into a startup log (NFR-015).
func (s *Secret) UnmarshalYAML(value *yaml.Node) error {
	var v string
	if err := value.Decode(&v); err != nil {
		return errors.New("expected a string value (the value is not quoted back: it is a secret)")
	}
	s.value = v
	return nil
}

// Redact reports the value's presence without revealing it, for the log
// lines and reporting surfaces that must state whether a credential is
// configured at all.
func (s Secret) Redact() string {
	if s.value == "" {
		return "(unset)"
	}
	return Redacted
}
