// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package store

import (
	"errors"
	"fmt"
	"os"
)

// syncDir flushes a directory entry to the platter, so a renamed object
// survives a power loss rather than reappearing as a name pointing at
// nothing (NFR-010: an interruption never corrupts the store).
//
// This is the library's own behaviour, reproduced rather than inherited
// because PutContent had to be replaced around it (B-023).
func syncDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // G304: the directory a rename this package just performed landed in
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("store: opening %s to flush it: %w", dir, err)
	}
	defer f.Close() //nolint:errcheck // read-only directory handle; nothing is buffered on it
	if err := f.Sync(); err != nil {
		return fmt.Errorf("store: flushing %s: %w", dir, err)
	}
	return nil
}
