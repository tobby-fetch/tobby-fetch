// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the permission bits Go reports mean nothing (os.Chmod only
// toggles the read-only attribute), so the verdict is read from the file's
// discretionary access list and judged by acl.go: an allow entry granting
// a broad principal (Everyone, BUILTIN\Users, Authenticated Users,
// INTERACTIVE, Guests, Domain Users, ALL APPLICATION PACKAGES…) a right
// that reads or replaces the file makes it broad. Owner-only is what
// secretfile writes (NFR-020); any other principal is left alone —
// Administrators and SYSTEM are on every file anyway, and a group-style
// grant to a named account is the operator's decision. An access list that
// cannot be read is an error, which the startup check turns into a
// refusal: it never assumes narrow what it could not see.

func accessOf(path string, _ fs.FileInfo) (Access, string, error) {
	sid, err := broadIn(path, secretFileRights)
	if err != nil {
		return "", "", err
	}
	if sid != "" {
		return AccessBroad, "acl:" + sid, nil
	}
	return AccessOwner, "acl", nil
}

// dirOpen reports whether a broad principal may add, remove or rename the
// entries of a directory that holds a secret.
func dirOpen(path string, _ fs.FileInfo) (open bool, mode string, err error) {
	sid, err := broadIn(path, secretParentRights)
	if err != nil {
		return false, "", err
	}
	if sid != "" {
		return true, "acl:" + sid + ", writable by every user", nil
	}
	return false, "acl", nil
}

// broadIn reads the access list of path and returns the broad principal
// granted any of rights ("null" for a missing or NULL list), or "".
func broadIn(path string, rights uint32) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("reading the access list of %s: %w", path, err)
	}
	acl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND):
		// No access list at all: everyone has full access.
		return "null", nil
	case err != nil:
		return "", fmt.Errorf("reading the access list of %s: %w", path, err)
	case acl == nil:
		// A NULL DACL grants everyone full access.
		return "null", nil
	}
	hdr := unsafe.Slice((*byte)(unsafe.Pointer(acl)), 8) //nolint:gosec // G103: the ACL header, as the Win32 contract lays it out
	size := int(binary.LittleEndian.Uint16(hdr[2:]))
	raw := unsafe.Slice((*byte)(unsafe.Pointer(acl)), size) //nolint:gosec // G103: the whole ACL, whose size its header states
	sid, err := aclBroad(raw, rights)
	if err != nil {
		return "", fmt.Errorf("reading the access list of %s: %w", path, err)
	}
	return sid, nil
}
