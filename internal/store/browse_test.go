// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// seeded is a store populated through the real OCI wire protocol (the
// quality-gate rule: never mock the protocol), with the digests the
// accessors must report back.
type seeded struct {
	st          *store.Store
	addr        string
	indexDigest v1.Hash
	// childDigests indexes the multi-arch children by "os/arch[/variant]".
	childDigests map[string]v1.Hash
	imageDigest  v1.Hash
}

// platformImage builds a random image whose config carries the platform.
func platformImage(t *testing.T, os, arch, variant string) v1.Image {
	t.Helper()
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture, cf.Variant = os, arch, variant
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// seedBrowseStore opens a store and pushes, as a standard client:
//   - a multi-arch OCI index (3 platforms, annotations) tagged 6.4.2 and
//     latest under docker.io/bitnami/wordpress (ADR-0013 relocated name),
//   - a helm chart (CNCF helm config media type),
//   - a recipe artifact (RECIPE-SPEC §11.2 media type as config),
//   - an artifact with an unknown config media type,
//   - a plain single-platform image under a ported relocated host.
func seedBrowseStore(t *testing.T) *seeded {
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
	s := &seeded{st: st, addr: srv.Listener.Addr().String(), childDigests: map[string]v1.Hash{}}

	// Multi-arch index with per-entry platforms and index annotations.
	idx := v1.ImageIndex(empty.Index)
	for _, p := range []struct{ os, arch, variant string }{
		{"linux", "amd64", ""}, {"linux", "arm64", ""}, {"linux", "arm", "v7"},
	} {
		img := platformImage(t, p.os, p.arch, p.variant)
		d, err := img.Digest()
		if err != nil {
			t.Fatal(err)
		}
		key := p.os + "/" + p.arch
		if p.variant != "" {
			key += "/" + p.variant
		}
		s.childDigests[key] = d
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: p.os, Architecture: p.arch, Variant: p.variant},
			},
		})
	}
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	idx = mutate.Annotations(idx, map[string]string{
		"org.opencontainers.image.source": "https://github.com/bitnami/wordpress",
	}).(v1.ImageIndex)
	s.indexDigest, err = idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"6.4.2", "latest"} {
		ref, err := name.ParseReference(s.addr + "/docker.io/bitnami/wordpress:" + tag)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.WriteIndex(ref, idx); err != nil {
			t.Fatalf("pushing index: %v", err)
		}
	}

	push := func(refStr string, img v1.Image) {
		ref, err := name.ParseReference(refStr)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("pushing %s: %v", refStr, err)
		}
	}

	chart, err := random.Image(512, 1)
	if err != nil {
		t.Fatal(err)
	}
	push(s.addr+"/docker.io/bitnamicharts/wordpress:6.4.2",
		mutate.ConfigMediaType(chart, "application/vnd.cncf.helm.config.v1+json"))

	recipe, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	push(s.addr+"/cookbook.example.com/platform/base:1.0.0",
		mutate.ConfigMediaType(recipe, "application/vnd.tobby.recipe.v1+yaml"))

	artifact, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	push(s.addr+"/quay.io/acme/sboms:2024",
		mutate.ConfigMediaType(artifact, "application/vnd.example.custom.config.v1+json"))

	plain := platformImage(t, "linux", "amd64", "")
	s.imageDigest, err = plain.Digest()
	if err != nil {
		t.Fatal(err)
	}
	push(s.addr+"/registry.example.com_5000/team/app:1.0.0", plain)

	return s
}

var seededRepos = []string{
	"cookbook.example.com/platform/base",
	"docker.io/bitnami/wordpress",
	"docker.io/bitnamicharts/wordpress",
	"quay.io/acme/sboms",
	"registry.example.com_5000/team/app",
}

