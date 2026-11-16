// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package statedir

import "io/fs"

// accessOf classifies a file by its permission bits: any bit for "other"
// is broad, any bit for the group is group access, the rest is owner-only.
func accessOf(_ string, info fs.FileInfo) (Access, string, error) {
	perm := info.Mode().Perm()
	mode := "0" + octal(perm)
	switch {
	case perm&0o007 != 0:
		return AccessBroad, mode, nil
	case perm&0o070 != 0:
		return AccessGroup, mode, nil
	}
	return AccessOwner, mode, nil
}

func octal(p fs.FileMode) string {
	const digits = "01234567"
	return string([]byte{digits[p>>6&7], digits[p>>3&7], digits[p&7]})
}
