// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package runid generates the unique identifier assigned to every
// synchronization run (R-09, FR-090).
//
// A run ID names one synchronization end to end: it is carried by every log
// record of the run, later written into the media manifest (FR-054), and
// reused by the destination-side instance — so a single filter reconstructs
// the whole story of a transfer, across the air gap included.
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
	return newAt(time.Now(), rand.Reader)
}

func newAt(t time.Time, random io.Reader) string {
	var b [4]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		// crypto/rand never fails on supported platforms; if it ever does,
		// operating without unique run IDs would silently corrupt audit
		// trails, which is worse than stopping.
		panic(fmt.Sprintf("runid: reading random bytes: %v", err))
	}
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}
