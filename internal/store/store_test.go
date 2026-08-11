// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/validate"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// startRegistry serves the embedded registry over a real TCP listener and
// returns its host:port. The client below is a third-party OCI
// implementation, so the wire protocol is exercised for real — no mocks of
// the OCI protocol (quality gate rule).
func startRegistry(t *testing.T) string {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), logging.New(io.Discard, slog.LevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})

	srv := httptest.NewServer(st.APIHandler())
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// TestMultiArchPushPullRelocatedName is the milestone exit criterion: a
// multi-arch image pushed to and pulled from the embedded registry by a
// standard client, under a nested relocated repository name (ADR-0013),
// with the index digest — the pinned identity — preserved (FR-042).
func TestMultiArchPushPullRelocatedName(t *testing.T) {
	addr := startRegistry(t)

	// The relocated name of docker.io/bitnami/wordpress in the local store.
	ref, err := name.ParseReference(addr + "/docker.io/bitnami/wordpress:6.5.2")
	if err != nil {
		t.Fatal(err)
	}

	idx, err := random.Index(1024, 2, 3) // 3 platform manifests
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}

	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatalf("pushing multi-arch index: %v", err)
	}

	pulled, err := remote.Index(ref)
	if err != nil {
		t.Fatalf("pulling multi-arch index: %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest {
		t.Errorf("digest changed through the store: pushed %s, pulled %s", wantDigest, gotDigest)
	}
	// Full structural validation: every manifest and blob reachable and
	// digest-consistent.
	if err := validate.Index(pulled); err != nil {
		t.Errorf("pulled index fails validation: %v", err)
	}

	// Per-platform pull (FR-042): each child manifest is retrievable.
	manifest, err := pulled.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Manifests) != 3 {
		t.Fatalf("index carries %d manifests, want 3", len(manifest.Manifests))
	}
	for _, m := range manifest.Manifests {
		img, err := pulled.Image(m.Digest)
		if err != nil {
			t.Errorf("pulling platform image %s: %v", m.Digest, err)
			continue
		}
		if err := validate.Image(img); err != nil {
			t.Errorf("platform image %s fails validation: %v", m.Digest, err)
		}
	}
}

// TestStandardListingAPIs covers FR-043: catalog and tag listing work
// against the embedded registry, so downstream tooling can enumerate
// content.
func TestStandardListingAPIs(t *testing.T) {
	addr := startRegistry(t)

	img, err := random.Image(512, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"1.0.0", "1.1.0"} {
		ref, err := name.ParseReference(fmt.Sprintf("%s/registry.example.com_5000/team/app:%s", addr, tag))
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
	}

	repo, err := name.NewRepository(addr + "/registry.example.com_5000/team/app")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := remote.List(repo)
	if err != nil {
		t.Fatalf("tags/list: %v", err)
	}
	if len(tags) != 2 {
		t.Errorf("tags = %v, want two", tags)
	}

	reg, err := name.NewRegistry(addr)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := remote.Catalog(context.Background(), reg)
	if err != nil {
		t.Fatalf("_catalog: %v", err)
	}
	if len(repos) != 1 || repos[0] != "registry.example.com_5000/team/app" {
		t.Errorf("catalog = %v, want the single nested relocated repository", repos)
	}
}

// TestPlatformManifestContentIsBitExact pulls a platform manifest by digest
// over plain HTTP and re-hashes the bytes, asserting the store serves
// bit-exact content (§7.1 of the format: bit-exactness is the foundation of
// digest pinning).
func TestPlatformManifestContentIsBitExact(t *testing.T) {
	addr := startRegistry(t)
	ref, err := name.ParseReference(addr + "/ghcr.io/acme/tool:2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}

	url := fmt.Sprintf("http://%s/v2/ghcr.io/acme/tool/manifests/%s", addr, digest)
	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", mediaType(t, raw))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, raw) {
		t.Error("served manifest bytes differ from pushed bytes")
	}
	got, _, err := v1.SHA256(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Errorf("served bytes hash to %s, pushed digest %s", got, digest)
	}
}

func mediaType(t *testing.T, rawManifest []byte) string {
	t.Helper()
	var m struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(rawManifest, &m); err != nil {
		t.Fatal(err)
	}
	return m.MediaType
}
