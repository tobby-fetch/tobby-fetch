// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package buildinfo

import (
	"strings"
	"testing"
)

func TestStringCarriesEveryField(t *testing.T) {
	s := String()
	if !strings.HasPrefix(s, "tobby ") {
		t.Errorf("String() = %q, want tobby prefix", s)
	}
	for _, part := range []string{"commit ", "built ", "go1"} {
		if !strings.Contains(s, part) {
			t.Errorf("String() = %q, missing %q", s, part)
		}
	}
}

func TestAccessorsNeverEmpty(t *testing.T) {
	if Version() == "" || Commit() == "" || Date() == "" {
		t.Errorf("accessors must never be empty: %q %q %q", Version(), Commit(), Date())
	}
}

// TestBuildTimeReadsOnlyRFC3339 pins FR-096's first clock anchor: a stamp
// that is not RFC 3339 proves nothing, a valid one is read back in UTC.
func TestBuildTimeReadsOnlyRFC3339(t *testing.T) {
	saved := date
	t.Cleanup(func() { date = saved })

	for _, bad := range []string{"unknown", "", "2026-11-16"} {
		date = bad
		if got, ok := BuildTime(); ok || !got.IsZero() {
			t.Errorf("BuildTime() with %q = %v, %v; want zero, false", bad, got, ok)
		}
	}

	date = "2026-11-16T14:00:00+02:00"
	got, ok := BuildTime()
	if !ok || got.Location().String() != "UTC" || got.Hour() != 12 {
		t.Errorf("BuildTime() = %v, %v; want 2026-11-16T12:00:00Z, true", got, ok)
	}
}

// TestStampedValuesWin checks that release stamps are returned verbatim,
// never replaced by what the module system knows.
func TestStampedValuesWin(t *testing.T) {
	savedV, savedC := version, commit
	t.Cleanup(func() { version, commit = savedV, savedC })
	version, commit = "v1.2.3", "abc123"
	if Version() != "v1.2.3" || Commit() != "abc123" {
		t.Errorf("stamped accessors = %q %q, want v1.2.3 abc123", Version(), Commit())
	}
}
