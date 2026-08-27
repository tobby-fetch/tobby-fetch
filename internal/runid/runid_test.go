// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package runid

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
)

var format = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{8}$`)

var mediaFormat = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{16}$`)

func TestFormat(t *testing.T) {
	id := New()
	if !format.MatchString(id) {
		t.Errorf("New() = %q, want format YYYYMMDDThhmmssZ-8hex", id)
	}
}

// TestMediaFormat locks the media identifier's shape (FR-054 amendment
// R-28): same sortable timestamp as a run ID, twice the random suffix.
func TestMediaFormat(t *testing.T) {
	id := NewMedia()
	if !mediaFormat.MatchString(id) {
		t.Errorf("NewMedia() = %q, want format YYYYMMDDThhmmssZ-16hex", id)
	}
	if format.MatchString(id) {
		t.Errorf("NewMedia() = %q must not be mistakable for a run ID", id)
	}
}

func TestChronologicallySortable(t *testing.T) {
	early := newAt(time.Date(2026, 8, 11, 14, 3, 22, 0, time.UTC), bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff}), runBytes)
	late := newAt(time.Date(2026, 8, 11, 14, 3, 23, 0, time.UTC), bytes.NewReader([]byte{0x00, 0x00, 0x00, 0x00}), runBytes)
	if early >= late {
		t.Errorf("lexicographic order must follow time: %q !< %q", early, late)
	}
	if want := "20260811T140322Z-ffffffff"; early != want {
		t.Errorf("newAt = %q, want %q", early, want)
	}
}

func TestTimestampIsUTC(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	local := time.Date(2026, 8, 11, 16, 3, 22, 0, paris) // 14:03:22 UTC
	id := newAt(local, bytes.NewReader([]byte{1, 2, 3, 4}), runBytes)
	if !strings.HasPrefix(id, "20260811T140322Z") {
		t.Errorf("id %q must use UTC, not local time", id)
	}
}

func TestUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := New()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate run ID %q", id)
		}
		seen[id] = struct{}{}
	}
}
