// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build windows

package auth

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockRegion is the byte range the lock covers: one byte at 4 GiB, far
// past any marker. Windows byte-range locks are mandatory — a locked range
// cannot be read through another handle — so the range must not cover
// the content `tobby store verify` reads back.
func lockRegion() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1} }

// tryLock takes an exclusive LockFileEx without waiting. The lock belongs
// to the handle, so a second open in the same process conflicts too, and
// the system releases it when the process dies.
func tryLock(f *os.File) (held bool, err error) {
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRegion())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRegion())
}
