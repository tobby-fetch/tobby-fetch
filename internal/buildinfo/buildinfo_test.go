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
