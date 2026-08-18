// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Size is a byte quantity that (un)marshals as a human unit string
// ("64MiB", "512kB", "2GiB") in YAML, the way Duration does for durations.
//
// It exists because the one setting that needed it — the FR-029 resume
// threshold — is a number an operator reasons about in mebibytes and
// would otherwise have to write as 67108864. A configuration value nobody
// can read is a configuration value nobody audits.
type Size int64

// Binary and decimal multipliers. Both spellings are accepted because both
// appear in the wild; the marshaled form is always binary, which is what
// storage and transfer sizes actually mean.
const (
	KiB = 1 << 10
	MiB = 1 << 20
	GiB = 1 << 30
	TiB = 1 << 40
)

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"TiB", TiB}, {"GiB", GiB}, {"MiB", MiB}, {"KiB", KiB},
	{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3},
	{"B", 1},
}

// ParseSize parses a byte quantity: a bare integer means bytes, a suffixed
// one means what the suffix says. Negative values are refused — a
// threshold below zero has no meaning and would silently behave like a
// different setting.
func ParseSize(s string) (Size, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("empty size (expected e.g. \"64MiB\")")
	}
	for _, u := range sizeUnits {
		digits, ok := cutSuffixFold(raw, u.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(digits), 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid size %q (expected e.g. \"64MiB\", \"512kB\", or a byte count)", s)
		}
		if n > (1<<62)/u.mult {
			return 0, fmt.Errorf("size %q overflows", s)
		}
		return Size(n * u.mult), nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q (expected e.g. \"64MiB\", \"512kB\", or a byte count)", s)
	}
	return Size(n), nil
}

// cutSuffixFold trims a unit suffix case-insensitively ("64mib" is the
// same setting as "64MiB"; operators type both).
func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) < len(suffix) {
		return "", false
	}
	if !strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return "", false
	}
	return s[:len(s)-len(suffix)], true
}

// UnmarshalYAML implements yaml.Unmarshaler. Both the string form
// ("64MiB") and a bare YAML integer are accepted.
func (z *Size) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		var n int64
		if nerr := value.Decode(&n); nerr != nil {
			return fmt.Errorf("expected a size such as \"64MiB\": %w", err)
		}
		if n < 0 {
			return fmt.Errorf("invalid size %d (must not be negative)", n)
		}
		*z = Size(n)
		return nil
	}
	parsed, err := ParseSize(s)
	if err != nil {
		return err
	}
	*z = parsed
	return nil
}

// MarshalYAML implements yaml.Marshaler, rendering the largest binary unit
// that divides the value exactly so a dumped configuration reads like the
// one that was written.
func (z Size) MarshalYAML() (any, error) { return z.String(), nil }

// String renders the value in the largest binary unit dividing it exactly,
// always with a unit suffix.
//
// The suffix is not decoration: without it the YAML form of a plain byte
// count would be an integer, which the marshaler then has to quote, and a
// dumped configuration would read `resumeThreshold: "4097"` where the
// operator wrote `4097`. With it, every value round-trips through the
// dump unquoted and unchanged.
func (z Size) String() string {
	n := int64(z)
	if n == 0 {
		return "0B"
	}
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"TiB", TiB}, {"GiB", GiB}, {"MiB", MiB}, {"KiB", KiB}} {
		if n%u.mult == 0 {
			return strconv.FormatInt(n/u.mult, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10) + "B"
}

// Bytes returns the quantity as a byte count.
func (z Size) Bytes() int64 { return int64(z) }
