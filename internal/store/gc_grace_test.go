// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

// TestSweepGraceDefersFreshOrphans is the positive test of the FR-044
// grace period: a freshly committed orphan — blob data AND the layer
// link WriteBlob laid for it — survives a sweep and is counted in
// Deferred instead of silently kept; once older than the grace, the next
// sweep reclaims both. Every other GC test zeroes sweepGrace to get
// deterministic reclamation, so before this one the grace path and the
// Deferred counter had no test proving they do anything at all.
func TestSweepGraceDefersFreshOrphans(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()
	old := sweepGrace
	sweepGrace = time.Hour
	defer func() { sweepGrace = old }()

	payload := []byte("fresh-orphan")
	orphan := digest.FromBytes(payload)
	if err := st.WriteBlob(ctx, "docker.io/a/pending", orphan, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	// The untagged manifest makes the repository visible to the sweep's
	// catalog walk — a repository holding only blob links is not
	// enumerated — and is itself unreachable content under grace.
	manifestDigest := putTestManifest(t, st, "docker.io/a/pending", "", "fresh-orphan")

	// First sweep: everything is unreachable (no tag points at any of
	// it) but fresh — blobs and repository links are deferred, and the
	// deferral is COUNTED, never silent.
	st.gcMu.Lock()
	res, err := st.sweep(ctx)
	st.gcMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 || res.Deferred == 0 {
		t.Fatalf("fresh sweep = %+v, want nothing removed and the deferrals counted", res)
	}
	if !blobExists(st, orphan.String()) {
		t.Error("fresh orphan blob was swept despite the grace period")
	}
	if !st.HasBlob(ctx, "docker.io/a/pending", orphan) {
		t.Error("fresh layer link was pruned despite the grace period")
	}
	if !st.HasManifest(ctx, "docker.io/a/pending", manifestDigest) {
		t.Error("fresh revision link was pruned despite the grace period")
	}

	// Age everything past the grace: the second sweep reclaims exactly
	// what the first one deferred — deferral is a delay, never a leak.
	ageStoreContent(t, st, time.Now().Add(-2*time.Hour))
	st.gcMu.Lock()
	res2, err := st.sweep(ctx)
	st.gcMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if res2.Removed != res.Deferred || res2.Deferred != 0 {
		t.Errorf("aged sweep = %+v, want the %d deferred pieces removed and 0 deferred", res2, res.Deferred)
	}
	if blobExists(st, orphan.String()) {
		t.Error("aged orphan blob survived the second sweep")
	}
	if st.HasBlob(ctx, "docker.io/a/pending", orphan) {
		t.Error("aged layer link survived the second sweep")
	}
	if st.HasManifest(ctx, "docker.io/a/pending", manifestDigest) {
		t.Error("aged revision link survived the second sweep")
	}
}

// ageStoreContent rewinds the mtime of every blob data file and link
// file in the store, pushing all content past the sweep grace.
func ageStoreContent(t *testing.T, st *Store, when time.Time) {
	t.Helper()
	err := filepath.WalkDir(st.Root(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (d.Name() == "data" || d.Name() == "link") {
			return os.Chtimes(path, when, when)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRemovalDoesNotCollectAnInFlightTransfer is the B-017 regression
// lock. The direct-to-storage import commits blobs — repository links
// first — before the manifest that makes them reachable is tagged.
// Removing an UNRELATED repository sweeps the whole store, and before
// the fix pruneRepositoryLinks deleted unreachable links with no age
// check: only blob data enjoyed the grace, so the in-flight transfer's
// links vanished and its repository could no longer serve blobs it had
// just committed. The links must ride the same grace as the data.
func TestRemovalDoesNotCollectAnInFlightTransfer(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)
	old := sweepGrace
	sweepGrace = time.Hour
	defer func() { sweepGrace = old }()

	// The unrelated repository an operator removes mid-transfer.
	seedManifest(t, st, "docker.io/a/unrelated", "1.0", "unrelated-content")
	if err := st.SetProvenance("docker.io/a/unrelated", &Provenance{Class: ProvenanceUnitImport}); err != nil {
		t.Fatal(err)
	}

	// The transfer in flight: layers committed and child manifests stored
	// UNTAGGED — copyIndexChildren's order — while the tag that will make
	// them reachable has not been laid yet: the exact interstice B-017
	// names.
	payload := []byte("in-flight-layer")
	d := digest.FromBytes(payload)
	if err := st.WriteBlob(ctx, "docker.io/b/inflight", d, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	childDigest := putTestManifest(t, st, "docker.io/b/inflight", "", "in-flight-layer")
	if !st.HasBlob(ctx, "docker.io/b/inflight", d) || !st.HasManifest(ctx, "docker.io/b/inflight", childDigest) {
		t.Fatal("fixture: committed content must be linked before the removal")
	}

	if err := st.DeleteRepository(ctx, "docker.io/a/unrelated", logger); err != nil {
		t.Fatal(err)
	}

	// The failing assertions before the fix: the sweep triggered by the
	// unrelated removal pruned the fresh layer and revision links.
	if !st.HasBlob(ctx, "docker.io/b/inflight", d) {
		t.Fatal("in-flight layer link was swept by an unrelated removal (B-017)")
	}
	if !st.HasManifest(ctx, "docker.io/b/inflight", childDigest) {
		t.Fatal("in-flight untagged manifest was swept by an unrelated removal (B-017)")
	}

	// The transfer completes normally: manifest tagged, content served.
	manDigest := putTestManifest(t, st, "docker.io/b/inflight", "1.0", "in-flight-layer")
	if _, _, _, err := st.RawManifest(ctx, "docker.io/b/inflight", "1.0"); err != nil {
		t.Errorf("completed transfer does not serve its manifest: %v", err)
	}
	if !st.HasManifest(ctx, "docker.io/b/inflight", manDigest) {
		t.Error("completed transfer's manifest missing")
	}
}

// TestConcurrentContentWritesAndSweep exercises the FR-044 lock contract
// under the race detector: content writes hold the GC lock shared while
// sweeps hold it exclusively, so a store written to by several syncs
// while removals sweep it ends with every committed manifest and blob
// intact. The default-scale grace keeps in-flight content protected
// between a writer's two calls, exactly as in production.
func TestConcurrentContentWritesAndSweep(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)
	old := sweepGrace
	sweepGrace = time.Hour
	defer func() { sweepGrace = old }()

	const writers, perWriter = 4, 5
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				repo := fmt.Sprintf("docker.io/w%d/app", w)
				tag := fmt.Sprintf("1.%d", i)
				if err := writeTestArtifact(ctx, st, repo, tag, fmt.Sprintf("content-%d-%d", w, i)); err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
			}
		}(w)
	}
	sweeps := make(chan struct{})
	go func() {
		defer close(sweeps)
		for range 8 {
			if err := st.Sweep(ctx, logger); err != nil {
				t.Errorf("sweep: %v", err)
			}
		}
	}()
	wg.Wait()
	<-sweeps

	// Everything committed survived the concurrent sweeps.
	for w := range writers {
		for i := range perWriter {
			repo := fmt.Sprintf("docker.io/w%d/app", w)
			tag := fmt.Sprintf("1.%d", i)
			if _, _, _, err := st.RawManifest(ctx, repo, tag); err != nil {
				t.Errorf("%s:%s lost to a concurrent sweep: %v", repo, tag, err)
			}
		}
	}
}

// putTestManifest stores a single-layer manifest over layerContent
// (blobs assumed or written by the caller), optionally tagged, and
// returns its digest.
func putTestManifest(t *testing.T, st *Store, repo, tag, layerContent string) string {
	t.Helper()
	ctx := context.Background()
	layer := []byte(layerContent)
	cfg := []byte(`{}`)
	if err := st.WriteBlob(ctx, repo, digest.FromBytes(cfg), bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    digest.FromBytes(cfg).String(), "size": len(cfg),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar",
			"digest":    digest.FromBytes(layer).String(), "size": len(layer),
		}},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(ctx, repo, "application/vnd.oci.image.manifest.v1+json", raw, tag)
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// writeTestArtifact commits one single-layer artifact the way the import
// pipeline does — blobs first, then the tagged manifest — returning an
// error instead of failing the test, so goroutines can use it.
func writeTestArtifact(ctx context.Context, st *Store, repo, tag, layerContent string) error {
	layer := []byte(layerContent)
	cfg := []byte(`{}`)
	for _, b := range []struct {
		d digest.Digest
		p []byte
	}{{digest.FromBytes(layer), layer}, {digest.FromBytes(cfg), cfg}} {
		if err := st.WriteBlob(ctx, repo, b.d, bytes.NewReader(b.p)); err != nil {
			return err
		}
	}
	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    digest.FromBytes(cfg).String(), "size": len(cfg),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar",
			"digest":    digest.FromBytes(layer).String(), "size": len(layer),
		}},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		return err
	}
	_, err = st.PutManifest(ctx, repo, "application/vnd.oci.image.manifest.v1+json", raw, tag)
	return err
}
