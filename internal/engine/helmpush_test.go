// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestSyncChartPushedByHelm (B-056): a chart as `helm push` publishes it —
// an OCI manifest that omits the optional top-level "mediaType" field —
// synchronizes, bit-exact, under its pinned digest. Before the fix the
// copy took the media type from that absent field and the store refused
// the manifest with TBY-SRV-001 ("unsupported manifest media type"), so
// no chart published by Helm itself could ever be mirrored.
func TestSyncChartPushedByHelm(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	chartDig := seedHelmPushedChart(t, src, "charts/tobby", "tobby", "0.6.0")

	recipeYAML := cookedRecipeYAML(t, "tobby", "0.6.0", []spec.Ingredient{{
		Name: "chart", Kind: spec.IngredientHelmChart,
		Ref: src.addr + "/charts/tobby", Version: "0.6.0", Digest: chartDig,
	}})
	manDig := publishRecipe(t, src.st, "cookbook/tobby", "0.6.0", recipeYAML)
	kp := newKeyPair(t)
	signManifest(t, src.st, "cookbook/tobby", manDig, kp)
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{{Name: "tobby", Version: "0.6.0"}})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())

	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if it := itemByName(t, task, "tobby@0.6.0/chart"); it.Status != tasks.StatusDone {
		t.Fatalf("the helm-pushed chart = %s (error: %+v), want done", it.Status, it.Error)
	}

	ctx := context.Background()
	chartRepo := strings.ReplaceAll(src.addr, ":", "_") + "/charts/tobby"
	if d, ok := dst.ResolveTag(ctx, chartRepo, "0.6.0"); !ok || d != chartDig {
		t.Fatalf("chart tag resolves %q (ok=%v), want the pinned digest %s", d, ok, chartDig)
	}
	srcRaw, _, _, err := src.st.RawManifest(ctx, "charts/tobby", "0.6.0")
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, mediaType, _, err := dst.RawManifest(ctx, chartRepo, "0.6.0")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRaw, srcRaw) {
		t.Error("the chart manifest was not stored bit-exactly")
	}
	if mediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("the stored chart is served as %q, want the OCI image manifest type", mediaType)
	}
}
