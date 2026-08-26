// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FR-055 verdicts, executed.
//
// Every refusal below is reached by RUNNING the decision, never by
// asserting that a branch exists: the inspector is injected, so a FAT32
// volume is a value this test constructs and the refusal is the one
// production computes from it. The real detectors are covered separately,
// against the volume the test binary is actually running on, and — where
// the platform makes it possible without privileges — against a genuine
// FAT32 image (TestRealFAT32Volume).

// fakeFS is an injected inspector.
type fakeFS struct {
	fs    preflight.Filesystem
	space preflight.Space
	err   error
}

func (f *fakeFS) Inspect(string) (preflight.Filesystem, preflight.Space, error) {
	return f.fs, f.space, f.err
}

const (
	gib      = int64(1) << 30
	fatLimit = int64(1)<<32 - 1
)

// TestFAT32RefusesAFileOverFourGiB is the FR-055 acceptance criterion:
// "a FAT32 target with a > 4 GiB blob or export archive is refused naming
// the limit".
func TestFAT32RefusesAFileOverFourGiB(t *testing.T) {
	fat := &fakeFS{
		fs:    preflight.Filesystem{Type: "vfat", Identified: true, MaxFileSize: fatLimit, Detection: "test"},
		space: preflight.Space{FreeBytes: 500 * gib, TotalBytes: 500 * gib, Known: true},
	}

	// One byte over the ceiling: refused, with the limit named.
	check, refusal := preflight.Evaluate(fat, preflight.Request{
		Target:           preflight.TargetStore,
		Path:             "/media/usb",
		ProjectedBytes:   5 * gib,
		LargestFileBytes: fatLimit + 1,
	})
	if refusal == nil {
		t.Fatal("a 4 GiB + 1 byte file on FAT32 was not refused (FR-055)")
	}
	if refusal.Code() != taxonomy.CodeFileTooLarge {
		t.Errorf("refusal code = %s, want %s", refusal.Code(), taxonomy.CodeFileTooLarge)
	}
	if check.OK() {
		t.Error("the check reports OK while the operation was refused")
	}
	// The message must NAME the limit — that is the requirement's word.
	text := taxonomy.Text("en", refusal)
	for _, want := range []string{"vfat", "4294967295", "/media/usb"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, text)
		}
	}
	// And it must be legible in French too (FR-063).
	if fr := taxonomy.Text("fr", refusal); !strings.Contains(fr, "4294967295") || strings.Contains(fr, "TBY-STO-005 [") {
		t.Errorf("the French refusal did not render:\n%s", fr)
	}

	// Exactly at the ceiling: allowed. The boundary is the requirement,
	// not an approximation of it.
	if _, refusal := preflight.Evaluate(fat, preflight.Request{
		Target: preflight.TargetStore, Path: "/media/usb",
		ProjectedBytes: 5 * gib, LargestFileBytes: fatLimit,
	}); refusal != nil {
		t.Errorf("a file of exactly the FAT32 ceiling was refused: %s", refusal)
	}
}

// TestExportArchiveRefusedOnFAT32 covers the half of the requirement that
// is easy to forget: "including single-tar exports". The archive is one
// file, and its size is the number that matters — not the largest blob
// inside it.
func TestExportArchiveRefusedOnFAT32(t *testing.T) {
	fat := &fakeFS{
		fs:    preflight.Filesystem{Type: "msdos", Identified: true, MaxFileSize: fatLimit},
		space: preflight.Space{FreeBytes: 500 * gib, TotalBytes: 500 * gib, Known: true},
	}
	check, refusal := preflight.Evaluate(fat, preflight.Request{
		Target: preflight.TargetArchive, Path: "/media/usb/export.tar",
		ProjectedBytes:   6 * gib,
		LargestFileBytes: 6 * gib, // the archive itself
	})
	if refusal == nil {
		t.Fatal("a 6 GiB export archive on FAT32 was not refused (FR-055)")
	}
	if check.Target != preflight.TargetArchive {
		t.Errorf("check target = %q, want %q", check.Target, preflight.TargetArchive)
	}
	if !strings.Contains(taxonomy.Text("en", refusal), "archive") {
		t.Error("the refusal does not say what was being written")
	}
}

