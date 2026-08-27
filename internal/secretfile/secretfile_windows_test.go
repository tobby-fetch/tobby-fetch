// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package secretfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The NFR-020 acceptance on Windows: "created secret files carry the
// documented permissions" — an access list naming the owning account and
// nobody else.
//
// The Unix half of that sentence is checked by reading four permission
// bits. The Windows half has no bits to read: os.Stat reports a mode Go
// synthesizes from the read-only attribute, and asserting on it would
// pass on a file the whole domain can read. So these tests read back the
// thing that actually decides — the discretionary access control list —
// through the same Win32 API that wrote it.
//
// secretfile_windows.go was written, reviewed and shipped without ever
// executing (NFR-018: Windows joined the CI matrix after it). Every
// assertion below is therefore about something that had never been
// observed, not about something being kept from regressing.

// fileAllAccess is FILE_ALL_ACCESS, which golang.org/x/sys/windows does
// not export. The security subsystem maps the GENERIC_ALL an ACE is built
// with onto the object's specific rights when the descriptor is assigned,
// so a read-back ACE may carry either spelling of "full control".
const fileAllAccess = windows.ACCESS_MASK(0x1F01FF)

// dacl reads back the owner and the discretionary access list of a path,
// plus whether that list is PROTECTED — the flag that stops the parent's
// inheritable entries from being merged back in, and the half of
// ownerOnlyDACL that carries the guarantee.
func dacl(t *testing.T, path string) (owner *windows.SID, aces []*windows.ACCESS_ALLOWED_ACE, protected bool, sddl string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the security descriptor of %s: %v", path, err)
	}
	owner, _, err = sd.Owner()
	if err != nil {
		t.Fatalf("reading the owner of %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("reading the control flags of %s: %v", path, err)
	}
	list, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("reading the access list of %s: %v", path, err)
	}
	if list == nil {
		// A NULL DACL grants everyone everything. It is the one outcome
		// that would make every other assertion below vacuous.
		t.Fatalf("%s has no access list at all: a NULL DACL grants full access to everyone", path)
	}
	aces = make([]*windows.ACCESS_ALLOWED_ACE, list.AceCount)
	for i := range list.AceCount {
		if err := windows.GetAce(list, uint32(i), &aces[i]); err != nil {
			t.Fatalf("reading entry %d of the access list of %s: %v", i, path, err)
		}
	}
	return owner, aces, control&windows.SE_DACL_PROTECTED != 0, sd.String()
}

// sidOf returns the SID an ACE names. The SID is stored inline at the end
// of the ACE structure, which is why it is reached by pointer arithmetic
// rather than by a field.
func sidOf(ace *windows.ACCESS_ALLOWED_ACE) *windows.SID {
	//nolint:gosec // G103: the documented way to reach a variable-length ACE's
	// SID, explained above; there is no field to read instead.
	return (*windows.SID)(unsafe.Pointer(uintptr(unsafe.Pointer(ace)) +
		unsafe.Offsetof(ace.SidStart)))
}

// grantsFullControl reports an access mask meaning "everything", in
// either of the two spellings a read-back ACE can carry.
func grantsFullControl(mask windows.ACCESS_MASK) bool {
	return mask == windows.GENERIC_ALL || mask&fileAllAccess == fileAllAccess
}

// assertOwnerOnly is the whole promise in one place: exactly one entry,
// allowing everything, naming the object's own owner.
func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	owner, aces, protected, sddl := dacl(t, path)
	t.Logf("%s: %s", path, sddl)
	if len(aces) != 1 {
		t.Fatalf("%s has %d access-list entries, want exactly 1 (the owner's): %s",
			path, len(aces), sddl)
	}
	ace := aces[0]
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Errorf("%s: the single entry has type %d, want ACCESS_ALLOWED (%d)",
			path, ace.Header.AceType, windows.ACCESS_ALLOWED_ACE_TYPE)
	}
	if got := sidOf(ace); !got.Equals(owner) {
		t.Errorf("%s: the single entry names %s, want the owner %s", path, got, owner)
	}
	if !grantsFullControl(ace.Mask) {
		t.Errorf("%s: the owner's entry grants 0x%08x, which is not full control", path, ace.Mask)
	}
	if !protected {
		t.Errorf("%s: the access list is not PROTECTED — the parent's inheritable entries "+
			"are free to be merged back on top of it", path)
	}
}

// everyoneFullControl hangs an INHERITABLE "Everyone: full control" entry
// on a directory, unprotected, so it propagates to whatever is created
// inside.
//
// It is the fixture that makes the tests below falsifiable. Without it, a
// harden() that forgot PROTECTED_DACL_SECURITY_INFORMATION would still
// produce a one-entry list on a runner whose temporary directory happens
// to carry no inheritable entries, and the test would pass on code that
// does not work. With it, the entry is there, it WILL be re-merged, and
// the assertion of "exactly one entry" is the thing that catches it.
func everyoneFullControl(t *testing.T, dir string) {
	t.Helper()
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("building the Everyone SID: %v", err)
	}
	// TrusteeValueFromSID stores a Go pointer as a uintptr, so the SID has
	// to stay put until SetEntriesInAcl has copied it.
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(world)

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(world),
		},
	}}, nil)
	if err != nil {
		t.Fatalf("building the Everyone access list: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatalf("opening %s up to Everyone: %v", dir, err)
	}
}

