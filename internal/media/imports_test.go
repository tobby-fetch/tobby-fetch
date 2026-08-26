// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
)

// The freshness register (R-28). Two properties matter and are tested
// here: it survives a restart, and it lives in the state directory rather
// than on the medium — a register that travelled with the store would be
// rewritten by whoever holds the store, which is the accident it exists to
// catch.

func TestImportsPersistAcrossRestart(t *testing.T) {
	state := t.TempDir()
	im, err := media.OpenImports(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := im.Last("zone-a"); ok {
		t.Fatal("a fresh instance already knows an import")
	}

	resolved := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	if err := im.Record("zone-a", &media.ImportRecord{
		MediaID: "20260826T090000Z-0011223344556677", ResolvedAt: resolved, RunID: "run-1",
	}); err != nil {
		t.Fatalf("recording an import: %v", err)
	}

	reopened, err := media.OpenImports(state)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := reopened.Last("zone-a")
	if !ok {
		t.Fatal("the record did not survive a restart")
	}
	if !rec.ResolvedAt.Equal(resolved) || rec.MediaID == "" || rec.RunID != "run-1" {
		t.Errorf("record came back altered: %+v", rec)
	}
	if rec.ImportedAt.IsZero() {
		t.Error("the record does not say when the import completed")
	}
	if zones := reopened.Zones(); len(zones) != 1 || zones[0] != "zone-a" {
		t.Errorf("zones = %v, want [zone-a]", zones)
	}
	if all := reopened.All(); len(all) != 1 {
		t.Errorf("All() = %v, want one zone", all)
	}
}

// TestImportsNeverGoBackwards: an admin who overrode the staleness guard
// to restore an older delivery does not thereby erase the fact that a
// newer one was imported.
func TestImportsNeverGoBackwards(t *testing.T) {
	im, err := media.OpenImports(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newer := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	older := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	if err := im.Record("zone-a", &media.ImportRecord{MediaID: "new", ResolvedAt: newer}); err != nil {
		t.Fatal(err)
	}
	if err := im.Record("zone-a", &media.ImportRecord{MediaID: "old", ResolvedAt: older}); err != nil {
		t.Fatal(err)
	}
	rec, _ := im.Last("zone-a")
	if !rec.ResolvedAt.Equal(newer) {
		t.Errorf("the register rolled back to %s; the high-water mark must hold at %s", rec.ResolvedAt, newer)
	}
	if rec.MediaID != "old" {
		t.Errorf("the register does not name the medium actually imported last: %q", rec.MediaID)
	}
}

// TestImportsPerZone keeps two zones independent.
func TestImportsPerZone(t *testing.T) {
	im, err := media.OpenImports(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	b := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	if err := im.Record("zone-a", &media.ImportRecord{ResolvedAt: a}); err != nil {
		t.Fatal(err)
	}
	if err := im.Record("zone-b", &media.ImportRecord{ResolvedAt: b}); err != nil {
		t.Fatal(err)
	}
	ra, _ := im.Last("zone-a")
	rb, _ := im.Last("zone-b")
	if !ra.ResolvedAt.Equal(a) || !rb.ResolvedAt.Equal(b) {
		t.Errorf("zones bled into each other: %s / %s", ra.ResolvedAt, rb.ResolvedAt)
	}
}

// TestImportsWithoutStateDirectoryRefuseToPretend: an instance with no
// state directory cannot keep the guard, and says so instead of accepting
// a write that would evaporate.
func TestImportsWithoutStateDirectoryRefuseToPretend(t *testing.T) {
	im, err := media.OpenImports("")
	if err != nil {
		t.Fatal(err)
	}
	err = im.Record("zone-a", &media.ImportRecord{ResolvedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("recording succeeded with nowhere to persist")
	}
	if !strings.Contains(err.Error(), "state.root") {
		t.Errorf("the refusal does not say what to configure: %v", err)
	}
}

// TestImportsRefuseAnEmptyZone: a record under no zone would silently
// disable the guard for every zone.
func TestImportsRefuseAnEmptyZone(t *testing.T) {
	im, err := media.OpenImports(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := im.Record("", &media.ImportRecord{ResolvedAt: time.Now().UTC()}); err == nil {
		t.Fatal("an import was recorded under no zone")
	}
}

// TestCorruptRegisterIsAStartupError: falling back to "no record" would
// turn a corrupted file into a disabled guard, at exactly the moment
// nobody is looking.
func TestCorruptRegisterIsAStartupError(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "media-imports.json"), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := media.OpenImports(state); err == nil {
		t.Fatal("a corrupt register opened as an empty one")
	}
}
