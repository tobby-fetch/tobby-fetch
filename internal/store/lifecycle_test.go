// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// FR-045: a prune unlinks the tags it is given, removes the repository a
// tag leaves bare, keeps one that still has a tag, absorbs a tag that is
// already gone, and reports what it did.
func TestPruneTags(t *testing.T) {
	old := sweepGrace
	sweepGrace = -1
	defer func() { sweepGrace = old }()
	ctx := context.Background()
	st := openTemp(t)
	logger := slog.New(slog.DiscardHandler)

	putTestManifest(t, st, "solo", "v1", "solo layer")
	putTestManifest(t, st, "pair", "v1", "pair layer one")
	putTestManifest(t, st, "pair", "v2", "pair layer two")
	if err := st.MarkManualImport("solo"); err != nil {
		t.Fatal(err)
	}

	if res, err := st.PruneTags(ctx, nil, logger); err != nil || res != (PruneResult{}) {
		t.Fatalf("an empty prune must do nothing: %+v, %v", res, err)
	}

	res, err := st.PruneTags(ctx, []TagRef{
		{Repo: "solo", Tag: "v1"},
		{Repo: "pair", Tag: "v1"},
		{Repo: "pair", Tag: "never-existed"},
		{Repo: "ghost", Tag: "v1"},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tags != 2 || res.Repositories != 1 {
		t.Errorf("prune result = %+v, want 2 tags and 1 repository", res)
	}
	if res.ContentSwept == 0 {
		t.Errorf("the pruned layers were not swept: %+v", res)
	}
	if _, ok := st.ResolveTag(ctx, "pair", "v2"); !ok {
		t.Error("the surviving tag was removed")
	}
	if _, ok := st.ResolveTag(ctx, "pair", "v1"); ok {
		t.Error("the pruned tag still resolves")
	}
	repos, err := st.Repositories(ctx)
	if err != nil || len(repos) != 1 || repos[0] != "pair" {
		t.Errorf("repositories = %v, %v; want only pair", repos, err)
	}
	if _, ok := st.ProvenanceOf("solo"); ok {
		t.Error("the provenance of a pruned repository survived")
	}
}

func TestDeleteTagReportsAMissingOne(t *testing.T) {
	st := openTemp(t)
	putTestManifest(t, st, "app", "v1", "layer")
	err := st.DeleteTag(context.Background(), "app", "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an absent tag: %v, want ErrNotFound", err)
	}
}

func TestResolveTag(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	want := putTestManifest(t, st, "app", "v1", "layer")
	if got, ok := st.ResolveTag(ctx, "app", "v1"); !ok || got != want {
		t.Errorf("ResolveTag = %q, %v; want %q", got, ok, want)
	}
	if _, ok := st.ResolveTag(ctx, "app", "v2"); ok {
		t.Error("an unknown tag resolved")
	}
	if _, ok := st.ResolveTag(ctx, "Not A Name", "v1"); ok {
		t.Error("an invalid repository name resolved")
	}
}

// FR-048: a locally packed FileSet is a unit import of local origin, and the
// ledger survives a reopening of the store.
func TestProvenanceLedgerPersists(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	if _, ok := st.ProvenanceOf("files"); ok {
		t.Fatal("provenance of unseen content")
	}
	if err := st.MarkManualImport("files"); err != nil {
		t.Fatal(err)
	}
	p, ok := st.ProvenanceOf("files")
	if !ok || p.Class != ProvenanceUnitImport || p.Origin != OriginLocalPack || p.RecordedAt.IsZero() {
		t.Fatalf("provenance = %+v, %v", p, ok)
	}
	again, err := Open(ctx, st.root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if p2, ok := again.ProvenanceOf("files"); !ok || p2.Origin != OriginLocalPack {
		t.Errorf("after reopening: %+v, %v", p2, ok)
	}
	if st.MediaID() == "" || st.MediaID() != again.MediaID() {
		t.Errorf("the medium identity must be minted once and stable: %q vs %q", st.MediaID(), again.MediaID())
	}
}

// R-31: the blob walk lists what is on disk, the check re-hashes it, and a
// corrupted or emptied blob is reported, not repaired.
func TestBlobWalkAndCheck(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	if blobs, err := st.Blobs(); err != nil || len(blobs) != 0 {
		t.Fatalf("an empty store has no blobs: %v, %v", blobs, err)
	}
	if err := writeTestArtifact(ctx, st, "app", "v1", "checked layer"); err != nil {
		t.Fatal(err)
	}
	blobs, err := st.Blobs()
	if err != nil || len(blobs) < 3 {
		t.Fatalf("blobs = %v, %v; want layer, config and manifest", blobs, err)
	}
	for i := 1; i < len(blobs); i++ {
		if blobs[i-1].Digest >= blobs[i].Digest {
			t.Fatal("blobs are not sorted by digest")
		}
	}
	for _, b := range blobs {
		if c := st.CheckBlob(ctx, b); !c.OK() || c.Size == 0 {
			t.Errorf("healthy blob %s reported %+v", b.Digest, c)
		}
	}

	layer := digest.FromString("checked layer")
	var victim BlobFile
	for _, b := range blobs {
		if b.Digest == layer {
			victim = b
		}
	}
	data := filepath.Join(st.root, filepath.FromSlash(victim.Path))
	if err := os.WriteFile(data, []byte("tampered!!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := st.CheckBlob(ctx, victim); c.OK() || c.Err != nil || c.Actual == layer {
		t.Errorf("a modified blob must read fine and hash differently: %+v", c)
	}
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	if c := st.CheckBlob(ctx, victim); c.OK() || c.Err == nil {
		t.Errorf("a missing data file must carry an error: %+v", c)
	}
	if c := st.CheckBlob(ctx, BlobFile{Digest: "sha256:zz", Path: victim.Path}); c.OK() || c.Err == nil {
		t.Errorf("a malformed digest must be refused: %+v", c)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, b := range blobs {
		if b.Digest == victim.Digest {
			continue
		}
		if c := st.CheckBlob(cancelled, b); c.OK() {
			t.Errorf("a cancelled verification must stop, got %+v", c)
		}
		break
	}
}

// Reachable is the mark phase without the sweep: everything under a tag,
// nothing else.
func TestReachableFollowsTags(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	putTestManifest(t, st, "app", "v1", "reachable layer")
	reach, err := st.Reachable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reach[digest.FromString("reachable layer")] || !reach[digest.FromString("{}")] {
		t.Errorf("layer or config not reachable: %v", reach)
	}
	if reach[digest.FromString("something else")] {
		t.Error("an unrelated digest is reachable")
	}
}

func TestRecipeManagedErrorNamesTheRecipes(t *testing.T) {
	e := &RecipeManagedError{Repo: "app", Recipes: []string{"web@1.0.0", "db@2.0.0"}}
	msg := e.Error()
	for _, want := range []string{"app", "web@1.0.0", "db@2.0.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not mention %q", msg, want)
		}
	}
}

func TestKindsAreStable(t *testing.T) {
	if len(Kinds()) == 0 {
		t.Fatal("no kinds")
	}
	seen := map[Kind]bool{}
	for _, k := range Kinds() {
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
}

// R-26: a store stamped with a layout this build does not know is refused,
// naming both versions and the standard-tooling way out.
func TestOpenRefusesAnUnknownFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "meta", "format.json"), []byte(`{"storeFormat": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "99") || !strings.Contains(err.Error(), "skopeo") {
		t.Fatalf("err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "meta", "format.json"), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("a corrupt format file was accepted")
	}
	if _, err := Open(context.Background(), "", slog.New(slog.DiscardHandler)); err == nil {
		t.Error("an empty root was accepted")
	}
}
