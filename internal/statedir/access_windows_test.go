// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"os/exec"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/secretfile"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestWindowsBroadACLIsRefused: a secret entry written the way Tobby
// writes it is owner-only; granting Everyone read access to it is what a
// restore that lost its ACLs looks like, and the start is refused
// (TBY-STA-002). The equivalent of "any permission for other" on Unix.
func TestWindowsBroadACLIsRefused(t *testing.T) {
	root := t.TempDir()
	path := Path(root, Accounts)
	if err := secretfile.Write(path, []byte("version: 1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "v1"); err != nil {
		t.Fatalf("an owner-only entry was refused: %v", err)
	}
	if out, err := exec.Command("icacls", path, "/grant", "*S-1-1-0:(R)").CombinedOutput(); err != nil { //nolint:gosec // G204: fixed arguments, test-owned path
		t.Skipf("icacls unavailable: %v: %s", err, out)
	}
	_, err := Prepare(root, Expect{}, "v1")
	if codeOf(err) != taxonomy.CodeStatePermissions {
		t.Fatalf("Prepare = %v, want TBY-STA-002 for an entry granted to Everyone", err)
	}
}

func icacls(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil { //nolint:gosec // G204: fixed arguments, test-owned path
		t.Skipf("icacls unavailable: %v: %s", err, out)
	}
}

// TestWindowsExtendedBroadPrincipalsAreRefused (review A4): the principals
// beyond Everyone and Users that stand for the whole host, granted read.
func TestWindowsExtendedBroadPrincipalsAreRefused(t *testing.T) {
	for _, sid := range []string{"S-1-5-4", "S-1-5-11", "S-1-5-32-546", "S-1-15-2-1"} {
		t.Run(sid, func(t *testing.T) {
			root := t.TempDir()
			path := Path(root, Accounts)
			if err := secretfile.Write(path, []byte("version: 1\n")); err != nil {
				t.Fatal(err)
			}
			icacls(t, path, "/grant", "*"+sid+":(R)")
			if _, err := Prepare(root, Expect{}, "v1"); codeOf(err) != taxonomy.CodeStatePermissions {
				t.Fatalf("Prepare = %v, want TBY-STA-002 for an entry readable by %s", err, sid)
			}
		})
	}
}

// TestWindowsSynchronizeOnlyIsNoExposure: a right that neither reads nor
// replaces the file is not a refusal (the mask is examined).
func TestWindowsSynchronizeOnlyIsNoExposure(t *testing.T) {
	root := t.TempDir()
	path := Path(root, Accounts)
	if err := secretfile.Write(path, []byte("version: 1\n")); err != nil {
		t.Fatal(err)
	}
	icacls(t, path, "/grant", "*S-1-1-0:(S)")
	if _, err := Prepare(root, Expect{}, "v1"); err != nil {
		t.Fatalf("SYNCHRONIZE for Everyone refused: %v", err)
	}
}

// TestWindowsAStateRootWritableByUsersIsRefused (review A3): whoever can
// add files to the state root can replace accounts.yaml.
func TestWindowsAStateRootWritableByUsersIsRefused(t *testing.T) {
	root := t.TempDir()
	if err := secretfile.Write(Path(root, Accounts), []byte("version: 1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "v1"); err != nil {
		t.Fatalf("an owner-only root was refused: %v", err)
	}
	icacls(t, root, "/grant", "*S-1-5-32-545:(WD,AD)")
	if _, err := Prepare(root, Expect{}, "v1"); codeOf(err) != taxonomy.CodeStatePermissions {
		t.Fatalf("Prepare = %v, want TBY-STA-002 for a root Users can write to", err)
	}
}
