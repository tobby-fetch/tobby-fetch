// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
)

// danglingChild is an index child known by its digest only: what a client
// that never uploaded it can still name in a sparse index.
type danglingChild struct{ d v1.Hash }

func (c danglingChild) MediaType() (types.MediaType, error) { return types.OCIManifestSchema1, nil }
func (c danglingChild) Size() (int64, error)                { return 256, nil }
func (c danglingChild) Digest() (v1.Hash, error)            { return c.d, nil }
func (c danglingChild) RawManifest() ([]byte, error)        { return []byte("{}"), nil }

// TestSparseIndexPinsNoForeignBlob: the embedded registry accepts a
// sparse index from any client (B-036), so an index can name, as a child,
// the digest of a blob its pusher never uploaded — a layer of another
// repository. That child is not held by the index's repository; it must
// keep nothing alive: once the repository that owns the layer is removed,
// the sweep reclaims it (FR-044), and the reachability R-31 reports
// orphans against does not count it (B-043).
func TestSparseIndexPinsNoForeignBlob(t *testing.T) {
	old := sweepGrace
	sweepGrace = -1
	defer func() { sweepGrace = old }()
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	victim, err := name.NewRepository(addr + "/victim")
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(victim.Tag("v1"), img); err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	layer, err := layers[0].Digest()
	if err != nil {
		t.Fatal(err)
	}

	// Another client knows the layer's digest, nothing more.
	idx := mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex),
		mutate.IndexAddendum{Add: danglingChild{d: layer}, Descriptor: v1.Descriptor{Digest: layer, Size: 256, MediaType: types.OCIManifestSchema1}})
	other, err := name.NewRepository(addr + "/other")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(other.Tag("pin"), idx); err != nil {
		t.Fatalf("sparse index refused (B-036 regression): %v", err)
	}

	if err := st.DeleteRepository(ctx, "victim", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	reach, err := st.Reachable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reach[digest.Digest(layer.String())] {
		t.Errorf("the layer of the removed repository counts as reachable through the other repository's dangling index child")
	}
	p := filepath.Join(st.Root(), "docker", "registry", "v2", "blobs", "sha256", layer.Hex[:2], layer.Hex)
	if _, err := os.Stat(p); err == nil {
		t.Errorf("blob %s of the removed repository survived the sweep, pinned by another repository's index", layer)
	}
	// The index itself is held, and stays.
	h, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !reach[digest.Digest(h.String())] {
		t.Errorf("the sparse index itself is no longer reachable")
	}
}
