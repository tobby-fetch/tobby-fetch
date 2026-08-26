// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// FR-046: the store reset. White-box, because the property that matters
// most is a lock contract — the reset must not run over a transfer in
// flight — and a lock is not observable from outside the package.

package store

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const resetRepo = "docker.io/library/alpine"

// seedForReset opens a store holding one image, one provenance entry and
// one recipe record.
func seedForReset(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})
	putImage(t, st, resetRepo, "3.22.1")
	if err := st.SetProvenance(resetRepo, &Provenance{Class: ProvenanceUnitImport}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecipeRecord(&RecipeRecord{
		Name: "alpine", Version: "1.0.0", CookbookRepo: "docker.io/cookbook/alpine",
		Digest: "sha256:" + strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// putImage stores a one-layer image under repo:tag.
func putImage(t *testing.T, st *Store, repo, tag string) digest.Digest {
	t.Helper()
	ctx := context.Background()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("layer bytes for " + repo + ":" + tag)
	descs := make([]ocispec.Descriptor, 0, 2)
	for _, blob := range [][]byte{config, layer} {
		d := digest.FromBytes(blob)
		if err := st.WriteBlob(ctx, repo, d, strings.NewReader(string(blob))); err != nil {
			t.Fatalf("writing blob: %v", err)
		}
		descs = append(descs, ocispec.Descriptor{Digest: d, Size: int64(len(blob))})
	}
	descs[0].MediaType = ocispec.MediaTypeImageConfig
	descs[1].MediaType = ocispec.MediaTypeImageLayerGzip
	payload, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    descs[0],
		Layers:    descs[1:],
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(ctx, repo, ocispec.MediaTypeImageManifest, payload, tag)
	if err != nil {
		t.Fatalf("storing the manifest: %v", err)
	}
	return d
}

// TestResetEmptiesTheStoreAndLeavesItUsable is the FR-046 acceptance
// criterion: "the instance is immediately usable on an empty store".
// Usable is checked by using it — a push after the reset, then a read —
// not by asserting a directory is gone.
func TestResetEmptiesTheStoreAndLeavesItUsable(t *testing.T) {
	ctx := context.Background()
	st := seedForReset(t)

	res, err := st.Reset(ctx, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("resetting: %v", err)
	}
	if res.Repositories != 1 {
		t.Errorf("reported %d repositories removed, want 1", res.Repositories)
	}
	if res.Bytes == 0 {
		t.Error("reported 0 bytes removed for a store that held content")
	}

	repos, err := st.Repositories(ctx)
	if err != nil {
		t.Fatalf("listing repositories after the reset: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("the store still holds %v", repos)
	}
	if _, ok := st.ProvenanceOf(resetRepo); ok {
		t.Error("the provenance ledger still describes content that is gone")
	}
	records, err := st.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("the recipe graph still holds %d records", len(records))
	}

	// Usable: push again, read it back.
	putImage(t, st, resetRepo, "3.23.0")
	if _, _, _, err := st.RawManifest(ctx, resetRepo, "3.23.0"); err != nil {
		t.Fatalf("the store is not usable after the reset: %v", err)
	}
}

// TestResetKeepsTheOperationHistory: a reset removes CONTENT, not the
// record of how the content got there. FR-094 asks the audit trail to be
// operational evidence, and evidence a destructive action erases is not
// evidence.
func TestResetKeepsTheOperationHistory(t *testing.T) {
	st := seedForReset(t)
	history := filepath.Join(st.Root(), "_tobby", "tasks")
	if err := os.MkdirAll(history, 0o750); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(history, "tsk_earlier.json")
	if err := os.WriteFile(kept, []byte(`{"id":"tsk_earlier"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Reset(context.Background(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("resetting: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the reset removed the operation history: %v", err)
	}
	// And the format marker, without which the emptied directory is no
	// longer a Tobby store (R-26).
	if _, err := os.Stat(filepath.Join(st.Root(), "meta", "format.json")); err != nil {
		t.Errorf("the reset removed the store format marker: %v", err)
	}
}

// TestResetWaitsForAnInFlightWrite locks the other half of the FR-044
// lock contract on the most destructive operation there is (B-017): a
// reset must not run over a transfer that is committing bytes.
//
// The write is held open on a reader that blocks; the reset is started
// behind it and must NOT complete while the write is in flight. Releasing
// the reader lets both finish.
func TestResetWaitsForAnInFlightWrite(t *testing.T) {
	ctx := context.Background()
	st := seedForReset(t)

	content := []byte("a blob that takes its time")
	release := make(chan struct{})
	blob := &blockingReader{content: content, release: release}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- st.WriteBlob(ctx, resetRepo, digest.FromBytes(content), blob)
	}()
	// Wait until the write actually holds the lock.
	waitFor(t, func() bool { return blob.started.Load() })

	var resetDone atomic.Bool
	go func() {
		_, err := st.Reset(ctx, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Errorf("resetting: %v", err)
		}
		resetDone.Store(true)
	}()

	// The reset must be waiting. A generous pause: the assertion is that
	// it did NOT happen, so a slow machine can only make this test more
	// patient, never flaky.
	time.Sleep(150 * time.Millisecond)
	if resetDone.Load() {
		t.Fatal("the reset completed while a content write was in flight")
	}

	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("the in-flight write failed: %v", err)
	}
	waitFor(t, resetDone.Load)
}

// blockingReader delivers its content only once release is closed, and
// reports when the first read reached it.
type blockingReader struct {
	content []byte
	release chan struct{}
	started atomic.Bool
	done    bool
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if b.done {
		return 0, io.EOF
	}
	b.started.Store(true)
	<-b.release
	n := copy(p, b.content)
	b.done = true
	return n, nil
}

// waitFor polls a condition until it holds or the test gives up.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
