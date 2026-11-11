// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestPlatformSubsetCrossesTheAirGap is B-036: a recipe that keeps only
// some platforms of a multi-platform ingredient — the ContainerImage of
// RECIPE-SPEC §7.1 and the FileSet of §7.4 alike — is carried by the
// mirror flow and imported into an isolated zone whose registry is
// Tobby's own embedded one.
//
// The index that crosses is sparse: it is the ORIGINAL index, at its
// pinned digest, listing platforms the medium does not carry (FR-022).
// An isolated zone cannot widen it by fetching the rest, so the zone's
// registry must accept it as it stands — exactly as the store that wrote
// it holds it — and serve the selected platforms, answering 404 for the
// others.
func TestPlatformSubsetCrossesTheAirGap(t *testing.T) {
	site := newKeyPair(t)
	src := newRegistry(t)

	image := seedIndex(t, src, "ghcr.io/example/app", "1.0.0",
		v1.Platform{OS: "linux", Architecture: "amd64"},
		v1.Platform{OS: "linux", Architecture: "arm64"},
		v1.Platform{OS: "linux", Architecture: "arm", Variant: "v7"})
	linuxBin := []byte("ELF tobby amd64")
	binaries := pushBinariesFileSet(t, src, "ghcr.io/tobby-fetch/binaries/tobby", "v0.6.0", map[string][]byte{
		"linux/amd64": linuxBin, "linux/arm64": []byte("ELF tobby arm64"),
		"windows/amd64": []byte("MZ tobby.exe amd64"), "windows/arm64": []byte("MZ tobby.exe arm64"),
	})
	recipe := cookedRecipeYAML(t, "subset", "1.0.0", []spec.Ingredient{
		{Name: "app", Kind: spec.IngredientContainerImage, Ref: "ghcr.io/example/app",
			Version: "1.0.0", Digest: image.digest, Platforms: []string{"linux/amd64", "linux/arm64"}},
		// The zone runs Linux only: the Windows binaries stay behind, as
		// the comment of examples/tobby.yaml invites.
		{Name: "binaries", Kind: spec.IngredientFileSet, Ref: "ghcr.io/tobby-fetch/binaries/tobby",
			Version: "v0.6.0", Digest: binaries, Platforms: []string{"linux/amd64", "linux/arm64"}},
	})
	d := publishRecipe(t, src.st, "ghcr.io/cookbook/subset", "1.0.0", recipe)
	signManifest(t, src.st, "ghcr.io/cookbook/subset", d, site)

	produced := openStore(t)
	retr := retrieverFile(t, servedZone, "ghcr.io/cookbook", []spec.RecipeSelector{{Name: "subset", Version: "1.0.0"}})
	source := New(produced, newRemotes(t, map[string]string{"ghcr.io": src.addr}), trustFor(t, nil, site), retr, "", syncCfg())
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

	arrived, err := store.Open(context.Background(), carryToAnotherHost(t, produced.Root()), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arrived.Close() })
	zone := newDestRegistry(t)
	destination, _ := destinationFor(t, &medium{st: arrived, zone: servedZone}, zone, servedZone, site)
	imp, _ := runImport(t, destination, MediaOptions{})

	if it := itemByName(t, imp, "media/subset@1.0.0"); it.Status != tasks.StatusDone {
		t.Fatalf("the recipe did not cross: %s %+v (%s)", it.Status, it.Error, itemNames(imp))
	}
	ctx := context.Background()
	for _, x := range []struct {
		repo, tag, want string
		absent          []string
	}{
		{"ghcr.io/example/app", "1.0.0", image.digest, []string{image.children["linux/arm/v7"]}},
		{"ghcr.io/tobby-fetch/binaries/tobby", "v0.6.0", binaries, nil},
	} {
		if _, _, got, err := zone.st.RawManifest(ctx, x.repo, x.tag); err != nil || got != x.want {
			t.Errorf("%s:%s in the zone = %s (%v), want the pinned %s — the index must cross as it is, never rewritten", x.repo, x.tag, got, err, x.want)
		}
		for _, child := range x.absent {
			ref, err := name.NewDigest(zone.addr+"/"+x.repo+"@"+child, name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := remote.Head(ref); err == nil {
				t.Errorf("%s@%s is in the zone, but the recipe left that platform out", x.repo, child)
			}
		}
	}
	for label, child := range image.children {
		if label == "linux/arm/v7" {
			continue
		}
		ref, err := name.NewDigest(zone.addr+"/ghcr.io/example/app@"+child, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Head(ref); err != nil {
			t.Errorf("selected platform %s of the image is missing in the zone: %v", label, err)
		}
	}
	if got := fileSetFile(t, zone.st, "ghcr.io/tobby-fetch/binaries/tobby", binaries, "linux/amd64"); !bytes.Equal(got, linuxBin) {
		t.Errorf("the linux/amd64 binary arrived as %q, want %q", got, linuxBin)
	}
}
