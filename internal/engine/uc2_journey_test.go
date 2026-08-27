// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/medialog"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// UC2 end to end — synchronize, transport, push on the destination side —
// with the transport leg actually played (NFR-018 acceptance).
//
// The individual legs are covered elsewhere and thoroughly: the fixture
// PRODUCES a medium by running a real mirror synchronization from a real
// source registry (mediafixture_test.go), and mediaimport_test.go asserts
// what a destination does with one. What none of them does is the step in
// the middle, and it is the step the whole milestone is named after: the
// store is unplugged from one machine and plugged into another. Nothing
// carried its path with it, nothing may have been left behind on the
// first host, and the second host may not spell paths the way the first
// one did.
//
// WHAT IS SIMULATED: the transport is a recursive directory copy. A
// GitHub runner has no removable device, and pretending otherwise would
// be theatre. The real block device — a USB stick, mounted, filled,
// unmounted and carried — is the crucible's job (ADR-0014), under Linux.
// What this proves is the property the copy CAN prove and the crucible
// cannot prove twice: that the store is self-contained (FR-050), that it
// verifies and pushes from a path it has never seen before, and that it
// does so on whichever operating system the runner is.
//
// WHAT IS NOT SIMULATED: everything else is real. A real source registry
// over the real /v2/ protocol, real cosign signatures, the real media
// manifest, the real destination-side verification order, and a real
// destination registry receiving real pushes.

// TestUC2SurvivesTheTransport is the journey.
func TestUC2SurvivesTheTransport(t *testing.T) {
	kp := newKeyPair(t)
	produced := seedMedium(t, kp,
		mediumRecipe{name: "alpine", version: "3.22.1"},
		// The second delivery is signed in the Sigstore bundle layout —
		// cosign 3.x's default, and the layout B-015 lived in the gap of.
		// A journey that only ever carried one layout would not have
		// caught that one either.
		mediumRecipe{name: "nginx", version: "1.25.0", bundleSig: true},
	)

	// The source side has finished. Everything below happens on the other
	// machine, and the only thing that crossed is the directory.
	if err := produced.st.Close(); err != nil {
		t.Fatalf("closing the store the medium was produced in: %v", err)
	}

	transported := carryToAnotherHost(t, produced.root())

	// NFR-020, on the artefact rather than on the configuration: a
	// courier is holding this directory. Nothing under it may be a secret
	// file, and the check runs on the copy because the copy is what
	// travels.
	assertNoSecretsOnTheMedium(t, transported)

	// FR-050: self-contained and relocatable. The store opens at a path
	// it has never occupied, and answers for its whole content.
	arrived, err := store.Open(context.Background(), transported, discardLogger())
	if err != nil {
		t.Fatalf("the transported store did not open at its new path (FR-050): %v", err)
	}
	t.Cleanup(func() {
		if cerr := arrived.Close(); cerr != nil {
			t.Errorf("closing the arrived store: %v", cerr)
		}
	})

	if _, serr := os.Stat(filepath.Join(transported, filepath.FromSlash(media.ManifestPath))); serr != nil {
		t.Fatalf("the transported store carries no media manifest (FR-054): %v", serr)
	}

	// FR-053: the destination-side operation writes its own trail INTO the
	// transported store, at a path outside the manifest's coverage. The
	// writer is composed here exactly as internal/cli/media.go composes it
	// — opened on the store root, teed into the operation's logger —
	// because the point is that the file lands on the MEDIUM, under
	// whatever path syntax the host uses. What is asserted at the CLI
	// level (TestMediaImportJournalsOntoTheMedium) is that the command
	// wires it; what is asserted here is that it works on the copy, at its
	// new path, on this operating system.
	mlog, err := medialog.Open(transported, medialog.Options{})
	if err != nil {
		t.Fatalf("opening the medium's operation log at its new path (FR-053): %v", err)
	}
	t.Cleanup(func() {
		if cerr := mlog.Close(); cerr != nil {
			t.Errorf("closing the medium's operation log: %v", cerr)
		}
	})
	mediumLogger := logging.Tee(discardLogger(), logging.New(mlog, slog.LevelDebug))

	// FR-052: the destination side opens the transported store and pushes
	// its content into the destination zone, after the FR-054
	// verification and with this instance's own trust roots.
	dest := newDestRegistry(t)
	arrivedMedium := &medium{
		st:               arrived,
		zone:             produced.zone,
		recipeDigest:     produced.recipeDigest,
		ingredientDigest: produced.ingredientDigest,
		resolvedAt:       produced.resolvedAt,
	}
	eng, imports := destinationFor(t, arrivedMedium, dest, produced.zone, kp)
	task := &tasks.Task{
		ID: "tsk_uc2", RunID: "run_uc2", Type: tasks.TypeMediaImport,
		Status: tasks.StatusRunning,
	}
	if err := eng.importMedia(context.Background(), newTaskSink(task, func() {}), mediumLogger, MediaOptions{}); err != nil {
		t.Fatalf("importing the transported medium: %v", err)
	}

	for _, id := range []string{"alpine@3.22.1", "nginx@1.25.0"} {
		item := itemByName(t, task, "media/"+id)
		if item.Status != tasks.StatusDone {
			t.Errorf("%s arrived %s, want done (%+v)", id, item.Status, item.Error)
		}
	}

	// Digests identical end to end, which is the acceptance sentence of
	// FR-052 word for word. The ingredient is compared at the destination
	// against the digest the SOURCE-side synchronization pinned, before
	// the directory was copied anywhere.
	for _, r := range []struct{ name, version string }{{"alpine", "3.22.1"}, {"nginx", "1.25.0"}} {
		id := r.name + "@" + r.version
		if tags := destTags(t, dest, cookbookRepoOf(r.name)); !containsString(tags, r.version) {
			t.Errorf("the zone cookbook holds %v, want %s (FR-034)", tags, r.version)
		}
		repo := "docker.io/library/" + r.name
		_, _, got, derr := dest.st.RawManifest(context.Background(), repo, r.version)
		if derr != nil {
			t.Errorf("%s did not reach the destination registry: %v", repo, derr)
			continue
		}
		if want := produced.ingredientDigest[id]; got != want {
			t.Errorf("%s arrived at digest %s, produced at %s: the journey changed the bytes", repo, got, want)
		}
	}

	if _, ok := imports.Last(produced.zone); !ok {
		t.Error("the destination recorded no import of a medium it pushed in full (R-28)")
	}

	// FR-053: the trail is on the medium, at the documented path, and it
	// narrates what was done — so the return journey carries the record.
	back := readIfPresent(t, filepath.Join(transported, filepath.FromSlash(medialog.DefaultPath)))
	if !strings.Contains(back, "media verification complete") {
		t.Errorf("the destination side left no usable trail on the medium it imported (FR-053): %q", back)
	}
	// And it landed OUTSIDE the manifest's coverage, so writing it did not
	// invalidate the inventory that was just verified.
	if media.Covered(medialog.DefaultPath) {
		t.Errorf("%s is inside manifest coverage: the return trail invalidates the medium it is written on",
			medialog.DefaultPath)
	}
}

