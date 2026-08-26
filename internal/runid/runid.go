// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package runid generates the unique identifiers Tobby stamps on the
// things an incident is later traced through (R-09, FR-090): one per
// synchronization run, and one per transportable store.
//
// A run ID names one synchronization end to end: it is carried by every log
// record of the run, later written into the media manifest (FR-054), and
// reused by the destination-side instance — so a single filter reconstructs
// the whole story of a transfer, across the air gap included.
//
// A media ID names one physical medium (FR-054 amendment R-28). Both share
// the same shape — a compact UTC timestamp, then random bytes — because
// both are read by the same operators in the same logs, and because the
// property that matters is the same: chronologically sortable, unique
// without coordination, and safe in a file name.
package runid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// New returns a fresh run ID, e.g. "20260811T140322Z-1a2b3c4d".
//
// The format is a compact UTC timestamp followed by 4 random bytes in hex:
// sortable chronologically, safe in file names and repository paths, and
// unique without any coordination.
func New() string {
	return newAt(time.Now(), rand.Reader, runBytes)
}

// NewMedia returns a fresh media ID, e.g.
// "20260811T140322Z-1a2b3c4d5e6f7a8b" — the identity a transportable store
// is stamped with when it is created and keeps for its whole life (FR-054
// amendment R-28).
//
// Twice the randomness of a run ID, deliberately: run IDs are unique within
// one instance's logs, while media IDs are compared across zones and across
// years, by people holding a drawer of physical media. The extra four bytes
// cost nothing and remove the argument.
func NewMedia() string {
	return newAt(time.Now(), rand.Reader, mediaBytes)
}

// Random-suffix widths, in bytes.
const (
	runBytes   = 4
	mediaBytes = 8
)

func newAt(t time.Time, random io.Reader, n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(random, b); err != nil {
		// crypto/rand never fails on supported platforms; if it ever does,
		// operating without unique run IDs would silently corrupt audit
		// trails, which is worse than stopping.
		panic(fmt.Sprintf("runid: reading random bytes: %v", err))
	}
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}