// makeUnwritable removes the right to create files inside dir, and
// reports whether the environment actually enforces it.
//
// The Unix twin clears the write bit. Here that would do nothing at all:
// os.Chmod on Windows toggles the read-only attribute, which the
// filesystem does not consult when a file is created inside a directory.
// The right that has to go is FILE_ADD_FILE, so the list is replaced with
// one granting the owner read, traverse and delete — and not that.
//
// DELETE is deliberately kept: without it t.TempDir's cleanup cannot
// remove the directory it made, and a test that leaves the runner unable
// to tidy up fails for a reason that has nothing to do with what it
// checks. The original list is put back afterwards for the same reason.
func makeUnwritable(t *testing.T, dir string) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the owner of %s: %v", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("reading the owner of %s: %v", dir, err)
	}
	const readTraverseDelete = windows.ACCESS_MASK(
		windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE | windows.DELETE)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: readTraverseDelete,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(owner),
		},
	}}, nil)
	if err != nil {
		t.Fatalf("building the read-only access list of %s: %v", dir, err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatalf("making %s unwritable: %v", dir, err)
	}
	t.Cleanup(func() {
		// Back to inheriting from the parent, so the temporary tree can be
		// removed.
		_ = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
			nil, nil, nil, nil)
	})
	// An account holding SeRestorePrivilege (a full administrator running
	// elevated) bypasses the list entirely, exactly as root does on Unix.
	// Rather than inspect the token, ask the filesystem: if a file can
	// still be created here, this fixture proves nothing.
	probe := filepath.Join(dir, "privilege-probe")
	//nolint:gosec // G304: a path this test just built under its own t.TempDir.
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, Mode)
	if err != nil {
		return true
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return false
}

// TestWriteCreatesAnOwnerOnlyAccessList is the NFR-020 acceptance on the
// created file, read back from the object rather than assumed from the
// call that wrote it.
//
// The secret is written into a directory that grants Everyone full
// control and propagates it, which is the realistic shape of the problem:
// a store directory on a workstation inherits from its parent, and a
// secret created there without an explicit list is readable by whatever
// that parent admits.
func TestWriteCreatesAnOwnerOnlyAccessList(t *testing.T) {
	dir := t.TempDir()
	everyoneFullControl(t, dir)

	path := filepath.Join(dir, "secret.pem")
	if err := Write(path, []byte("planted")); err != nil {
		t.Fatal(err)
	}
	assertOwnerOnly(t, path)

	// And the account that owns it can still read it. An access list that
	// locks the instance out of its own key material would satisfy every
	// assertion above and be useless.
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the path is the test's own temporary directory
	if err != nil || string(raw) != "planted" {
		t.Fatalf("the owner cannot read back the secret it just wrote: %q, %v", raw, err)
	}
}

// TestHardenNarrowsAnInheritedAccessList covers the entry point for
// material this package did not write: a file that already exists,
// already carrying whatever its directory handed down.
func TestHardenNarrowsAnInheritedAccessList(t *testing.T) {
	dir := t.TempDir()
	everyoneFullControl(t, dir)
	path := filepath.Join(dir, "inherited.pem")
	if err := os.WriteFile(path, []byte("x"), Mode); err != nil {
		t.Fatal(err)
	}

	// The fixture is only a fixture if it took effect: prove the file
	// really did inherit more than one entry before Harden narrows it.
	if _, aces, _, sddl := dacl(t, path); len(aces) < 2 {
		t.Fatalf("the inheritable Everyone entry did not propagate to %s (%d entries: %s): "+
			"this test would prove nothing", path, len(aces), sddl)
	}
	if err := Harden(path); err != nil {
		t.Fatal(err)
	}
	assertOwnerOnly(t, path)
}

// TestMkdirAllHandsOwnerOnlyDownToItsChildren: hardenDir marks its single
// entry inheritable so that files created inside start owner-only instead
// of relying on every writer to remember. That is a promise about files
// this package never touches, so it is checked on one.
func TestMkdirAllHandsOwnerOnlyDownToItsChildren(t *testing.T) {
	parent := t.TempDir()
	everyoneFullControl(t, parent)

	dir := filepath.Join(parent, "state")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	assertOwnerOnly(t, dir)

	child := filepath.Join(dir, "written-by-someone-else.db")
	if err := os.WriteFile(child, []byte("user database"), Mode); err != nil {
		t.Fatal(err)
	}
	// The child's own list is not protected — it is inherited — so this
	// asserts the inherited content rather than reusing assertOwnerOnly.
	owner, aces, _, sddl := dacl(t, child)
	t.Logf("%s: %s", child, sddl)
	if len(aces) != 1 {
		t.Fatalf("a file created inside an owner-only directory has %d access-list entries, "+
			"want exactly the inherited owner entry: %s", len(aces), sddl)
	}
	if got := sidOf(aces[0]); !got.Equals(owner) {
		t.Errorf("the inherited entry names %s, want the owner %s", got, owner)
	}
	if !grantsFullControl(aces[0].Mask) {
		t.Errorf("the inherited entry grants 0x%08x, which is not full control", aces[0].Mask)
	}
}