// TestInsufficientSpaceStatesTheShortfall is the other FR-055 acceptance
// criterion: "an oversized synchronization is refused before any transfer
// with the missing byte count stated".
func TestInsufficientSpaceStatesTheShortfall(t *testing.T) {
	// 100 bytes free, 10 % margin → 90 usable. Ask for 100: short by 10.
	ext4 := &fakeFS{
		fs:    preflight.Filesystem{Type: "ext4", Identified: true, MaxFileSize: 16 << 40},
		space: preflight.Space{FreeBytes: 100, TotalBytes: 1000, Known: true},
	}
	check, refusal := preflight.Evaluate(ext4, preflight.Request{
		Target: preflight.TargetStore, Path: "/var/lib/tobby",
		ProjectedBytes: 100, LargestFileBytes: 50,
	})
	if refusal == nil {
		t.Fatal("a projection larger than the usable space was not refused (FR-055)")
	}
	if refusal.Code() != taxonomy.CodeInsufficientSpace {
		t.Errorf("refusal code = %s, want %s", refusal.Code(), taxonomy.CodeInsufficientSpace)
	}
	if check.ReservedBytes != 10 || check.UsableBytes != 90 || check.ShortfallBytes != 10 {
		t.Errorf("arithmetic = reserved %d, usable %d, shortfall %d; want 10/90/10",
			check.ReservedBytes, check.UsableBytes, check.ShortfallBytes)
	}
	text := taxonomy.Text("en", refusal)
	if !strings.Contains(text, "10") || !strings.Contains(text, "/var/lib/tobby") {
		t.Errorf("the refusal does not state the shortfall and the path:\n%s", text)
	}

	// 90 exactly fits: the margin is a floor, not a rounding.
	if _, refusal := preflight.Evaluate(ext4, preflight.Request{
		Target: preflight.TargetStore, Path: "/var/lib/tobby", ProjectedBytes: 90,
	}); refusal != nil {
		t.Errorf("a projection of exactly the usable space was refused: %s", refusal)
	}
}

// TestConfigurableMargin locks that the margin is the operator's, and
// that its default is the 10 % FR-055 names.
func TestConfigurableMargin(t *testing.T) {
	fs := &fakeFS{
		fs:    preflight.Filesystem{Type: "ext4", Identified: true, MaxFileSize: 16 << 40},
		space: preflight.Space{FreeBytes: 1000, TotalBytes: 1000, Known: true},
	}
	for _, tc := range []struct {
		margin, wantReserved, wantUsable int64
	}{
		{0, 100, 900}, // unset → the FR-055 default of 10 %
		{10, 100, 900},
		{50, 500, 500},
		{1, 10, 990},
	} {
		check, _ := preflight.Evaluate(fs, preflight.Request{
			Target: preflight.TargetStore, Path: "/x", ProjectedBytes: 1,
			MarginPercent: int(tc.margin),
		})
		if check.ReservedBytes != tc.wantReserved || check.UsableBytes != tc.wantUsable {
			t.Errorf("margin %d%%: reserved %d usable %d, want %d/%d",
				tc.margin, check.ReservedBytes, check.UsableBytes, tc.wantReserved, tc.wantUsable)
		}
	}
	if preflight.DefaultMarginPercent != 10 {
		t.Errorf("the default margin is %d %%, and FR-055 says 10", preflight.DefaultMarginPercent)
	}
}

// TestUnidentifiedFilesystemWarnsAndProceeds is the honesty requirement:
// "SHALL warn when the filesystem cannot be identified". It must NOT
// refuse — that would ground the product on every filesystem this build
// has not heard of — and it must NOT stay silent.
func TestUnidentifiedFilesystemWarnsAndProceeds(t *testing.T) {
	unknown := &fakeFS{
		fs:    preflight.Filesystem{Type: "0xdeadbeef", Identified: false, Note: "unknown magic"},
		space: preflight.Space{FreeBytes: 100 * gib, TotalBytes: 100 * gib, Known: true},
	}
	check, refusal := preflight.Evaluate(unknown, preflight.Request{
		Target: preflight.TargetStore, Path: "/mnt/odd",
		ProjectedBytes: gib, LargestFileBytes: 8 * gib, // larger than FAT32 would take
	})
	if refusal != nil {
		t.Fatalf("an unidentified filesystem refused the operation: %s", refusal)
	}
	if !hasWarning(&check, preflight.WarnFilesystemUnidentified) {
		t.Error("an unidentified filesystem produced no warning (FR-055)")
	}
	if check.Filesystem.Identified {
		t.Error("the check claims the filesystem was identified")
	}
}

