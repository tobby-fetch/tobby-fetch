// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package buildinfo exposes the version metadata stamped into the binary at
// build time. Release builds set the variables through -ldflags; development
// builds fall back to whatever the Go module system knows.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"
)

// Stamped by the release pipeline via
// -ldflags "-X github.com/tobby-fetch/tobby-fetch/internal/buildinfo.version=…".
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// Version returns the semantic version of the binary ("dev" when unstamped).
func Version() string {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	return version
}

// Commit returns the VCS revision the binary was built from.
func Commit() string {
	if commit == "unknown" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					return s.Value
				}
			}
		}
	}
	return commit
}

// Date returns the build date (from SOURCE_DATE_EPOCH in release builds).
func Date() string {
	return date
}

// BuildTime returns the build date as a time (FR-096: the first anchor of
// the clock-plausibility check). The zero time, and false, for a build
// that carries none — a development build stamps "unknown" — or one whose
// stamp is not RFC 3339: a date nobody can read proves nothing.
func BuildTime() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// String renders a single human-readable line, e.g.
// "tobby dev (commit unknown, built unknown, go1.25.6, darwin/arm64)".
func String() string {
	return fmt.Sprintf("tobby %s (commit %s, built %s, %s, %s/%s)",
		Version(), Commit(), Date(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
