// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package secretfile

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Owner-only access on Windows (NFR-020, NFR-018).
//
// A 0600 literal means nothing here: os.Chmod maps the write bit onto the
// read-only attribute and discards the rest, so a file "created 0600" on
// Windows is readable by every account the inherited ACL admits — in a
// domain-joined deployment, that is a great many of them. What enforces
// the rule is the discretionary access control list, replaced outright
// with a single entry naming the file's own owner, and marked PROTECTED so
// the parent directory's inheritable entries are not merged back in.
//
// The owner is read from the object rather than assumed to be the current
// process token: a file restored from a backup, or created by an
// installer running as another account, still has exactly one account able
// to read it after this call, and that account is the one that owns it.

// harden replaces the file's DACL with a single owner-only entry.
func harden(path string) error { return ownerOnlyDACL(path, windows.NO_INHERITANCE) }

// hardenDir does the same to a directory, and makes the entry inheritable
// so files created inside it start owner-only instead of relying on every
// writer to remember.
func hardenDir(path string) error {
	return ownerOnlyDACL(path, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

func ownerOnlyDACL(path string, inheritance uint32) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("secretfile: reading the owner of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("secretfile: reading the owner of %s: %w", path, err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(owner),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("secretfile: building the access list of %s: %w", path, err)
	}
	// PROTECTED_DACL_SECURITY_INFORMATION is the load-bearing half: without
	// it the inheritable entries of the parent directory are re-applied on
	// top of the one entry above, and the list that was meant to name one
	// account names whatever the directory named.
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("secretfile: restricting %s: %w", path, err)
	}
	return nil
}
