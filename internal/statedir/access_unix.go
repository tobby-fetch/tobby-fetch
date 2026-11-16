// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package statedir

import (
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// accessOf classifies a file by its permission bits: any bit for "other"
// is broad, any bit for the group is group access, the rest is owner-only.
//
// POSIX ACLs (setfacl) are not read: a named-user entry shows in the group
// bits as the ACL mask, so it is judged as group access. The backup and
// rebuild page states the limit.
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

// dirOpen reports whether other users can change the entries of a
// directory that holds a secret — replace accounts.yaml, swap the key
// under tls/: writable by "other", or sticky and owned by another account
// (the sticky bit protects the entries of their owners from each other,
// and the directory's owner from nobody).
func dirOpen(_ string, info fs.FileInfo) (open bool, mode string, err error) {
	perm := info.Mode().Perm()
	mode = "mode 0" + octal(perm)
	if info.Mode()&os.ModeSticky != 0 {
		mode = "mode 1" + octal(perm)
	}
	if perm&0o002 != 0 {
		return true, mode + ", writable by every user", nil
	}
	if info.Mode()&os.ModeSticky != 0 {
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
			return true, mode + ", sticky and owned by uid " + strconv.FormatUint(uint64(st.Uid), 10), nil
		}
	}
	return false, mode, nil
}

func octal(p fs.FileMode) string {
	const digits = "01234567"
	return string([]byte{digits[p>>6&7], digits[p>>3&7], digits[p&7]})
}
