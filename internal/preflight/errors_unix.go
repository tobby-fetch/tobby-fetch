// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package preflight

// platformFileTooLarge has nothing to add outside Windows: EFBIG is the
// POSIX answer and IsFileTooLarge already matched it.
func platformFileTooLarge(error) bool { return false }
