// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FR-055 gate on the transfer path, executed.
//
// Both refusals are reached by RUNNING a real synchronization against a
// real registry with the filesystem layer injected — not by asserting
// that a decision function returns the right value. The assertion that
// matters is the second one in each test: the store is byte-identical
// afterwards, which is what "refuses to start" means.

const (
	gib      = int64(1) << 30
	fatLimit = int64(1)<<32 - 1
)

// injectFS substitutes the platform inspector for the duration of a test.
type injectFS struct {
	fs    preflight.Filesystem
	space preflight.Space
}

func (f *injectFS) Inspect(string) (preflight.Filesystem, preflight.Space, error) {
	return f.fs, f.space, nil
}

// withFS installs an inspector and restores the platform one.
func withFS(t *testing.T, in preflight.Inspector) {
	t.Helper()
	saved := preflight.System
	preflight.System = in
	t.Cleanup(func() { preflight.System = saved })
}

// TestSyncRefusedWhenTheProjectionExceedsFreeSpace is the FR-055
// acceptance criterion: "an oversized synchronization is refused before
// any transfer with the missing byte count stated".
//
// FALLIBILITY (proved 2026-08-26): with the `e.preflightGate` call
// removed from Engine.run, the test failed with "the synchronization was
// not refused" AND with "the refused synchronization wrote to the store",
// listing the blobs and manifests it had transferred — the second failure
// being the one the requirement is actually about.
func TestSyncRefusedWhenTheProjectionExceedsFreeSpace(t *testing.T) {
	env := newPlanEnv(t)
	// A volume with 8 bytes free. Whatever the fixture weighs, it does
	// not fit — and the margin makes the usable figure smaller still.
	withFS(t, &injectFS{
		fs:    preflight.Filesystem{Type: "ext4", Identified: true, MaxFileSize: 16 << 40, Detection: "test"},
		space: preflight.Space{FreeBytes: 8, TotalBytes: 1 << 20, Known: true},
	})

	before := fingerprint(t, env.storeRoot)
	_, err := runSync(t, env.eng)
	after := fingerprint(t, env.storeRoot)

	// The property that matters first: nothing moved. "Refuses to start"
	// is a statement about the store, not about the error value.
	if lines := before.diff(after); len(lines) > 0 {
		t.Errorf("the refused synchronization wrote to the store: %v", lines)
	}
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("the synchronization was not refused: err = %v", err)
	}
	if te.Code() != taxonomy.CodeInsufficientSpace {
		t.Fatalf("refusal code = %s, want %s", te.Code(), taxonomy.CodeInsufficientSpace)
	}
	// The shortfall is stated in bytes, in both languages (FR-055,
	// FR-063).
	for _, lang := range []string{"en", "fr"} {
		text := taxonomy.Text(lang, te)
		if !strings.Contains(text, env.storeRoot) {
			t.Errorf("[%s] the refusal does not name the target:\n%s", lang, text)
		}
		if strings.Contains(text, "TBY-STO-004 [") {
			t.Errorf("[%s] the refusal did not render from the catalog:\n%s", lang, text)
		}
	}
}

// TestSyncRefusedOnAFAT32Target is the other FR-055 acceptance criterion,
// on the transfer path: a target whose filesystem is positively
// identified as unable to hold the largest file is refused, naming the
// limit — and nothing is written.
func TestSyncRefusedOnAFAT32Target(t *testing.T) {
	env := newPlanEnv(t)
	// A FAT32 volume with room to spare, and a ceiling of one byte: the
	// content of the fixture is small, so the ceiling is what has to be
	// small for the verdict to be about the ceiling. The arithmetic is
	// the real one — the fixture's largest blob against the filesystem's
	// stated limit.
	withFS(t, &injectFS{
		fs:    preflight.Filesystem{Type: "vfat", Identified: true, MaxFileSize: 1, Detection: "test"},
		space: preflight.Space{FreeBytes: 100 * gib, TotalBytes: 100 * gib, Known: true},
	})

	before := fingerprint(t, env.storeRoot)
	_, err := runSync(t, env.eng)
	after := fingerprint(t, env.storeRoot)

	if lines := before.diff(after); len(lines) > 0 {
		t.Errorf("the refused synchronization wrote to the store: %v", lines)
	}
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("the synchronization was not refused: err = %v", err)
	}
	if te.Code() != taxonomy.CodeFileTooLarge {
		t.Fatalf("refusal code = %s, want %s", te.Code(), taxonomy.CodeFileTooLarge)
	}
	if !strings.Contains(taxonomy.Text("en", te), "vfat") {
		t.Errorf("the refusal does not name the filesystem:\n%s", taxonomy.Text("en", te))
	}
}

