// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight

import (
	"errors"
	"syscall"
)

// IsFileTooLarge reports the operating system's "this file cannot get any
// bigger" error, whatever it is called on the platform.
//
// It exists because FR-055 asks for two behaviours around the same
// condition, and only one of them can be decided in advance. The
// pre-flight refuses a target whose filesystem is positively identified
// as unable to hold the largest file; this one covers the case that
// slipped past it — an unidentified filesystem, a medium swapped between
// the check and the run, a quota — where the failure arrives mid-write
// and the requirement is that it be clean and the store intact.
//
// Recognizing it is what turns an anonymous "writing blob failed" into
// the message that names the 4 GiB ceiling and the way out. The check is
// on the errno rather than on a message: a wrapped *os.PathError carries
// the errno through errors.Is, and a string match would break on the
// first localized libc.
func IsFileTooLarge(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EFBIG) {
		return true
	}
	return platformFileTooLarge(err)
}
