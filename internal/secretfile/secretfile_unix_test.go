// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package secretfile

import (
	"os"
	"testing"
)

// makeUnwritable removes the right to create files inside dir, and
// reports whether the environment actually enforces it.
//
// The refusal path it feeds is the one that matters most in this package
// — a secret that could not be written must SAY so — and the two
// operating systems in the validated scope (NFR-018) deny a write in
// completely different ways. Unix clears the write bit; Windows ignores
// it entirely, which is why this has a platform-specific twin rather than
// a chmod inline in the test.
func makeUnwritable(t *testing.T, dir string) bool {
	t.Helper()
	if os.Getuid() == 0 {
		return false // root ignores permission bits
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: removing the write bit is precisely what this helper exists to do
		t.Fatal(err)
	}
	return true
}