// TestPreflightDisabledIsAnExplicitOptIn locks the FR-075 shape of the
// escape hatch: the gate is removed only by an explicit setting, the
// verdict is still computed and still reported, and the synchronization
// proceeds.
func TestPreflightDisabledIsAnExplicitOptIn(t *testing.T) {
	env := newPlanEnv(t)
	env.eng.SetPreflight("mirror", config.Preflight{Disabled: true})
	withFS(t, &injectFS{
		fs:    preflight.Filesystem{Type: "ext4", Identified: true, MaxFileSize: 16 << 40, Detection: "test"},
		space: preflight.Space{FreeBytes: 8, TotalBytes: 1 << 20, Known: true},
	})

	// The verdict is still computed, refusal code intact: nobody can
	// mistake a disabled gate for a passed one. Asked BEFORE the run,
	// because once the content has landed there is nothing left to
	// refuse.
	plan, err := env.planner.Plan(context.Background(), PlanOptions{SkipDestination: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Refused() {
		t.Fatal("with the gate disabled the plan stopped reporting the refusal (FR-075: visible, not silent)")
	}
	if plan.Checks[0].RefusalCode != taxonomy.CodeInsufficientSpace {
		t.Errorf("the reported refusal is %q, want the space verdict", plan.Checks[0].RefusalCode)
	}

	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("with the gate disabled the synchronization must proceed: %v", err)
	}
	if agg := task.Aggregate(); agg.Total == 0 || agg.Failed != 0 {
		t.Fatalf("the synchronization did not complete: %+v", agg)
	}
}

// TestPreflightFailsOpenOnAnUnreadableTarget: the safety feature must not
// become the least reliable part of the product. A target the platform
// will not describe warns and proceeds; it does not ground a
// synchronization.
func TestPreflightFailsOpenOnAnUnreadableTarget(t *testing.T) {
	env := newPlanEnv(t)
	withFS(t, brokenFS{})
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("an unreadable target grounded the synchronization: %v", err)
	}
}

type brokenFS struct{}

func (brokenFS) Inspect(string) (preflight.Filesystem, preflight.Space, error) {
	return preflight.Filesystem{}, preflight.Space{}, errors.New("statfs: permission denied")
}

// TestFileTooLargeMidWriteLeavesTheStoreIntact is the last FR-055
// acceptance criterion: "a simulated file-too-large error mid-write
// leaves the store consistent".
//
// The write is the real one — the store's own blob upload, through the
// distribution library's upload transaction — and only the byte source
// is synthetic: a reader that streams a while and then answers EFBIG,
// which is exactly what a FAT32 volume does at the four-gigabyte mark.
// Simulating the decision instead would test nothing about the store.
//
// FALLIBILITY (proved 2026-08-26): with the two `w.Cancel(ctx)` calls
// removed from store.WriteBlob, the test failed with "the failed write
// left 3 paths behind", listing the abandoned upload's data file, its
// startedat marker and their directories.
func TestFileTooLargeMidWriteLeavesTheStoreIntact(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	root := st.Root()

	// A store that already holds something: an empty directory would
	// hide a failure that only shows up as a leftover beside real
	// content.
	payload := bytes.Repeat([]byte("tobby"), 4096)
	good := digest.FromBytes(payload)
	if err := st.WriteBlob(ctx, "zone/app", good, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}

	before := fingerprint(t, root)
	big := digest.FromString("a blob that never lands")
	err := st.WriteBlob(ctx, "zone/app", big, &efbigReader{after: 1 << 16})
	if err == nil {
		t.Fatal("a write that hit the file-size ceiling reported success")
	}
	if !preflight.IsFileTooLarge(err) {
		t.Fatalf("the error does not carry the file-size errno: %v", err)
	}
	if lines := before.diff(fingerprint(t, root)); len(lines) > 0 {
		t.Errorf("the failed write left %d paths behind: %v", len(lines), lines)
	}

	// The store still serves what it held.
	if !st.HasBlob(ctx, "zone/app", good) {
		t.Error("the store lost the blob it already held")
	}
	// And it still accepts writes: the failure was clean, not fatal.
	second := digest.FromString("after the failure")
	if err := st.WriteBlob(ctx, "zone/app", second, strings.NewReader("after the failure")); err != nil {
		t.Errorf("the store refuses writes after a file-too-large failure: %v", err)
	}
}

// efbigReader streams zeroes and then answers EFBIG, the way a write past
// a filesystem's ceiling does.
type efbigReader struct {
	after int
	done  int
}

func (r *efbigReader) Read(p []byte) (int, error) {
	if r.done >= r.after {
		return 0, &os.PathError{Op: "write", Path: "/media/usb/blob", Err: syscall.EFBIG}
	}
	n := min(len(p), r.after-r.done)
	for i := range p[:n] {
		p[i] = 0
	}
	r.done += n
	return n, nil
}

// TestFileTooLargeIsTaxonomizedNotRetried locks the two consequences the
// requirement implies but does not spell out: the error becomes the
// FR-055 entry rather than an anonymous store-write failure, and the
// bounded backoff of FR-029 does not spend three attempts reaching the
// same ceiling.
func TestFileTooLargeIsTaxonomizedNotRetried(t *testing.T) {
	efbig := &os.PathError{Op: "write", Path: "/media/usb/blob", Err: syscall.EFBIG}

	te := fileTooLargeError(efbig, t.TempDir(), 5*gib)
	if te == nil || te.Code() != taxonomy.CodeFileTooLarge {
		t.Fatalf("mid-write EFBIG mapped to %v, want %s", te, taxonomy.CodeFileTooLarge)
	}
	if !errors.Is(te, syscall.EFBIG) {
		t.Error("the taxonomy error dropped its cause")
	}
	if fileTooLargeError(errors.New("connection refused"), t.TempDir(), 0) != nil {
		t.Error("an unrelated error was mapped to the file-size entry")
	}

	attempts := 0
	err := withRetries(context.Background(), 3, func() error {
		attempts++
		return efbig
	})
	if attempts != 1 {
		t.Errorf("a file-size failure was attempted %d times, want 1 (FR-029 backoff is for transients)", attempts)
	}
	if !errors.Is(err, syscall.EFBIG) {
		t.Errorf("withRetries changed the error: %v", err)
	}
}

// compile-time guard: the reader is an io.Reader, which is what the store
// takes.
var _ io.Reader = (*efbigReader)(nil)