// TestUnreadableTargetWarnsTwiceAndProceeds: a target the platform will
// not describe at all is two unknowns, not one, and neither of them is a
// refusal.
func TestUnreadableTargetWarnsTwiceAndProceeds(t *testing.T) {
	broken := &fakeFS{err: errors.New("statfs: permission denied")}
	check, refusal := preflight.Evaluate(broken, preflight.Request{
		Target: preflight.TargetStore, Path: "/mnt/gone",
		ProjectedBytes: 100 * gib, LargestFileBytes: 100 * gib,
	})
	if refusal != nil {
		t.Fatalf("an unreadable target refused the operation: %s", refusal)
	}
	if !hasWarning(&check, preflight.WarnFilesystemUnidentified) || !hasWarning(&check, preflight.WarnSpaceUnknown) {
		t.Errorf("an unreadable target warned %v, want both unknowns reported", check.Warnings)
	}
	if !strings.Contains(check.Filesystem.Note, "permission denied") {
		t.Errorf("the check does not say why nothing could be read: %q", check.Filesystem.Note)
	}
}

// TestUnknownSpaceSkipsTheArithmetic: a volume nobody could measure is
// not a volume that is full. Refusing it would be a guess, and this
// package does not guess in either direction.
func TestUnknownSpaceSkipsTheArithmetic(t *testing.T) {
	noSpace := &fakeFS{fs: preflight.Filesystem{Type: "ext4", Identified: true, MaxFileSize: 16 << 40}}
	check, refusal := preflight.Evaluate(noSpace, preflight.Request{
		Target: preflight.TargetStore, Path: "/mnt/x", ProjectedBytes: 100 * gib,
	})
	if refusal != nil {
		t.Fatalf("an unmeasurable volume refused the operation: %s", refusal)
	}
	if !hasWarning(&check, preflight.WarnSpaceUnknown) {
		t.Error("an unmeasurable volume produced no warning")
	}
}

// TestFileSizeVerdictComesFirst: a FAT32 volume with terabytes free is
// still a volume that cannot hold the blob, and reporting "not enough
// space" for it would send an operator to buy a disk that does not fix
// anything.
func TestFileSizeVerdictComesFirst(t *testing.T) {
	tinyFAT := &fakeFS{
		fs:    preflight.Filesystem{Type: "fat32", Identified: true, MaxFileSize: fatLimit},
		space: preflight.Space{FreeBytes: 1, TotalBytes: 2, Known: true},
	}
	_, refusal := preflight.Evaluate(tinyFAT, preflight.Request{
		Target: preflight.TargetStore, Path: "/media/usb",
		ProjectedBytes: 100 * gib, LargestFileBytes: 10 * gib,
	})
	if refusal == nil || refusal.Code() != taxonomy.CodeFileTooLarge {
		t.Fatalf("refusal = %v, want the file-size verdict to win over the space one", refusal)
	}
}

func hasWarning(c *preflight.Check, w preflight.Warning) bool {
	for _, got := range c.Warnings {
		if got == w {
			return true
		}
	}
	return false
}

// TestSystemInspectorOnTheRunningVolume exercises the REAL per-platform
// detector against the volume this test binary is running on. It cannot
// assert which filesystem that is, so it asserts the invariants that must
// hold whatever it is — and the one that matters most: an identified
// filesystem always carries a limit, and an unidentified one never
// pretends to.
func TestSystemInspectorOnTheRunningVolume(t *testing.T) {
	dir := t.TempDir()
	fs, space, err := preflight.System.Inspect(dir)
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		if err == nil {
			t.Fatalf("%s has no inspector and must say so rather than answer", runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatalf("inspecting %s: %v", dir, err)
	}
	if fs.Identified && fs.MaxFileSize <= 0 {
		t.Errorf("filesystem %q is reported identified with no limit: %+v", fs.Type, fs)
	}
	if !fs.Identified && fs.MaxFileSize != 0 {
		t.Errorf("an unidentified filesystem carries a limit: %+v", fs)
	}
	if fs.Detection == "" {
		t.Error("the report does not say how the filesystem was detected")
	}
	if !space.Known || space.FreeBytes <= 0 || space.TotalBytes < space.FreeBytes {
		t.Errorf("space on the test volume = %+v, which cannot be right", space)
	}
	t.Logf("running on %s: %+v, %d bytes free of %d", runtime.GOOS, fs, space.FreeBytes, space.TotalBytes)
}

// TestInspectMissingPathUsesTheNearestAncestor: an export names a file
// that does not exist yet, and the check has to answer about the volume
// it would land on.
func TestInspectMissingPathUsesTheNearestAncestor(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skipf("no inspector on %s", runtime.GOOS)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "not", "created", "yet", "export.tar")
	fs, space, err := preflight.System.Inspect(missing)
	if err != nil {
		t.Fatalf("inspecting a path that does not exist yet: %v", err)
	}
	onDir, spaceDir, err := preflight.System.Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fs.Type != onDir.Type || space.TotalBytes != spaceDir.TotalBytes {
		t.Errorf("a missing path answered %+v/%+v, its existing ancestor %+v/%+v",
			fs, space, onDir, spaceDir)
	}
}

