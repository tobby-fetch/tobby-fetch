// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package statedir

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestADirectoryHoldingASecretWritableByOthersIsRefused: whoever can write
// to the state root can replace accounts.yaml, and whoever can write to
// tls/ can swap the key; both refuse the start like a readable secret
// (adversarial review of milestone 7, A3).
func TestADirectoryHoldingASecretWritableByOthersIsRefused(t *testing.T) {
	for _, dir := range []string{".", TLS, TLS + "/..data"} {
		t.Run(dir, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if _, err := Prepare(root, Expect{}, "test"); err != nil {
				t.Fatal(err)
			}
			write(t, root, Accounts, "x", 0o600)
			write(t, root, TLS+"/..data/server.key", "k", 0o600)
			if _, err := Prepare(root, Expect{}, "test"); err != nil {
				t.Fatalf("an owner-only directory refused: %v", err)
			}
			target := filepath.Join(root, filepath.FromSlash(dir))
			if err := os.Chmod(target, 0o777); err != nil { //nolint:gosec // G302: the test opens the directory on purpose
				t.Fatal(err)
			}
			_, err := Prepare(root, Expect{}, "test")
			if codeOf(err) != taxonomy.CodeStatePermissions {
				t.Fatalf("%s at 0777: Prepare = %v, want TBY-STA-002", dir, err)
			}
			if text := taxonomy.Text("en", err.(*taxonomy.Error)); !strings.Contains(text, "writable by every user") { //nolint:errorlint // the assertion is the test's point
				t.Errorf("the refusal does not say why: %s", text)
			}
			// Group-writable (a volume's fsGroup, setgid) is tolerated.
			if err := os.Chmod(target, 0o770|fs.ModeSetgid); err != nil {
				t.Fatal(err)
			}
			if _, err := Prepare(root, Expect{}, "test"); err != nil {
				t.Errorf("a group-writable directory refused: %v", err)
			}
		})
	}
}

// fakeInfo is a directory as dirOpen sees it.
type fakeInfo struct {
	mode fs.FileMode
	uid  uint32
}

func (f fakeInfo) Name() string       { return "d" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode | fs.ModeDir }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return true }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

// TestAStickyDirectoryOwnedByAnotherAccountIsOpen: the sticky bit protects
// the entries of their owners from each other, never from the directory's
// owner.
func TestAStickyDirectoryOwnedByAnotherAccountIsOpen(t *testing.T) {
	me := uint32(os.Geteuid()) //nolint:gosec // G115: a uid fits
	for _, tc := range []struct {
		info fakeInfo
		open bool
	}{
		{fakeInfo{mode: 0o700, uid: me}, false},
		{fakeInfo{mode: 0o770 | fs.ModeSticky, uid: me}, false},
		{fakeInfo{mode: 0o770 | fs.ModeSticky, uid: me + 1}, true},
		{fakeInfo{mode: 0o777 | fs.ModeSticky, uid: me}, true},
		{fakeInfo{mode: 0o755, uid: me + 1}, false},
	} {
		if open, mode, _ := dirOpen("d", tc.info); open != tc.open {
			t.Errorf("%v uid %d: open = %v (%s), want %v", tc.info.mode, tc.info.uid, open, mode, tc.open)
		}
	}
}

// TestASymlinkedKeyIsJudgedByItsTarget: a key adopted under tls/ as a
// symbolic link — a projected Kubernetes volume, tls.key -> ..data/tls.key
// — is judged by the file it points to, not by the link's 0777 (review R2).
func TestASymlinkedKeyIsJudgedByItsTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if _, err := Prepare(root, Expect{}, "test"); err != nil {
		t.Fatal(err)
	}
	dir := Path(root, TLS)
	write(t, root, TLS+"/..data/tls.key", "k", 0o600)
	if err := os.Symlink("..data/tls.key", filepath.Join(dir, "tls.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "test"); err != nil {
		t.Fatalf("a link to an owner-only key refused: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "..data", "tls.key"), 0o644); err != nil { //nolint:gosec // G302: the test loosens the key on purpose
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "test"); codeOf(err) != taxonomy.CodeStatePermissions {
		t.Fatalf("a link to a world-readable key passed: %v", err)
	}
}

// TestAKeyAdoptedAsPEMIsJudged: the key is not always named *.key (R2).
func TestAKeyAdoptedAsPEMIsJudged(t *testing.T) {
	for _, name := range []string{"server.pem", "tls-key", "bundle.p12", "server.KEY"} {
		root := filepath.Join(t.TempDir(), "state")
		write(t, root, TLS+"/"+name, "k", 0o644)
		if _, err := Prepare(root, Expect{}, "test"); codeOf(err) != taxonomy.CodeStatePermissions {
			t.Errorf("%s at 0644 passed: %v", name, err)
		}
	}
	root := filepath.Join(t.TempDir(), "state")
	write(t, root, TLS+"/server.crt", "c", 0o644)
	write(t, root, TLS+"/ca.cer", "c", 0o644)
	if _, err := Prepare(root, Expect{}, "test"); err != nil {
		t.Errorf("public certificates refused: %v", err)
	}
}

// TestALostLayoutRecordStillReportsMissingIdentity: a partial restore that
// lost layout.json and accounts.yaml but kept the rest is adopted again —
// and the missing accounts are reported, not hidden by the adoption
// (review R3). A directory holding nothing of the layout is new and says
// nothing.
func TestALostLayoutRecordStillReportsMissingIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	expect := Expect{Accounts: true, Certificate: true}
	if _, err := Prepare(root, expect, "test"); err != nil {
		t.Fatal(err)
	}
	write(t, root, TLS+"/self-signed.key", "k", 0o600)
	write(t, root, Schedule, "{}", 0o600)
	if err := os.Remove(Path(root, LayoutFile)); err != nil {
		t.Fatal(err)
	}
	rep, err := Prepare(root, expect, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Adopted || len(rep.Missing) != 1 || rep.Missing[0] != Accounts {
		t.Errorf("report = %+v, want adopted with %s missing", rep, Accounts)
	}
	// An empty directory (a new volume) is new.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if rep, err := Prepare(empty, expect, "test"); err != nil || len(rep.Missing) != 0 {
		t.Errorf("an empty directory: %+v, %v", rep, err)
	}
}

// TestTheLayoutRecordIsCreatedNotFollowed: a link planted at layout.json
// does not make the adoption write through it (R3).
func TestTheLayoutRecordIsCreatedNotFollowed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(victim, Path(root, LayoutFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "test"); err == nil {
		t.Error("the adoption wrote through a planted link")
	}
	if _, err := os.Lstat(victim); err == nil {
		t.Error("the link's target was created")
	}
}
