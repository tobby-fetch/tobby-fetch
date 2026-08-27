// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package medialog_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/medialog"
)

// childEnv, when set, makes the FR-056 test run as the doomed child
// process instead of as the parent. Its value is the store root.
const childEnv = "TOBBY_MEDIALOG_KILL_TEST_ROOT"

// TestMediaLogStaysOutsideManifestCoverage is the refusal that keeps the
// medium verifiable: a log written inside the manifest's coverage
// invalidates the inventory the destination checks, one line at a time.
//
// The rule is not restated here — the test asks media.Covered the same
// question the writer asks, so a change to the coverage definition cannot
// leave the two disagreeing.
func TestMediaLogStaysOutsideManifestCoverage(t *testing.T) {
	root := t.TempDir()

	for _, bad := range []string{
		"meta/operations.log",                    // beside the manifest's own bookkeeping
		"docker/registry/v2/operations.log",      // inside the content
		"meta/../docker/registry/v2/blobs/x.log", // not in clean form
		"/var/log/tobby.log",                     // outside the store entirely
		"../escape.log",                          // ditto, relatively
		`_tobby\logs\ops.log`,                    // a Windows separator (NFR-018)
		"C:/logs/ops.log",                        // a drive designator (NFR-018)
		"C:logs.txt",                             // ditto, drive-relative
	} {
		if _, err := medialog.Open(root, medialog.Options{Path: bad}); err == nil {
			t.Errorf("Open(%q) succeeded; a media log must never live there", bad)
		}
	}

	// And the default is a location the manifest genuinely does not cover.
	if media.Covered(medialog.DefaultPath) {
		t.Errorf("the default media log path %q is inside manifest coverage", medialog.DefaultPath)
	}
	w, err := medialog.Open(root, medialog.Options{})
	if err != nil {
		t.Fatalf("Open with defaults: %v", err)
	}
	defer w.Close() //nolint:errcheck // test cleanup
	want := filepath.Join(root, filepath.FromSlash(medialog.DefaultPath))
	if w.Path() != want {
		t.Errorf("Path() = %q, want %q", w.Path(), want)
	}
}

// TestMediaLogRotatesBySize is FR-056's second half: the log stays inside
// its budget, so a campaign of imports cannot fill the medium it is
// trying to deliver.
func TestMediaLogRotatesBySize(t *testing.T) {
	root := t.TempDir()
	const maxBytes = 512
	w, err := medialog.Open(root, medialog.Options{MaxBytes: maxBytes, Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	logger := logging.New(w, slog.LevelInfo)
	for i := range 200 {
		logger.LogAttrs(context.Background(), slog.LevelInfo, "narrating an import",
			slog.Int("step", i), slog.String("filler", strings.Repeat("x", 64)))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	live := sizeOf(t, w.Path())
	if live > maxBytes {
		t.Errorf("the live log holds %d bytes, above the %d budget", live, maxBytes)
	}
	// Two generations kept and no more: the third must have been dropped,
	// or the budget is a suggestion rather than a bound.
	for gen := 1; gen <= 2; gen++ {
		if sizeOf(t, w.Path()+"."+strconv.Itoa(gen)) == 0 {
			t.Errorf("generation .%d is missing: 200 records over a %d-byte budget must have rotated", gen, maxBytes)
		}
	}
	if _, err := os.Stat(w.Path() + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("generation .3 exists although keep is 2: the budget is unbounded")
	}
	// Whatever survived is still readable line by line: rotation must
	// never split a record across two files.
	for _, path := range []string{w.Path(), w.Path() + ".1", w.Path() + ".2"} {
		for _, line := range readLines(t, path) {
			if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
				t.Errorf("%s holds a torn record: %.60q", filepath.Base(path), line)
			}
		}
	}
}

// TestMediaLogSurvivesAKilledProcess is the FR-056 acceptance, tested by
// execution rather than by inspection: "killing the process immediately
// after a task completes leaves that task's entries readable on the
// media".
//
// A child process writes the entries of a task, calls Sync at the task
// boundary the way the queue's own hook does, and then kills itself
// outright — no deferred close, no flush on the way out, nothing the
// runtime can do on its behalf. The parent then reads the medium. Any
// implementation that buffered those entries anywhere would hand back a
// short file.
func TestMediaLogSurvivesAKilledProcess(t *testing.T) {
	if root := os.Getenv(childEnv); root != "" {
		writeThenDie(t, root)
		return
	}
	root := t.TempDir()
	//nolint:gosec // G204: the command is this very test binary
	cmd := exec.Command(os.Args[0], "-test.run=^TestMediaLogSurvivesAKilledProcess$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"="+root)
	err := cmd.Run()
	if err == nil {
		t.Fatal("the child exited normally: it was supposed to be killed mid-flight, " +
			"so this run proves nothing about durability")
	}

	lines := readLines(t, filepath.Join(root, filepath.FromSlash(medialog.DefaultPath)))
	if len(lines) < 3 {
		t.Fatalf("the medium holds %d records after the kill, want the 3 the task wrote: %v", len(lines), lines)
	}
	for i, want := range []string{"task started", "delivery pushed", "task finished"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("record %d = %.120q, want one about %q", i, lines[i], want)
		}
	}
}

// writeThenDie is the child half of the test above.
func writeThenDie(t *testing.T, root string) {
	w, err := medialog.Open(root, medialog.Options{})
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	logger := logging.New(w, slog.LevelInfo)
	for _, msg := range []string{"task started", "delivery pushed", "task finished"} {
		logger.LogAttrs(context.Background(), slog.LevelInfo, msg,
			slog.String("run_id", "run_child"))
	}
	// The task boundary: exactly what tasks.WithBoundary invokes.
	if err := w.Sync(); err != nil {
		t.Fatalf("child: sync: %v", err)
	}
	// And now the medium is yanked, as far as this process is concerned.
	// Kill rather than os.Exit: os.Exit still unwinds nothing but leaves
	// the runtime a chance to flush, and the point is that nothing was
	// waiting to be flushed.
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	_ = p.Kill()
	select {} // unreachable on any platform where Kill works
}

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a path this test built
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