// TestBrowseAccessors drives every accessor over one really-seeded store
// (FR-062, R-06). The sparse subtest mutates the store and runs last.
func TestBrowseAccessors(t *testing.T) {
	s := seedBrowseStore(t)
	ctx := context.Background()

	t.Run("repositories", func(t *testing.T) {
		repos, err := s.st.Repositories(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(repos) != len(seededRepos) {
			t.Fatalf("repositories = %v, want %v", repos, seededRepos)
		}
		for i, want := range seededRepos {
			if repos[i] != want {
				t.Errorf("repositories[%d] = %q, want %q (sorted)", i, repos[i], want)
			}
		}
	})

	t.Run("repo_info_multiarch", func(t *testing.T) {
		info, err := s.st.RepoInfo(ctx, "docker.io/bitnami/wordpress")
		if err != nil {
			t.Fatal(err)
		}
		if info.Kind != store.KindContainerImage {
			t.Errorf("kind = %s, want ContainerImage", info.Kind)
		}
		if len(info.Tags) != 2 || info.Tags[0].Tag != "latest" || info.Tags[1].Tag != "6.4.2" {
			t.Fatalf("tags = %+v, want [latest 6.4.2] (descending)", info.Tags)
		}
		for _, tag := range info.Tags {
			if tag.Digest != s.indexDigest.String() {
				t.Errorf("tag %s digest = %s, want pinned index digest %s", tag.Tag, tag.Digest, s.indexDigest)
			}
			if tag.Platforms != 3 {
				t.Errorf("tag %s platforms = %d, want 3", tag.Tag, tag.Platforms)
			}
			if tag.Present != 3 {
				t.Errorf("tag %s present = %d, want 3 in a fully imported index (B-007)", tag.Tag, tag.Present)
			}
			if tag.Size <= 0 {
				t.Errorf("tag %s size = %d, want > 0", tag.Tag, tag.Size)
			}
		}
		// Two tags on one digest count the manifests once (logical size).
		if info.Size != info.Tags[0].Size {
			t.Errorf("repo size = %d, want the deduplicated %d", info.Size, info.Tags[0].Size)
		}
	})

	t.Run("repo_info_kinds", func(t *testing.T) {
		cases := map[string]store.Kind{
			"docker.io/bitnamicharts/wordpress":  store.KindHelmChart,
			"cookbook.example.com/platform/base": store.KindRecipe,
			"quay.io/acme/sboms":                 store.KindOCIArtifact,
			"registry.example.com_5000/team/app": store.KindContainerImage,
		}
		for repo, want := range cases {
			info, err := s.st.RepoInfo(ctx, repo)
			if err != nil {
				t.Fatalf("%s: %v", repo, err)
			}
			if info.Kind != want {
				t.Errorf("%s kind = %s, want %s", repo, info.Kind, want)
			}
		}
	})

	t.Run("manifest_info_index", func(t *testing.T) {
		for _, ref := range []string{"6.4.2", s.indexDigest.String()} {
			info, err := s.st.ManifestInfo(ctx, "docker.io/bitnami/wordpress", ref)
			if err != nil {
				t.Fatalf("ManifestInfo(%s): %v", ref, err)
			}
			if !info.IsIndex || info.Digest != s.indexDigest.String() {
				t.Errorf("ref %s: IsIndex=%v digest=%s, want pinned %s", ref, info.IsIndex, info.Digest, s.indexDigest)
			}
			if info.MediaType != string(types.OCIImageIndex) {
				t.Errorf("mediaType = %s", info.MediaType)
			}
			if info.Annotations["org.opencontainers.image.source"] == "" {
				t.Error("index annotations missing")
			}
			if len(info.Platforms) != 3 {
				t.Fatalf("platforms = %+v, want 3 entries", info.Platforms)
			}
			for _, p := range info.Platforms {
				if !p.Present {
					t.Errorf("platform %s/%s not Present in a fully imported index", p.OS, p.Arch)
				}
				if p.Size <= 0 {
					t.Errorf("platform %s/%s size = %d", p.OS, p.Arch, p.Size)
				}
				key := p.OS + "/" + p.Arch
				if p.Variant != "" {
					key += "/" + p.Variant
				}
				if want, ok := s.childDigests[key]; !ok || p.Digest != want.String() {
					t.Errorf("platform %s digest = %s, want %s", key, p.Digest, want)
				}
			}
		}
	})

	t.Run("manifest_info_plain_image", func(t *testing.T) {
		info, err := s.st.ManifestInfo(ctx, "registry.example.com_5000/team/app", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		if info.IsIndex {
			t.Error("plain manifest reported as index")
		}
		if info.Kind != store.KindContainerImage {
			t.Errorf("kind = %s", info.Kind)
		}
		if len(info.Platforms) != 1 {
			t.Fatalf("platforms = %+v, want the single synthetic entry", info.Platforms)
		}
		p := info.Platforms[0]
		if p.OS != "linux" || p.Arch != "amd64" || !p.Present {
			t.Errorf("platform = %+v, want linux/amd64 present (read from the config)", p)
		}
		if p.Digest != s.imageDigest.String() {
			t.Errorf("platform digest = %s, want %s", p.Digest, s.imageDigest)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		if _, err := s.st.RepoInfo(ctx, "docker.io/none/nothing"); !errorsIsNotFound(err) {
			t.Errorf("unknown repo: err = %v, want ErrNotFound", err)
		}
		if _, err := s.st.RepoInfo(ctx, "NOT a valid NAME"); !errorsIsNotFound(err) {
			t.Errorf("invalid repo name: err = %v, want ErrNotFound", err)
		}
		if _, err := s.st.ManifestInfo(ctx, "docker.io/bitnami/wordpress", "9.9.9"); !errorsIsNotFound(err) {
			t.Errorf("unknown tag: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("counts", func(t *testing.T) {
		c, err := s.st.Counts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c.Repositories != 5 || c.Tags != 6 {
			t.Errorf("counts = %+v, want 5 repositories, 6 tags", c)
		}
		// PhysicalBytes is the real on-disk blob directory size.
		var want int64
		blobs := filepath.Join(s.st.Root(), "docker", "registry", "v2", "blobs")
		err = filepath.WalkDir(blobs, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				info, err := d.Info()
				if err != nil {
					return err
				}
				want += info.Size()
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if c.PhysicalBytes != want || want == 0 {
			t.Errorf("PhysicalBytes = %d, want on-disk %d (> 0)", c.PhysicalBytes, want)
		}
	})

	t.Run("browse_filters", func(t *testing.T) {
		names := func(p *store.BrowsePage) []string {
			out := make([]string, 0, len(p.Repos))
			for _, r := range p.Repos {
				out = append(out, r.Name)
			}
			return out
		}

		p, err := s.st.Browse(ctx, store.BrowseQuery{Q: "wordpress", Page: 1})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 2 || len(p.Repos) != 2 {
			t.Errorf("q=wordpress → %v", names(p))
		}

		p, err = s.st.Browse(ctx, store.BrowseQuery{Kinds: []store.Kind{store.KindHelmChart}, Page: 1})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 1 || p.Repos[0].Name != "docker.io/bitnamicharts/wordpress" {
			t.Errorf("kind=HelmChart → %v", names(p))
		}

		p, err = s.st.Browse(ctx, store.BrowseQuery{Prefix: "docker.io", Page: 1})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 2 {
			t.Errorf("prefix=docker.io → %v", names(p))
		}
		// The prefix is a path prefix, not a string prefix.
		p, err = s.st.Browse(ctx, store.BrowseQuery{Prefix: "docker.io/bitnami", Page: 1})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 1 || p.Repos[0].Name != "docker.io/bitnami/wordpress" {
			t.Errorf("prefix=docker.io/bitnami → %v (must not match bitnamicharts)", names(p))
		}

		// Out-of-range page: empty window, page math intact (FR-061: the
		// API mirrors the same behavior).
		p, err = s.st.Browse(ctx, store.BrowseQuery{Page: 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Repos) != 0 || p.Total != 5 || p.TotalPages != 1 {
			t.Errorf("page=4 → %d repos, total=%d, totalPages=%d", len(p.Repos), p.Total, p.TotalPages)
		}
	})

	// Sparse index (FR-022): delete one child manifest through the standard
	// registry API — the index digest stays pinned, the platform reads as
	// absent. Mutates the store: keep this subtest last.
	t.Run("sparse_index", func(t *testing.T) {
		child := s.childDigests["linux/arm/v7"]
		delURL := fmt.Sprintf("http://%s/v2/docker.io/bitnami/wordpress/manifests/%s", s.addr, child)
		req, err := http.NewRequest(http.MethodDelete, delURL, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("DELETE child manifest = %d, want 202", resp.StatusCode)
		}

		info, err := s.st.ManifestInfo(ctx, "docker.io/bitnami/wordpress", "6.4.2")
		if err != nil {
			t.Fatal(err)
		}
		if info.Digest != s.indexDigest.String() {
			t.Errorf("index digest changed on sparse store: %s", info.Digest)
		}
		var present, absent int
		for _, p := range info.Platforms {
			switch {
			case p.Digest == child.String():
				if p.Present {
					t.Error("deleted platform still reads Present")
				}
				absent++
			default:
				if !p.Present {
					t.Errorf("platform %s/%s lost its presence", p.OS, p.Arch)
				}
				present++
			}
		}
		if present != 2 || absent != 1 {
			t.Errorf("present/absent = %d/%d, want 2/1", present, absent)
		}

		// The tag table reports both counts — present/total, never the
		// total alone (B-007).
		repo, err := s.st.RepoInfo(ctx, "docker.io/bitnami/wordpress")
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range repo.Tags {
			if tag.Platforms != 3 || tag.Present != 2 {
				t.Errorf("sparse tag %s = %d/%d, want 2/3 present/total", tag.Tag, tag.Present, tag.Platforms)
			}
		}
	})
}

// TestParseBrowseQuery pins the shared UI/API parameter contract (FR-061).
func TestParseBrowseQuery(t *testing.T) {
	v, err := url.ParseQuery("q=word&kind=HelmChart&kind=Recipe&prefix=docker.io&page=3")
	if err != nil {
		t.Fatal(err)
	}
	q := store.ParseBrowseQuery(v)
	if q.Q != "word" || q.Prefix != "docker.io" || q.Page != 3 || len(q.Kinds) != 2 {
		t.Errorf("parsed = %+v", q)
	}
	if !q.HasFilter() {
		t.Error("HasFilter must be true with filters set")
	}
	if got := q.Values().Encode(); got != "kind=HelmChart&kind=Recipe&page=3&prefix=docker.io&q=word" {
		t.Errorf("round-trip = %s", got)
	}

	zero := store.ParseBrowseQuery(url.Values{})
	if zero.Page != 1 || zero.HasFilter() {
		t.Errorf("empty query = %+v", zero)
	}
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
