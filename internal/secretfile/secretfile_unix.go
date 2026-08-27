// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package secretfile

import (
	"fmt"
	"os"
)

// harden sets mode 0600: the owning account reads and writes, group and
// others get nothing (NFR-020).
func harden(path string) error {
	if err := os.Chmod(path, Mode); err != nil {
		return fmt.Errorf("secretfile: restricting %s: %w", path, err)
	}
	return nil
}

// hardenDir sets mode 0700 on a directory holding secret files: without
// the execute bit removed for group and others, the mode of the files
// inside is the only thing standing between them and a directory walk.
func hardenDir(path string) error {
	if err := os.Chmod(path, DirMode); err != nil {
		return fmt.Errorf("secretfile: restricting %s: %w", path, err)
	}
	return nil
}