// TestIsFileTooLarge recognizes the POSIX errno through the wrappers the
// standard library puts around it — which is how it will arrive from a
// blob write (FR-055's clean mid-write failure).
func TestIsFileTooLarge(t *testing.T) {
	if !preflight.IsFileTooLarge(syscall.EFBIG) {
		t.Error("the bare errno is not recognized")
	}
	wrapped := &os.PathError{Op: "write", Path: "/media/usb/blob", Err: syscall.EFBIG}
	if !preflight.IsFileTooLarge(wrapped) {
		t.Error("a *os.PathError carrying EFBIG is not recognized")
	}
	if preflight.IsFileTooLarge(nil) || preflight.IsFileTooLarge(syscall.ENOSPC) {
		t.Error("something other than a file-size error was recognized as one")
	}
}

// TestRealFAT32Volume runs the whole FR-055 file-size verdict against a
// GENUINE FAT32 filesystem, created and mounted by the test.
//
// The injected fixtures above prove the decision; this proves the
// detector — that a real msdos volume is positively identified as one on
// the platform this build runs on. It is macOS-only because hdiutil is
// the one way to create and attach a filesystem image without
// privileges: on Linux, mkfs.vfat plus a loop mount needs root, which a
// test suite must not have. The Linux and Windows detectors are covered
// by TestSystemInspectorOnTheRunningVolume and by the injected fixtures;
// this is the extra assurance the platform makes cheap.
func TestRealFAT32Volume(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("a FAT32 image can only be attached without privileges on darwin (hdiutil)")
	}
	if _, err := exec.LookPath("hdiutil"); err != nil {
		t.Skip("hdiutil is not available")
	}
	dir := t.TempDir()
	image := filepath.Join(dir, "fat32.dmg")
	// An explicit mount point rather than /Volumes/<name>: two runs of
	// this test in one process (the anti-flaky `-count=2`) would otherwise
	// race for the same path, and the second would silently inspect the
	// first one's leftovers.
	mount := filepath.Join(dir, "mnt")
	if err := os.MkdirAll(mount, 0o750); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: the only variable argument is a path this test just built under its own t.TempDir()
	out, err := exec.Command("hdiutil", "create", "-quiet",
		"-size", "16m", "-fs", "MS-DOS", "-volname", "TOBBYFAT", image).CombinedOutput()
	if err != nil {
		t.Skipf("hdiutil create failed (%v): %s", err, out)
	}
	//nolint:gosec // G204: same paths, same provenance
	out, err = exec.Command("hdiutil", "attach", "-quiet", "-nobrowse", "-mountpoint", mount, image).CombinedOutput()
	if err != nil {
		t.Skipf("hdiutil attach failed (%v): %s", err, out)
	}
	t.Cleanup(func() {
		//nolint:gosec // G204: the mount point this test created
		_ = exec.Command("hdiutil", "detach", "-quiet", "-force", mount).Run()
	})

	fs, space, err := preflight.System.Inspect(mount)
	if err != nil {
		t.Fatalf("inspecting the FAT32 volume: %v", err)
	}
	if !fs.Identified {
		t.Fatalf("a real FAT32 volume was not identified: %+v", fs)
	}
	if fs.MaxFileSize != fatLimit {
		t.Errorf("FAT32 limit = %d, want %d", fs.MaxFileSize, fatLimit)
	}
	if !space.Known || space.TotalBytes <= 0 {
		t.Errorf("space on the FAT32 volume = %+v", space)
	}

	// The whole verdict, on the real volume: a blob past the ceiling is
	// refused before anything is written to it.
	_, refusal := preflight.Evaluate(preflight.System, preflight.Request{
		Target: preflight.TargetStore, Path: mount,
		ProjectedBytes: 1024, LargestFileBytes: fatLimit + 1,
	})
	if refusal == nil || refusal.Code() != taxonomy.CodeFileTooLarge {
		t.Fatalf("a > 4 GiB file on a real FAT32 volume was not refused: %v", refusal)
	}
	t.Logf("real FAT32 volume identified as %q by %s, ceiling %d", fs.Type, fs.Detection, fs.MaxFileSize)
}
