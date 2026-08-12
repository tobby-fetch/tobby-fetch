// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// pushChart publishes a chart tgz as its standard OCI artifact.
func pushChart(t *testing.T, ref string, tgz []byte) {
	t.Helper()
	layer := static.NewLayer(tgz, types.MediaType(helmChartContentMediaType))
	img, err := mutate.AppendLayers(emptyImage(), layer)
	if err != nil {
		t.Fatal(err)
	}
	img = mutate.ConfigMediaType(img, helmConfigMediaType)
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
}

// storedManifest decodes one stored chart manifest for assertions.
type storedManifest struct {
	Annotations map[string]string `json:"annotations"`
	Layers      []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
}

// readStoredChart returns the manifest and the archive bytes of repo:tag.
func readStoredChart(t *testing.T, dst *store.Store, repo, tag string) (digest string, manifest *storedManifest, archive []byte) {
	t.Helper()
	ctx := context.Background()
	payload, _, dgst, err := dst.RawManifest(ctx, repo, tag)
	if err != nil {
		t.Fatal(err)
	}
	man := &storedManifest{}
	if err := json.Unmarshal(payload, man); err != nil {
		t.Fatal(err)
	}
	if len(man.Layers) != 1 {
		t.Fatalf("stored manifest layers = %+v", man.Layers)
	}
	rc, err := dst.BlobReader(ctx, repo, man.Layers[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close() //nolint:errcheck // test read
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return dgst, man, data
}

// TestVendorOCIChart is the FR-025 acceptance on an OCI chart: without the
// opt-in the TBY-CHT-001 refusal stands; with it the missing dependency is
// fetched from its https repository — the Chart.lock pin winning over the
// Chart.yaml range — embedded under charts/, the report flips to embedded,
// the manifest carries the mandatory trace annotations, and vendoring the
// same upstream twice yields the same digest (determinism).
func TestVendorOCIChart(t *testing.T) {
	ctx := context.Background()
	depPinned := simpleChartTgz(t, "mariadb", "16.3.2")
	depNewer := simpleChartTgz(t, "mariadb", "16.4.0")
	files := map[string][]byte{
		"/deps/mariadb-16.3.2.tgz": depPinned,
		"/deps/mariadb-16.4.0.tgz": depNewer,
	}
	files["/deps/index.yaml"] = chartIndexYAML(map[string][]helmIndexEntry{
		"mariadb": {
			{Version: "16.3.2", URLs: []string{"mariadb-16.3.2.tgz"}, Digest: "sha256:" + sha256Hex(depPinned)},
			{Version: "16.4.0", URLs: []string{"mariadb-16.4.0.tgz"}},
		},
	})
	du := serveChartRepo(t, files)
	depRepo := du.String() + "/deps"

	parent := buildTgz(t, []tfile{
		{"wordpress/Chart.yaml", []byte(fmt.Sprintf(
			"name: wordpress\nversion: 19.2.6\ndependencies:\n  - name: mariadb\n    version: 16.x\n    repository: %s\n", depRepo))},
		{"wordpress/Chart.lock", []byte(fmt.Sprintf(
			"dependencies:\n  - name: mariadb\n    version: 16.3.2\n    repository: %s\n", depRepo))},
		{"wordpress/values.yaml", []byte("replicaCount: 1\n")},
	})
	host := upstream(t)
	ref := host + "/charts/wordpress:19.2.6"
	pushChart(t, ref, parent)
	dst := destStore(t)
	opts := WithInsecureHosts([]string{du.Host})

	rep, err := Inspect(ctx, ref, dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	upstreamDigest := rep.IndexDigest

	// Without the opt-in: the FR-024 refusal stands, nothing lands.
	task := runTask(t, dst, ref, itemsFromReport(rep, nil), opts)
	if agg := task.Aggregate(); agg.Failed != 1 {
		t.Fatalf("default aggregates = %+v", agg)
	}
	if e := task.Items[0].Error; e == nil || e.Code != taxonomy.CodeChartDependency {
		t.Fatalf("default item error = %+v, want TBY-CHT-001", e)
	}
	if _, err := dst.RepoInfo(ctx, rep.Repository); err == nil {
		t.Fatal("refused chart landed in the store")
	}

	// With it: vendored, traced, installable.
	vtask := runTaskVendor(t, dst, ref, itemsFromReport(rep, nil), true, opts)
	if agg := vtask.Aggregate(); agg.Done != 1 {
		t.Fatalf("vendored aggregates = %+v (error %+v, items %+v)", agg, vtask.Error, vtask.Items)
	}
	if len(vtask.ChartDependencies) != 1 || !vtask.ChartDependencies[0].Embedded ||
		vtask.ChartDependencies[0].Name != "mariadb" {
		t.Errorf("post-vendoring report = %+v", vtask.ChartDependencies)
	}
	vendoredDigest := vtask.Items[0].Digest
	if vendoredDigest == upstreamDigest || vendoredDigest == "" {
		t.Fatalf("vendored digest = %q (upstream %q): the substitution must be visible", vendoredDigest, upstreamDigest)
	}

	dgst, man, archive := readStoredChart(t, dst, rep.Repository, "19.2.6")
	if dgst != vendoredDigest {
		t.Errorf("stored digest %s ≠ item digest %s", dgst, vendoredDigest)
	}
	if man.Annotations[annotationVendoredUpstream] != upstreamDigest {
		t.Errorf("upstream annotation = %q, want %q", man.Annotations[annotationVendoredUpstream], upstreamDigest)
	}
	// The Chart.lock pin won over the 16.4.0 the range would resolve.
	if man.Annotations[annotationVendoredDeps] != "mariadb@16.3.2" {
		t.Errorf("dependencies annotation = %q, want mariadb@16.3.2", man.Annotations[annotationVendoredDeps])
	}
	arch, err := scanChartArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if !arch.embedded["mariadb"] {
		t.Error("vendored archive misses charts/mariadb-16.3.2.tgz")
	}
	if _, terr := arch.dependencyRows(); terr != nil {
		t.Errorf("vendored archive fails re-verification: %v", terr)
	}

	// Determinism: a second vendoring of the same upstream, in a fresh
	// store, lands the exact same digest.
	dst2 := destStore(t)
	vtask2 := runTaskVendor(t, dst2, ref, itemsFromReport(rep, nil), true, opts)
	if agg := vtask2.Aggregate(); agg.Done != 1 {
		t.Fatalf("second vendoring aggregates = %+v", agg)
	}
	if vtask2.Items[0].Digest != vendoredDigest {
		t.Errorf("second vendoring digest %s ≠ first %s (non-deterministic repack)", vtask2.Items[0].Digest, vendoredDigest)
	}
}

// TestVendorOCIDependencySource covers the oci:// dependency source: the
// version range resolves against the repository's tag list (RECIPE-SPEC
// §9.2) and the dependency chart's content layer is embedded.
func TestVendorOCIDependencySource(t *testing.T) {
	ctx := context.Background()
	host := upstream(t)
	pushChart(t, host+"/bitnamicharts/redis:7.3.0", simpleChartTgz(t, "redis", "7.3.0"))
	pushChart(t, host+"/bitnamicharts/redis:7.4.0", simpleChartTgz(t, "redis", "7.4.0"))

	parent := buildTgz(t, []tfile{
		{"app/Chart.yaml", []byte(fmt.Sprintf(
			"name: app\nversion: 1.0.0\ndependencies:\n  - name: redis\n    version: 7.x\n    repository: oci://%s/bitnamicharts\n", host))},
		{"app/values.yaml", []byte("a: 1\n")},
	})
	ref := host + "/charts/app:1.0.0"
	pushChart(t, ref, parent)
	dst := destStore(t)

	rep, err := Inspect(ctx, ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	vtask := runTaskVendor(t, dst, ref, itemsFromReport(rep, nil), true)
	if agg := vtask.Aggregate(); agg.Done != 1 {
		t.Fatalf("aggregates = %+v (error %+v, items %+v)", agg, vtask.Error, vtask.Items)
	}
	_, man, archive := readStoredChart(t, dst, rep.Repository, "1.0.0")
	if man.Annotations[annotationVendoredDeps] != "redis@7.4.0" {
		t.Errorf("dependencies annotation = %q, want redis@7.4.0", man.Annotations[annotationVendoredDeps])
	}
	arch, err := scanChartArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if !arch.embedded["redis"] {
		t.Error("vendored archive misses charts/redis-7.4.0.tgz")
	}
}

// TestChartRepoVendor is FR-024 + FR-025 combined: an https chart whose
// dependency is missing refuses by default, vendors on opt-in with the
// upstream annotation anchored on the unvendored conversion — the digest
// the inspection reported — and revendors deterministically.
func TestChartRepoVendor(t *testing.T) {
	ctx := context.Background()
	dep := simpleChartTgz(t, "postgresql", "12.5.6")
	depFiles := map[string][]byte{"/deps/postgresql-12.5.6.tgz": dep}
	depFiles["/deps/index.yaml"] = chartIndexYAML(map[string][]helmIndexEntry{
		"postgresql": {{Version: "12.5.6", URLs: []string{"postgresql-12.5.6.tgz"}, Digest: "sha256:" + sha256Hex(dep)}},
	})
	du := serveChartRepo(t, depFiles)

	parent := buildTgz(t, []tfile{
		{"forge/Chart.yaml", []byte(fmt.Sprintf(
			"name: forge\nversion: 2.1.0\ndependencies:\n  - name: postgresql\n    version: 12.5.6\n    repository: %s/deps\n", du.String()))},
		{"forge/values.yaml", []byte("b: 2\n")},
	})
	parentFiles := map[string][]byte{"/charts/forge-2.1.0.tgz": parent}
	parentFiles["/charts/index.yaml"] = chartIndexYAML(map[string][]helmIndexEntry{
		"forge": {{Version: "2.1.0", URLs: []string{"forge-2.1.0.tgz"}, Digest: "sha256:" + sha256Hex(parent)}},
	})
	pu := serveChartRepo(t, parentFiles)
	opts := WithInsecureHosts([]string{du.Host, pu.Host})
	ref := pu.String() + "/charts/forge"
	dst := destStore(t)

	rep, err := Inspect(ctx, ref, dst, opts)
	if err != nil {
		t.Fatal(err)
	}

	// Default: refused (TBY-CHT-001), with the dependency named missing in
	// the report.
	task := runTask(t, dst, ref, itemsFromReport(rep, nil), opts)
	if agg := task.Aggregate(); agg.Failed != 1 {
		t.Fatalf("default aggregates = %+v", agg)
	}
	if e := task.Items[0].Error; e == nil || e.Code != taxonomy.CodeChartDependency {
		t.Fatalf("default item error = %+v", e)
	}
	if len(task.ChartDependencies) != 1 || task.ChartDependencies[0].Embedded {
		t.Errorf("default report = %+v", task.ChartDependencies)
	}

	// Opt-in: vendored, the upstream anchor is the unvendored conversion —
	// exactly what the inspection pinned.
	vtask := runTaskVendor(t, dst, ref, itemsFromReport(rep, nil), true, opts)
	if agg := vtask.Aggregate(); agg.Done != 1 {
		t.Fatalf("vendored aggregates = %+v (error %+v, items %+v)", agg, vtask.Error, vtask.Items)
	}
	dgst, man, archive := readStoredChart(t, dst, rep.Repository, "2.1.0")
	if dgst == rep.IndexDigest {
		t.Error("vendored digest equals the upstream conversion: no substitution happened")
	}
	if man.Annotations[annotationVendoredUpstream] != rep.IndexDigest {
		t.Errorf("upstream annotation = %q, want the inspected digest %q",
			man.Annotations[annotationVendoredUpstream], rep.IndexDigest)
	}
	if man.Annotations[annotationVendoredDeps] != "postgresql@12.5.6" {
		t.Errorf("dependencies annotation = %q", man.Annotations[annotationVendoredDeps])
	}
	arch, err := scanChartArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if !arch.embedded["postgresql"] {
		t.Error("vendored archive misses charts/postgresql-12.5.6.tgz")
	}
	if len(vtask.ChartDependencies) != 1 || !vtask.ChartDependencies[0].Embedded {
		t.Errorf("vendored report = %+v", vtask.ChartDependencies)
	}

	// Determinism across stores.
	dst2 := destStore(t)
	vtask2 := runTaskVendor(t, dst2, ref, itemsFromReport(rep, nil), true, opts)
	if vtask2.Items[0].Digest != dgst {
		t.Errorf("revendored digest %s ≠ %s", vtask2.Items[0].Digest, dgst)
	}
}

// TestVendorUnsupportedRepository: a missing dependency whose repository
// cannot be vendored (file://, or none) keeps failing as TBY-CHT-001 even
// with the opt-in — never a silent partial vendoring.
func TestVendorUnsupportedRepository(t *testing.T) {
	ctx := context.Background()
	host := upstream(t)
	parent := buildTgz(t, []tfile{
		{"app/Chart.yaml", []byte(
			"name: app\nversion: 1.0.0\ndependencies:\n  - name: sub\n    version: 1.0.0\n    repository: file://charts/sub\n")},
	})
	ref := host + "/charts/app:1.0.0"
	pushChart(t, ref, parent)
	dst := destStore(t)
	rep, err := Inspect(ctx, ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	vtask := runTaskVendor(t, dst, ref, itemsFromReport(rep, nil), true)
	if e := vtask.Items[0].Error; e == nil || e.Code != taxonomy.CodeChartDependency {
		t.Fatalf("item error = %+v, want TBY-CHT-001", e)
	}
	if _, err := dst.RepoInfo(ctx, rep.Repository); err == nil {
		t.Error("unvendorable chart landed in the store")
	}
}
