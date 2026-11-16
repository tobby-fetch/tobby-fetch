// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package interop_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// exportLayout writes a layout of one image to a fresh archive.
func exportLayout(t *testing.T) (archive, digest string) {
	t.Helper()
	src := openStore(t)
	img := putImage(t, src, ingredientRepo, "3.22.1", "alpine")
	out := filepath.Join(t.TempDir(), "payload.tar")
	if _, err := interop.New(src, nil, "", slog.New(slog.DiscardHandler)).Export(context.Background(),
		&interop.ExportRequest{Output: out}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	return out, img.Digest.String()
}

// TestLayoutImportRecordsItsProvenance (B-071): content imported from an
// OCI layout is a unit import of origin oci-layout — content no recipe
// manages, individually removable — not seeded content pushed through
// /v2/, which is what a repository without a ledger entry reads as. The
// never-downgrade rule still holds: a repository already holding seeded
// content stays seed.
func TestLayoutImportRecordsItsProvenance(t *testing.T) {
	archive, _ := exportLayout(t)
	dst := openStore(t)
	if _, err := interop.New(dst, nil, "", slog.New(slog.DiscardHandler)).
		Import(context.Background(), &interop.ImportRequest{Input: archive}, slog.New(slog.DiscardHandler), nil); err != nil {
		t.Fatalf("importing: %v", err)
	}
	c := dst.Classify(ingredientRepo)
	if c.Class != store.ProvenanceUnitImport || c.Origin != store.OriginOCILayout {
		t.Errorf("imported repository classified %+v, want unit-import of origin oci-layout", c)
	}

	seeded := openStore(t)
	putImage(t, seeded, ingredientRepo, "1.0.0", "pushed")
	if _, err := seeded.RecordSeed(ingredientRepo); err != nil {
		t.Fatal(err)
	}
	if _, err := interop.New(seeded, nil, "", slog.New(slog.DiscardHandler)).
		Import(context.Background(), &interop.ImportRequest{Input: archive}, slog.New(slog.DiscardHandler), nil); err != nil {
		t.Fatalf("importing: %v", err)
	}
	if c := seeded.Classify(ingredientRepo); c.Class != store.ProvenanceSeed {
		t.Errorf("a layout import downgraded seeded content to %+v", c)
	}
}

// TestLayoutImportRefusedWhenUnitImportDisabled (B-071): under
// hardening.disableUnitImport an OCI layout import is refused with
// TBY-POL-005 naming the setting — directly (the CLI), when enqueued (the
// API and the UI), and by the runner, which also stops a task queued
// before the switch was set and resumed after the restart that set it.
// Nothing lands.
func TestLayoutImportRefusedWhenUnitImportDisabled(t *testing.T) {
	archive, _ := exportLayout(t)
	dst := openStore(t)
	svc, queue := newQueued(t, dst)
	svc.SetUnitImportDisabled(true)

	_, err := svc.Import(context.Background(), &interop.ImportRequest{Input: archive}, slog.New(slog.DiscardHandler), nil)
	if code := codeOf(t, err); code != taxonomy.CodeUnitImportDisabled {
		t.Errorf("direct import = %v, want TBY-POL-005", err)
	}
	if _, err := svc.StartImport("alexis", &interop.ImportRequest{Input: archive}); codeOf(t, err) != taxonomy.CodeUnitImportDisabled {
		t.Errorf("enqueued import = %v, want TBY-POL-005", err)
	}

	queued, err := queue.Create(tasks.TypeLayoutImport, archive, "alexis", nil, tasks.WithLayout(&tasks.Layout{}))
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, queue, queued.ID)
	if done.Status != tasks.StatusFailed || done.Error == nil || done.Error.Code != taxonomy.CodeUnitImportDisabled {
		t.Errorf("a queued layout import ran under the switch: %s %+v", done.Status, done.Error)
	}
	if _, err := dst.RepoInfo(context.Background(), ingredientRepo); !errors.Is(err, store.ErrNotFound) {
		t.Error("a refused layout import landed content")
	}
}