// carryToAnotherHost copies a store to a directory it has never occupied and
// returns the new root — the simulated leg, and the only simulated thing
// in this test.
//
// It is a plain recursive copy rather than an archive round trip on
// purpose: an archive would normalize separators, modes and ordering on
// the way through, which is exactly the normalization a courier carrying
// a filesystem does not perform.
func carryToAnotherHost(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "removable-media", "tobby-store")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o750)
		case !d.Type().IsRegular():
			// A store holds regular files and directories, and a courier
			// carrying anything else on a FAT32 stick would lose it
			// silently. Say so rather than copy it.
			return errors.New("the store holds a non-regular file the transport cannot carry: " + rel)
		default:
			return copyFile(p, target)
		}
	})
	if err != nil {
		t.Fatalf("transporting the store: %v", err)
	}
	return dst
}

func copyFile(from, to string) error {
	in, err := os.Open(from) //nolint:gosec // G304: a path under the test's own temporary store
	if err != nil {
		return err
	}
	defer in.Close()                                                       //nolint:errcheck // read-only handle
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: a path under the test's own temporary directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// readIfPresent returns a file's content, or "" when it is not there.
func readIfPresent(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a path under the test's own temporary store
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

// assertNoSecretsOnTheMedium is the NFR-020 acceptance played on the
// produced artefact: "the e2e suite scans a produced medium for the
// planted secrets and finds none".
//
// It looks for the file NAMES the requirement enumerates rather than for
// their content, because a credentials file that is empty at the moment
// of the scan is still a credentials file that travelled.
func assertNoSecretsOnTheMedium(t *testing.T, root string) {
	t.Helper()
	forbidden := []string{
		"config.json", "dockerconfigjson", ".dockerconfigjson",
		"accounts.json", "tokens.json",
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		for _, bad := range forbidden {
			if name == bad {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("the medium carries %s: secrets never travel (NFR-020)", rel)
			}
		}
		if strings.HasSuffix(name, ".key") || strings.HasSuffix(name, "-key.pem") {
			rel, _ := filepath.Rel(root, p)
			t.Errorf("the medium carries the private key %s: secrets never travel (NFR-020)", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning the medium: %v", err)
	}
}

// TestUC2TransportPreservesEveryPathSeparator is the half of the journey
// that only Windows can fail.
//
// A store's own layout is a slash namespace — the repository names inside
// it, the keys of the media manifest's inventory — while its files live
// under whatever the host uses. Copying it between hosts is where the two
// get confused, and a manifest that inventoried `meta\recipes.json` on
// one side would fail verification on the other while naming a file that
// is plainly there.
func TestUC2TransportPreservesEveryPathSeparator(t *testing.T) {
	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})
	if err := m.st.Close(); err != nil {
		t.Fatal(err)
	}
	raw := readIfPresent(t, filepath.Join(m.root(), filepath.FromSlash(media.ManifestPath)))
	if raw == "" {
		t.Fatal("no media manifest was produced")
	}
	if strings.Contains(raw, `\\`) {
		t.Errorf("the media manifest inventories paths with a backslash on %s: "+
			"the inventory is a slash namespace and the destination reads it as one", runtime.GOOS)
	}
}
