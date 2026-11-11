// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package trivy

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// ownedByUs reports whether a directory belongs to the user this process
// runs as: a directory another user created holds what that user put
// there — a database the scanner would reuse, a binary it would run.
func ownedByUs(fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Geteuid(); int(st.Uid) != uid {
		return fmt.Errorf("it belongs to uid %d, not to uid %d this instance runs as", st.Uid, uid)
	}
	return nil
}

// privateMode reports whether nobody else may write into the directory.
func privateMode(fi fs.FileInfo) error {
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("its mode %04o lets other users write into it", perm)
	}
	return nil
}
