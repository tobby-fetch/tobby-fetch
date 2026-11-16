// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The decision half of the Windows access check, written over the raw
// bytes of an access list as Win32 lays it out (ACL header, then ACEs) so
// that it is plain Go, tested on every platform; access_windows.go only
// fetches those bytes. A file is "broad" when an allow entry that applies
// to the file itself grants a principal standing for "every user of the
// host" a right that matters: reading or replacing a secret file, adding,
// removing or renaming entries in a directory that holds one.
//
// Deny entries are not subtracted: an allow to Everyone narrowed by a deny
// is refused anyway — the check fails towards the refusal, and the
// corrective action (remove the broad entry) is the same.

// ACE types that grant access. The object and callback forms carry the
// same mask; Windows evaluates them on files as well.
const (
	aceAllowed               = 0x00
	aceAllowedObject         = 0x05
	aceAllowedCallback       = 0x09
	aceAllowedCallbackObject = 0x0B

	aceInheritOnly = 0x08 // AceFlags: applies to children only

	aceObjectTypePresent          = 0x1 // object ACE Flags
	aceInheritedObjectTypePresent = 0x2
)

// Access rights.
const (
	rightReadData      = 0x00000001 // FILE_READ_DATA / FILE_LIST_DIRECTORY
	rightWriteData     = 0x00000002 // FILE_WRITE_DATA / FILE_ADD_FILE
	rightAppendData    = 0x00000004 // FILE_APPEND_DATA / FILE_ADD_SUBDIRECTORY
	rightDeleteChild   = 0x00000040 // FILE_DELETE_CHILD
	rightDelete        = 0x00010000 // DELETE
	rightWriteDAC      = 0x00040000 // WRITE_DAC: can grant itself anything
	rightWriteOwner    = 0x00080000 // WRITE_OWNER: can take the file
	rightMaximum       = 0x02000000 // MAXIMUM_ALLOWED
	rightGenericAll    = 0x10000000
	rightGenericWrite  = 0x40000000
	rightGenericRead   = 0x80000000
	rightsTakeOver     = rightWriteDAC | rightWriteOwner | rightMaximum | rightGenericAll
	rightsReadFile     = rightReadData | rightGenericRead | rightsTakeOver
	rightsWriteFile    = rightWriteData | rightAppendData | rightDelete | rightGenericWrite | rightsTakeOver
	rightsWriteDir     = rightWriteData | rightAppendData | rightDeleteChild | rightDelete | rightGenericWrite | rightsTakeOver
	secretFileRights   = rightsReadFile | rightsWriteFile
	secretParentRights = rightsWriteDir
)

// broadSIDs are the well-known principals that stand for "every user of
// the host", or for code running on behalf of any of them.
var broadSIDs = map[string]string{
	"S-1-1-0":      "Everyone",
	"S-1-2-0":      "LOCAL",
	"S-1-2-1":      "CONSOLE LOGON",
	"S-1-5-2":      "NETWORK",
	"S-1-5-4":      "INTERACTIVE",
	"S-1-5-7":      "ANONYMOUS LOGON",
	"S-1-5-11":     "Authenticated Users",
	"S-1-5-13":     "TERMINAL SERVER USER",
	"S-1-5-14":     "REMOTE INTERACTIVE LOGON",
	"S-1-5-32-545": "BUILTIN\\Users",
	"S-1-5-32-546": "BUILTIN\\Guests",
	"S-1-15-2-1":   "ALL APPLICATION PACKAGES",
	"S-1-15-2-2":   "ALL RESTRICTED APPLICATION PACKAGES",
}

// broadSID reports whether sid stands for every user of the host: one of
// broadSIDs, or a domain's Domain Users (RID 513) or Domain Guests (514).
func broadSID(sid string) bool {
	if _, ok := broadSIDs[sid]; ok {
		return true
	}
	if strings.HasPrefix(sid, "S-1-5-21-") {
		return strings.HasSuffix(sid, "-513") || strings.HasSuffix(sid, "-514")
	}
	return false
}

// parseSID renders a binary SID as its string form (S-R-I-S…).
func parseSID(b []byte) (string, error) {
	if len(b) < 8 {
		return "", errors.New("SID too short")
	}
	n := int(b[1])
	if b[0] != 1 || len(b) < 8+4*n {
		return "", errors.New("malformed SID")
	}
	var auth uint64
	for _, x := range b[2:8] {
		auth = auth<<8 | uint64(x)
	}
	var sb strings.Builder
	sb.WriteString("S-1-")
	sb.WriteString(strconv.FormatUint(auth, 10))
	for i := range n {
		sb.WriteByte('-')
		sb.WriteString(strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b[8+4*i:])), 10))
	}
	return sb.String(), nil
}

// aclBroad walks a raw access list and returns the first broad principal
// an allow entry effective on the object grants any of rights, or "". A
// malformed list is an error: the caller refuses rather than guess.
func aclBroad(acl []byte, rights uint32) (string, error) {
	if len(acl) < 8 {
		return "", errors.New("access list too short")
	}
	size := int(binary.LittleEndian.Uint16(acl[2:]))
	count := int(binary.LittleEndian.Uint16(acl[4:]))
	if size < 8 || size > len(acl) {
		return "", fmt.Errorf("access list size %d out of bounds", size)
	}
	off := 8
	for i := range count {
		if off+4 > size {
			return "", fmt.Errorf("entry %d runs past the access list", i)
		}
		typ, flags := acl[off], acl[off+1]
		aceSize := int(binary.LittleEndian.Uint16(acl[off+2:]))
		if aceSize < 8 || off+aceSize > size {
			return "", fmt.Errorf("entry %d has size %d", i, aceSize)
		}
		ace := acl[off : off+aceSize]
		off += aceSize
		if flags&aceInheritOnly != 0 {
			continue
		}
		var sidAt int
		switch typ {
		case aceAllowed, aceAllowedCallback:
			sidAt = 8
		case aceAllowedObject, aceAllowedCallbackObject:
			if len(ace) < 12 {
				return "", fmt.Errorf("entry %d: object entry too short", i)
			}
			of := binary.LittleEndian.Uint32(ace[8:])
			sidAt = 12
			if of&aceObjectTypePresent != 0 {
				sidAt += 16
			}
			if of&aceInheritedObjectTypePresent != 0 {
				sidAt += 16
			}
		default:
			continue // deny and audit entries grant nothing
		}
		mask := binary.LittleEndian.Uint32(ace[4:])
		if mask&rights == 0 {
			continue
		}
		if sidAt > len(ace) {
			return "", fmt.Errorf("entry %d: SID past the entry", i)
		}
		sid, err := parseSID(ace[sidAt:])
		if err != nil {
			return "", fmt.Errorf("entry %d: %w", i, err)
		}
		if broadSID(sid) {
			return sid, nil
		}
	}
	return "", nil
}
