// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// Tobby updates through its own channel (R-25), end to end, the way the
// vulnerability database does (TestScanDatabaseCrossesTheAirGap):
//
//  1. the connected zone holds the recipe that carries Tobby itself —
//     its image, its chart and its binaries as a FileSet, shaped like
//     examples/tobby.yaml — at two versions: 0.6.0 cooked and signed by
//     the SITE's qualification key, 0.6.1 signed by the PROJECT's key
//     only, as a release arrives before the site has qualified it;
//  2. a mirror synchronization, which trusts both keys at the source,
//     writes both onto a medium by the standard flow;
//  3. the medium is carried and imported by a destination that trusts the
//     site's key alone: 0.6.0 arrives — image, chart, and the binaries,
//     byte for byte — while 0.6.1 is refused
//     whole and nothing of it reaches the zone (invariant 1: Tobby signs
//     nothing, and a destination's trust roots ignore the project key).
//
// Nothing in this journey looks for a newer version on its own (NFR-019):
// the update is a recipe a person cooked, carried, and deploys.
func TestSelfUpdateCrossesTheAirGap(t *testing.T) {
	site, project := newKeyPair(t), newKeyPair(t)
	src := newRegistry(t)

	type release struct {
		version          string
		image, chart     string
		binaries         string
		linuxBin, winBin []byte
	}
	publishRelease := func(version string) *release {
		r := &release{version: version}
		r.linuxBin = []byte("ELF tobby " + version)
		r.winBin = []byte("MZ tobby.exe " + version)
		r.image = seedIndex(t, src, "ghcr.io/tobby-fetch/tobby-fetch", "v"+version,
			v1.Platform{OS: "linux", Architecture: "amd64"}, v1.Platform{OS: "linux", Architecture: "arm64"}).digest
		// The chart as release.yml publishes it: by `helm push`, whose
		// manifest omits the top-level mediaType (B-056).
		r.chart = seedHelmPushedChart(t, src, "ghcr.io/tobby-fetch/charts/tobby", "tobby", version)
		r.binaries = pushBinariesFileSet(t, src, "ghcr.io/tobby-fetch/binaries/tobby", "v"+version, map[string][]byte{
			"linux/amd64": r.linuxBin, "linux/arm64": []byte("ELF arm64 " + version),
			"windows/amd64": r.winBin, "windows/arm64": []byte("MZ arm64 " + version),
		})
		return r
	}
	recipeFor := func(r *release) []byte {
		return cookedRecipeYAML(t, "tobby", r.version, []spec.Ingredient{
			{Name: "tobby-image", Kind: spec.IngredientContainerImage, Ref: "ghcr.io/tobby-fetch/tobby-fetch",
				Version: "v" + r.version, Digest: r.image, Platforms: []string{"linux/amd64", "linux/arm64"}},
			{Name: "tobby-chart", Kind: spec.IngredientHelmChart, Ref: "ghcr.io/tobby-fetch/charts/tobby",
				Version: r.version, Digest: r.chart},
			// The four platforms, as examples/tobby.yaml ships them. A subset
			// crosses as a sparse index (FR-022), which Tobby's own registry
			// accepts: TestPlatformSubsetCrossesTheAirGap (B-036).
			{Name: "tobby-binaries", Kind: spec.IngredientFileSet, Ref: "ghcr.io/tobby-fetch/binaries/tobby",
				Version: "v" + r.version, Digest: r.binaries,
				Platforms: []string{"linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}},
		})
	}
	qualified, unqualified := publishRelease("0.6.0"), publishRelease("0.6.1")
	for _, x := range []struct {
		r  *release
		by string
	}{{qualified, "site"}, {unqualified, "project"}} {
		d := publishRecipe(t, src.st, "ghcr.io/cookbook/tobby", x.r.version, recipeFor(x.r))
		if x.by == "site" {
			signManifest(t, src.st, "ghcr.io/cookbook/tobby", d, site)
		} else {
			signManifest(t, src.st, "ghcr.io/cookbook/tobby", d, project)
		}
	}

	// 2. The standard mirror flow. The source admits both: it trusts the
	// project key too, the way a connected zone that receives releases does.
	produced := openStore(t)
	retr := retrieverFile(t, servedZone, "ghcr.io/cookbook", []spec.RecipeSelector{
		{Name: "tobby", Version: "0.6.0"}, {Name: "tobby", Version: "0.6.1"},
	})
	source := New(produced, newRemotes(t, map[string]string{"ghcr.io": src.addr}), trustFor(t, nil, site, project), retr, "", syncCfg())
	source.SetMediaManifest(func(ctx context.Context, z, runID string, at time.Time) error {
		_, err := media.Write(ctx, produced, media.WriteOptions{Zone: z, RunID: runID, ResolvedAt: at})
		return err
	})
	task, err := runSync(t, source)
	if err != nil {
		t.Fatalf("writing the medium: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("writing the medium: %s", itemNames(task))
	}
	if err := produced.Close(); err != nil {
		t.Fatal(err)
	}

	// 3. The other side, trusting the site's key alone.
	arrived, err := store.Open(context.Background(), carryToAnotherHost(t, produced.Root()), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arrived.Close() })
	zone := newDestRegistry(t)
	destination, _ := destinationFor(t, &medium{st: arrived, zone: servedZone}, zone, servedZone, site)
	imp, _ := runImport(t, destination, MediaOptions{})

	if it := itemByName(t, imp, "media/tobby@0.6.0"); it.Status != tasks.StatusDone {
		t.Fatalf("the site-signed release did not cross: %s %+v", it.Status, it.Error)
	}
	if it := itemByName(t, imp, "media/tobby@0.6.1"); it.Status != tasks.StatusFailed {
		t.Errorf("a release signed by the project key alone crossed: %s — the destination must trust its own roots only", it.Status)
	}
	ctx := context.Background()
	for repo, want := range map[string]string{
		"ghcr.io/tobby-fetch/tobby-fetch":    qualified.image,
		"ghcr.io/tobby-fetch/charts/tobby":   qualified.chart,
		"ghcr.io/tobby-fetch/binaries/tobby": qualified.binaries,
	} {
		tag := "v0.6.0"
		if repo == "ghcr.io/tobby-fetch/charts/tobby" {
			tag = "0.6.0"
		}
		if _, _, got, err := zone.st.RawManifest(ctx, repo, tag); err != nil || got != want {
			t.Errorf("%s:%s in the zone = %s (%v), want %s", repo, tag, got, err, want)
		}
	}
	// The binaries arrive byte for byte.
	for plat, want := range map[string][]byte{"linux/amd64": qualified.linuxBin, "windows/amd64": qualified.winBin} {
		got := fileSetFile(t, zone.st, "ghcr.io/tobby-fetch/binaries/tobby", qualified.binaries, plat)
		if !bytes.Equal(got, want) {
			t.Errorf("the %s binary arrived as %q, want %q", plat, got, want)
		}
	}
	// Nothing of the unqualified release reached the zone.
	if tags := destTags(t, zone, cookbookRepoOf("tobby")); containsString(tags, "0.6.1") {
		t.Error("the zone cookbook advertises the release the site never qualified")
	}
	for _, repo := range []string{"ghcr.io/tobby-fetch/tobby-fetch", "ghcr.io/tobby-fetch/binaries/tobby"} {
		if _, _, _, err := zone.st.RawManifest(ctx, repo, "v0.6.1"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s:v0.6.1 reached the zone (%v)", repo, err)
		}
	}
}

// pushBinariesFileSet publishes the release binaries the way
// tools/release-fileset.sh does: one index, one single-layer child per
// platform holding exactly one file at its root.
func pushBinariesFileSet(t *testing.T, r *registry, repoPath, tag string, files map[string][]byte) string {
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	for plat, data := range files {
		file := "tobby"
		goos, arch, _ := strings.Cut(plat, "/")
		if goos == "windows" {
			file = "tobby.exe"
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{Name: file, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		img, err := mutate.AppendLayers(mutate.MediaType(mutate.ConfigMediaType(empty.Image, types.OCIConfigJSON), types.OCIManifestSchema1),
			static.NewLayer(buf.Bytes(), types.OCILayer))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cfg = cfg.DeepCopy()
		cfg.OS, cfg.Architecture = goos, arch
		if img, err = mutate.ConfigFile(img, cfg); err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: goos, Architecture: arch}}})
	}
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	ref, err := name.ParseReference(r.addr + "/" + repoPath + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	d, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// fileSetFile reads the single file of one platform child of a FileSet
// index held by st.
func fileSetFile(t *testing.T, st *store.Store, repo, index, platform string) []byte {
	t.Helper()
	ctx := context.Background()
	raw, _, _, err := st.RawManifest(ctx, repo, index)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := v1.ParseIndexManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i := range idx.Manifests {
		m := &idx.Manifests[i]
		if m.Platform == nil || m.Platform.OS+"/"+m.Platform.Architecture != platform {
			continue
		}
		childRaw, _, _, err := st.RawManifest(ctx, repo, m.Digest.String())
		if err != nil {
			t.Fatalf("%s child not in the zone: %v", platform, err)
		}
		man, err := v1.ParseManifest(bytes.NewReader(childRaw))
		if err != nil {
			t.Fatal(err)
		}
		rc, err := st.BlobReader(ctx, repo, man.Layers[0].Digest.String())
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(rc)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(zr)
		if _, err := tr.Next(); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		rc.Close() //nolint:errcheck,gosec // test read
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	t.Fatalf("no %s child in the FileSet", platform)
	return nil
}
