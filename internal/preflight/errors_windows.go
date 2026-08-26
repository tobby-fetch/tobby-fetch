// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight

import (
	"errors"

	"golang.org/x/sys/windows"
)

// platformFileTooLarge recognizes the Win32 answer to writing past a
// filesystem's file-size ceiling.
//
// syscall.EFBIG exists on Windows as a Go constant, but nothing in the
// kernel ever returns it: NtWriteFile answers ERROR_FILE_TOO_LARGE (223)
// on a FAT32 volume, and ERROR_DISK_FULL (112) is what a full one
// answers. Matching only EFBIG would make the FR-055 mid-write branch
// dead code on exactly the platform NFR-018 puts in the operating scope
// of the mirror journey — and dead code that looks alive.
func platformFileTooLarge(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_TOO_LARGE)
}
