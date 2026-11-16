// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the permission bits Go reports mean nothing (os.Chmod only
// toggles the read-only attribute), so the verdict is read from the file's
// discretionary access list: an ACE that allows access to Everyone,
// BUILTIN\Users or Authenticated Users makes the file broad. Owner-only is
// what secretfile writes (NFR-020); any other principal is left alone —
// Administrators and SYSTEM are on every file anyway, and a group-style
// grant to a named account is the operator's decision.

// broadSIDs are the principals that stand for "every user of the host".
var broadSIDs = []string{"S-1-1-0", "S-1-5-32-545", "S-1-5-11"}

func accessOf(path string, _ fs.FileInfo) (Access, string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", "", fmt.Errorf("reading the access list of %s: %w", path, err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return "", "", fmt.Errorf("reading the access list of %s: %w", path, err)
	}
	if acl == nil {
		// A NULL DACL grants everyone full access.
		return AccessBroad, "acl:null", nil
	}
	broad := map[string]bool{}
	for _, s := range broadSIDs {
		broad[s] = true
	}
	for i := range int(acl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return "", "", fmt.Errorf("reading the access list of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // G103: the SID follows the ACE header in the ACL buffer, as the Win32 contract defines
		if broad[sid.String()] {
			return AccessBroad, "acl:" + sid.String(), nil
		}
	}
	return AccessOwner, "acl", nil
}
