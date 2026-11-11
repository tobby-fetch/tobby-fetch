// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build windows

package trivy

import "io/fs"

// ownedByUs: on Windows, ownership and access are ACLs, which the mode
// bits do not reflect; the work directory lives in the state directory or
// in a directory this process created (os.MkdirTemp), whose ACL is the
// user's own.
func ownedByUs(fs.FileInfo) error { return nil }

// privateMode: see ownedByUs.
func privateMode(fs.FileInfo) error { return nil }
