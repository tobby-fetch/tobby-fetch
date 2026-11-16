// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
)

// The decision half of the Windows check, on every platform: raw access
// lists built here the way Win32 lays them out.

func sidBytes(t *testing.T, s string) []byte {
	t.Helper()
	parts := strings.Split(s, "-")
	auth, _ := strconv.ParseUint(parts[2], 10, 48)
	b := []byte{1, byte(len(parts) - 3), 0, 0, 0, 0, 0, 0} //nolint:gosec // G115: test SIDs have a handful of parts
	for i := range 6 {
		b[2+i] = byte(auth >> (8 * (5 - i))) //nolint:gosec // G115: one byte of the 48-bit authority, on purpose
	}
	for _, p := range parts[3:] {
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	return b
}

type testACE struct {
	typ, flags byte
	mask       uint32
	sid        string
	objFlags   uint32 // object ACEs
}

func rawACL(t *testing.T, aces ...testACE) []byte {
	t.Helper()
	var body []byte
	for _, a := range aces {
		ace := binary.LittleEndian.AppendUint32(nil, a.mask)
		if a.typ == aceAllowedObject || a.typ == aceAllowedCallbackObject {
			ace = binary.LittleEndian.AppendUint32(ace, a.objFlags)
			if a.objFlags&aceObjectTypePresent != 0 {
				ace = append(ace, make([]byte, 16)...)
			}
			if a.objFlags&aceInheritedObjectTypePresent != 0 {
				ace = append(ace, make([]byte, 16)...)
			}
		}
		ace = append(ace, sidBytes(t, a.sid)...)
		hdr := []byte{a.typ, a.flags, 0, 0}
		binary.LittleEndian.PutUint16(hdr[2:], uint16(4+len(ace))) //nolint:gosec // G115: test sizes
		body = append(append(body, hdr...), ace...)
	}
	acl := []byte{2, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(acl[2:], uint16(8+len(body))) //nolint:gosec // G115: test sizes
	binary.LittleEndian.PutUint16(acl[4:], uint16(len(aces)))   //nolint:gosec // G115: test sizes
	return append(acl, body...)
}

const (
	sidOwner   = "S-1-5-21-1111-2222-3333-1001"
	sidSystem  = "S-1-5-18"
	maskSync   = 0x00100000 // SYNCHRONIZE
	maskRead   = 0x00120089 // FILE_GENERIC_READ
	maskModify = 0x001301BF
)

func TestParseSIDRendersTheStringForm(t *testing.T) {
	for _, s := range []string{"S-1-1-0", "S-1-5-32-545", "S-1-15-2-1", sidOwner} {
		got, err := parseSID(sidBytes(t, s))
		if err != nil || got != s {
			t.Errorf("parseSID(%s) = %q, %v", s, got, err)
		}
	}
	if _, err := parseSID([]byte{1, 5, 0}); err == nil {
		t.Error("a truncated SID parsed")
	}
}

// TestBroadPrincipalsAreTheWholeHost: the extended list of the review (A4).
func TestBroadPrincipalsAreTheWholeHost(t *testing.T) {
	for _, s := range []string{
		"S-1-1-0", "S-1-5-32-545", "S-1-5-11", "S-1-5-4", "S-1-5-7", "S-1-5-32-546",
		"S-1-15-2-1", "S-1-15-2-2", "S-1-5-21-1-2-3-513", "S-1-5-21-1-2-3-514",
	} {
		if !broadSID(s) {
			t.Errorf("%s is not judged broad", s)
		}
	}
	for _, s := range []string{sidOwner, sidSystem, "S-1-5-32-544", "S-1-5-21-1-2-3-512", "S-1-3-0"} {
		if broadSID(s) {
			t.Errorf("%s is judged broad", s)
		}
	}
}

// TestTheACLVerdictReadsTypesFlagsAndMasks (A4): every allow form counts,
// inherit-only entries and deny entries do not, and a right that neither
// reads nor replaces (SYNCHRONIZE alone) is no exposure.
func TestTheACLVerdictReadsTypesFlagsAndMasks(t *testing.T) {
	owner := testACE{typ: aceAllowed, mask: 0x001F01FF, sid: sidOwner}
	for _, tc := range []struct {
		name   string
		ace    testACE
		rights uint32
		broad  bool
	}{
		{"Everyone read", testACE{typ: aceAllowed, mask: maskRead, sid: "S-1-1-0"}, secretFileRights, true},
		{"Everyone synchronize only", testACE{typ: aceAllowed, mask: maskSync, sid: "S-1-1-0"}, secretFileRights, false},
		{"Users generic read", testACE{typ: aceAllowed, mask: rightGenericRead, sid: "S-1-5-32-545"}, secretFileRights, true},
		{"INTERACTIVE modify", testACE{typ: aceAllowed, mask: maskModify, sid: "S-1-5-4"}, secretFileRights, true},
		{"Domain Users write only", testACE{typ: aceAllowed, mask: rightWriteData, sid: "S-1-5-21-9-9-9-513"}, secretFileRights, true},
		{"Everyone write DAC", testACE{typ: aceAllowed, mask: rightWriteDAC, sid: "S-1-1-0"}, secretFileRights, true},
		{"object ACE with types", testACE{typ: aceAllowedObject, mask: maskRead, sid: "S-1-5-11", objFlags: aceObjectTypePresent | aceInheritedObjectTypePresent}, secretFileRights, true},
		{"object ACE without types", testACE{typ: aceAllowedObject, mask: maskRead, sid: "S-1-15-2-1"}, secretFileRights, true},
		{"callback ACE", testACE{typ: aceAllowedCallback, mask: maskRead, sid: "S-1-5-32-546"}, secretFileRights, true},
		{"callback object ACE", testACE{typ: aceAllowedCallbackObject, mask: maskRead, sid: "S-1-5-7", objFlags: aceObjectTypePresent}, secretFileRights, true},
		{"inherit-only Everyone full", testACE{typ: aceAllowed, flags: aceInheritOnly | 0x3, mask: rightGenericAll, sid: "S-1-1-0"}, secretFileRights, false},
		{"deny Everyone", testACE{typ: 0x01, mask: maskRead, sid: "S-1-1-0"}, secretFileRights, false},
		{"SYSTEM full", testACE{typ: aceAllowed, mask: 0x001F01FF, sid: sidSystem}, secretFileRights, false},
		{"Users list and read on a directory", testACE{typ: aceAllowed, mask: maskRead, sid: "S-1-5-32-545"}, secretParentRights, false},
		{"Users add file on a directory", testACE{typ: aceAllowed, mask: rightWriteData | maskSync, sid: "S-1-5-32-545"}, secretParentRights, true},
		{"Users delete child on a directory", testACE{typ: aceAllowed, mask: rightDeleteChild, sid: "S-1-5-32-545"}, secretParentRights, true},
	} {
		sid, err := aclBroad(rawACL(t, owner, tc.ace), tc.rights)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if (sid != "") != tc.broad {
			t.Errorf("%s: broad = %q, want %v", tc.name, sid, tc.broad)
		}
	}
}

// TestAMalformedACLIsAnError: the caller refuses on it, never assumes the
// list narrow (A4: fail closed).
func TestAMalformedACLIsAnError(t *testing.T) {
	good := rawACL(t, testACE{typ: aceAllowed, mask: maskRead, sid: sidOwner})
	for name, b := range map[string][]byte{
		"short header":     good[:4],
		"size past buffer": append(append([]byte{}, good[:2]...), 0xff, 0xff, 1, 0, 0, 0),
		"count past list":  func() []byte { c := append([]byte{}, good...); c[4] = 3; return c }(),
		"truncated SID": func() []byte {
			c := append([]byte{}, good...)
			c[8+8+1] = 15 // subauthority count beyond the entry
			return c
		}(),
	} {
		if _, err := aclBroad(b, secretFileRights); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
