// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package secretfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// permOf reads the Unix permission bits. On Windows the mode is not what
// enforces anything — the access list is — so the assertions that read it
// skip there rather than assert something meaningless (NFR-018).
func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// TestWriteCreatesOwnerOnly is the NFR-020 acceptance on the created file:
// owner read/write, nothing for group or others.
func TestWriteCreatesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not carry the rule on Windows; the access list does")
	}
	path := filepath.Join(t.TempDir(), "nested", "secret.pem")
	if err := Write(path, []byte("planted")); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got != Mode {
		t.Errorf("file mode = %04o, want %04o", got, Mode)
	}
	if got := permOf(t, filepath.Dir(path)); got != DirMode {
		t.Errorf("directory mode = %04o, want %04o", got, DirMode)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the path is the test's own temporary directory
	if err != nil || string(raw) != "planted" {
		t.Fatalf("content = %q, %v", raw, err)
	}
}

// TestWriteNarrowsAnExistingLooseFile is the regression the certificate
// path taught: a secret whose file was once loosened must come back
// owner-only on the next write. Inheriting the mode of the target is right
// for a public certificate and wrong for a key.
func TestWriteNarrowsAnExistingLooseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not carry the rule on Windows; the access list does")
	}
	path := filepath.Join(t.TempDir(), "secret.pem")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil { //nolint:gosec // G306: deliberately loose — the fixture is the state Write must narrow
		t.Fatal(err)
	}
	if err := Write(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got != Mode {
		t.Errorf("file mode after replacing a 0644 target = %04o, want %04o", got, Mode)
	}
}

// TestWriteLeavesNoLooseTemporary: the atomic write must not leave a
// readable copy of the secret behind under any name.
func TestWriteLeavesNoLooseTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.pem")
	if err := Write(path, []byte("planted")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "secret.pem" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only secret.pem", names)
	}
}

// TestHardenNarrowsAnExistingFile covers the entry point for material this
// package did not write.
func TestHardenNarrowsAnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not carry the rule on Windows; the access list does")
	}
	path := filepath.Join(t.TempDir(), "secret.pem")
	if err := os.WriteFile(path, []byte("x"), 0o666); err != nil { //nolint:gosec // G306: deliberately loose — the fixture is the state Harden must narrow
		t.Fatal(err)
	}
	if err := Harden(path); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, path); got != Mode {
		t.Errorf("mode after Harden = %04o, want %04o", got, Mode)
	}
}

// TestMkdirAllCreatesOwnerOnly: a directory holding secrets is no more
// readable than the files in it — without the execute bit cleared, the
// file modes are the only barrier left.
func TestMkdirAllCreatesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not carry the rule on Windows; the access list does")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: deliberately loose — the fixture is the state MkdirAll must narrow
		t.Fatal(err)
	}
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, dir); got != DirMode {
		t.Errorf("directory mode = %04o, want %04o", got, DirMode)
	}
}

// TestWriteReportsWhereItFailed covers the refusal paths. A secret that
// could not be written must say so with the path in the message: silence
// here means an instance running on a key it never persisted.
func TestWriteReportsWhereItFailed(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), Mode); err != nil {
		t.Fatal(err)
	}

	t.Run("the parent cannot be created", func(t *testing.T) {
		err := Write(filepath.Join(blocker, "secret.pem"), []byte("s"))
		if err == nil || !strings.Contains(err.Error(), blocker) {
			t.Errorf("Write = %v, want a refusal naming %s", err, blocker)
		}
	})

	t.Run("the directory is not writable", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores permission bits")
		}
		ro := filepath.Join(dir, "read-only")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(ro, "secret.pem")
		err := Write(target, []byte("s"))
		if err == nil || !strings.Contains(err.Error(), "temp file") {
			t.Errorf("Write = %v, want a refusal about the temporary file", err)
		}
		if _, serr := os.Stat(target); serr == nil {
			t.Error("a failed write left a file behind")
		}
	})

	t.Run("the target cannot be replaced", func(t *testing.T) {
		// A directory where the secret file should be: the rename is the
		// last step, so this is the path that proves the temporary copy of
		// the secret does not survive a late failure.
		target := filepath.Join(dir, "occupied")
		if err := os.Mkdir(target, DirMode); err != nil {
			t.Fatal(err)
		}
		err := Write(target, []byte("s"))
		if err == nil || !strings.Contains(err.Error(), "replacing") {
			t.Fatalf("Write = %v, want a refusal about replacing the target", err)
		}
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			t.Fatal(rerr)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tobby-secret-") {
				t.Errorf("a failed write left %s behind: an unreferenced copy of key material", e.Name())
			}
		}
	})

	t.Run("hardening a file that is not there", func(t *testing.T) {
		err := Harden(filepath.Join(dir, "absent.pem"))
		if err == nil {
			t.Error("Harden on a missing file reported success")
		}
	})

	t.Run("the secret directory cannot be created", func(t *testing.T) {
		err := MkdirAll(filepath.Join(blocker, "state"))
		if err == nil || !strings.Contains(err.Error(), "secretfile:") {
			t.Errorf("MkdirAll = %v, want a refusal naming the package", err)
		}
	})
}

// TestWriteKeepsThePreviousSecretWhenTheFlushFails: the rename is what
// makes the replacement atomic, and it must never run on bytes that did
// not reach the disk — a secret half-written over a working one is an
// instance that cannot authenticate and cannot say why.
func TestWriteKeepsThePreviousSecretWhenTheFlushFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.pem")
	if err := Write(path, []byte("working")); err != nil {
		t.Fatal(err)
	}

	restore := syncFile
	syncFile = func(*os.File) error { return errors.New("no space left on device") }
	t.Cleanup(func() { syncFile = restore })

	err := Write(path, []byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "flushing") {
		t.Fatalf("Write = %v, want a refusal about the flush", err)
	}
	raw, rerr := os.ReadFile(path) //nolint:gosec // G304: the path is the test's own temporary directory
	if rerr != nil || string(raw) != "working" {
		t.Errorf("previous secret = %q, %v — want it untouched", raw, rerr)
	}
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		t.Fatal(derr)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the secret: a temporary copy survived", len(entries))
	}
}
