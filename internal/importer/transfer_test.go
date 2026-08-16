// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// destStore opens a real embedded store as the transfer destination
// (ADR-0005: the writes go through the storage backend directly).
func destStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// runTask drives the runner exactly as the queue would.
func runTask(t *testing.T, dst Destination, reference string, items []tasks.Item, opts ...Option) *tasks.Task {
	t.Helper()
	return runTaskVendor(t, dst, reference, items, false, opts...)
}

// runTaskVendor is runTask with the FR-025 vendoring opt-in.
func runTaskVendor(t *testing.T, dst Destination, reference string, items []tasks.Item, vendor bool, opts ...Option) *tasks.Task {
	t.Helper()
	task := &tasks.Task{
		ID: "tsk_test", RunID: "run_test", Type: tasks.TypeUnitImport,
		Reference: reference, Items: items, Status: tasks.StatusRunning,
		Created: time.Now(), VendorDependencies: vendor,
	}
	err := NewRunner(dst, opts...)(context.Background(), task, slog.New(slog.DiscardHandler), func() {})
	if err != nil {
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			t.Fatalf("runner returned a non-taxonomy error: %v", err)
		}
		task.Error = tasks.FromTaxonomy(te)
		task.Status = tasks.StatusFailed
	}
	return task
}

// itemsFromReport builds the task items the UI would submit from an
// inspection, optionally keeping only some platforms.
func itemsFromReport(rep *Report, keep func(Platform) bool) []tasks.Item {
	var items []tasks.Item
	for i := range rep.Platforms {
		p := rep.Platforms[i]
		if keep != nil && !keep(p) {
			continue
		}
		items = append(items, tasks.Item{Name: p.Name(), Digest: p.Digest})
	}
	return items
}

func TestTransferMultiArchEndToEnd(t *testing.T) {
	host := upstream(t)
	ref := host + "/bitnami/wordpress:6.4.2"
	pushIndex(t, ref, "linux/amd64", "linux/arm64")
	dst := destStore(t)
	ctx := context.Background()

	rep, err := Inspect(ctx, ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	task := runTask(t, dst, ref, itemsFromReport(rep, nil))

	if task.Error != nil {
		t.Fatalf("task error: %+v", task.Error)
	}
	agg := task.Aggregate()
	if agg.Done != 2 || agg.Failed != 0 {
		t.Fatalf("aggregates = %+v (items %+v)", agg, task.Items)
	}
	for _, it := range task.Items {
		if it.SizeBytes <= 0 {
			t.Errorf("item %s transferred 0 bytes", it.Name)
		}
	}

	// The index landed bit-exactly: same pinned digest, tag resolves to it
	// (FR-022), both platforms present.
	got, err := dst.ManifestInfo(ctx, rep.Repository, rep.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != rep.IndexDigest {
		t.Errorf("index digest %s ≠ pinned %s (bit-exact broken)", got.Digest, rep.IndexDigest)
	}
	for _, p := range got.Platforms {
		if !p.Present {
			t.Errorf("platform %s/%s absent after full import", p.OS, p.Arch)
		}
	}

	// Idempotence (FR-026): a re-inspection sees everything up to date and
	// a re-run transfers zero bytes.
	rep2, err := Inspect(ctx, ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rep2.Platforms {
		if p.Status != StatusUpToDate {
			t.Errorf("after import, %s status = %s, want up-to-date", p.Name(), p.Status)
		}
	}
	task2 := runTask(t, dst, ref, itemsFromReport(rep2, nil))
	for _, it := range task2.Items {
		if it.SizeBytes != 0 {
			t.Errorf("second run transferred %d bytes on %s, want 0", it.SizeBytes, it.Name)
		}
	}
}

// TestTransferPartialSelection is the FR-022 sparse-index acceptance: only
// one platform selected, the original index digest preserved, the missing
// platform browsable as absent.
func TestTransferPartialSelection(t *testing.T) {
	host := upstream(t)
	ref := host + "/library/redis:7.2"
	pushIndex(t, ref, "linux/amd64", "linux/arm64", "linux/arm/v7")
	dst := destStore(t)
	ctx := context.Background()

	rep, err := Inspect(ctx, ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	task := runTask(t, dst, ref, itemsFromReport(rep, func(p Platform) bool {
		return p.Arch == "amd64"
	}))
	if agg := task.Aggregate(); agg.Done != 1 {
		t.Fatalf("aggregates = %+v", agg)
	}

	got, err := dst.ManifestInfo(ctx, rep.Repository, rep.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != rep.IndexDigest {
		t.Errorf("sparse index digest %s ≠ pinned %s", got.Digest, rep.IndexDigest)
	}
	present := map[string]bool{}
	for _, p := range got.Platforms {
		present[p.OS+"/"+p.Arch+val(p.Variant)] = p.Present
	}
	if !present["linux/amd64"] || present["linux/arm64"] || present["linux/arm/v7"] {
		t.Errorf("presence map = %v, want only amd64", present)
	}
}

func val(v string) string {
	if v == "" {
		return ""
	}
	return "/" + v
}

// TestTransferUntaggedReference is B-009 end to end: an untagged reference
// on a repository without "latest" resolves to the highest stable semver
// tag at inspection, and the transfer imports and tags exactly that.
func TestTransferUntaggedReference(t *testing.T) {
	host := upstream(t)
	repo := host + "/bitnamicharts/wordpress"
	for _, tag := range []string{"19.1.0", "19.2.6"} {
		ref, err := name.ParseReference(repo + ":" + tag)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, chartImage(t, true)); err != nil {
			t.Fatal(err)
		}
	}
	dst := destStore(t)
	ctx := context.Background()

	rep, err := Inspect(ctx, repo, dst)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tag != "19.2.6" {
		t.Fatalf("resolved tag = %q, want 19.2.6", rep.Tag)
	}
	task := runTask(t, dst, repo, itemsFromReport(rep, nil))
	if agg := task.Aggregate(); agg.Done != 1 || agg.Failed != 0 {
		t.Fatalf("aggregates = %+v (error %+v)", agg, task.Error)
	}
	got, err := dst.ManifestInfo(ctx, rep.Repository, "19.2.6")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != rep.IndexDigest {
		t.Errorf("stored digest %s ≠ pinned %s", got.Digest, rep.IndexDigest)
	}
}

// chartImage packages a minimal Helm chart tgz as an OCI artifact, with or
// without its declared dependency embedded.
func chartImage(t *testing.T, withDep bool) v1.Image {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(path, content string) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("wordpress/Chart.yaml", "name: wordpress\nversion: 19.2.6\ndependencies:\n  - name: mariadb\n    version: 16.3.2\n    repository: oci://registry-1.docker.io/bitnamicharts\n")
	if withDep {
		add("wordpress/charts/mariadb-16.3.2.tgz", "fake-subchart-bytes")
	}
	add("wordpress/values.yaml", "replicaCount: 1\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	layer := static.NewLayer(buf.Bytes(), types.MediaType("application/vnd.cncf.helm.chart.content.v1.tar+gzip"))
	img, err := mutate.AppendLayers(emptyImage(), layer)
	if err != nil {
		t.Fatal(err)
	}
	return mutate.ConfigMediaType(img, helmConfigMediaType)
}

// empty_Image returns an empty base image for chart packaging.
func emptyImage() v1.Image {
	img, _ := random.Image(0, 0)
	return img
}

// TestChartDependencyVerification locks FR-024: an embedded dependency
// passes with a visible report; a missing one fails naming the dependency,
// before anything lands.
func TestChartDependencyVerification(t *testing.T) {
	host := upstream(t)
	dst := destStore(t)
	ctx := context.Background()

	good := host + "/bitnamicharts/wordpress:19.2.6"
	gref, _ := name.ParseReference(good)
	if err := remote.Write(gref, chartImage(t, true)); err != nil {
		t.Fatal(err)
	}
	rep, err := Inspect(ctx, good, dst)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Kind != KindChart {
		t.Fatalf("kind = %s", rep.Kind)
	}
	task := runTask(t, dst, good, itemsFromReport(rep, nil))
	if agg := task.Aggregate(); agg.Done != 1 {
		t.Fatalf("chart import failed: %+v / %+v", task.Error, task.Items)
	}
	if len(task.ChartDependencies) != 1 || task.ChartDependencies[0].Name != "mariadb" ||
		!task.ChartDependencies[0].Embedded {
		t.Errorf("dependency report = %+v", task.ChartDependencies)
	}

	// Missing dependency: TBY-CHT-001 naming it, nothing stored.
	bad := host + "/bitnamicharts/broken:1.0.0"
	bref, _ := name.ParseReference(bad)
	if err := remote.Write(bref, chartImage(t, false)); err != nil {
		t.Fatal(err)
	}
	brep, err := Inspect(ctx, bad, dst)
	if err != nil {
		t.Fatal(err)
	}
	btask := runTask(t, dst, bad, itemsFromReport(brep, nil))
	if agg := btask.Aggregate(); agg.Failed != 1 {
		t.Fatalf("broken chart aggregates = %+v", agg)
	}
	itErr := btask.Items[0].Error
	if itErr == nil || itErr.Code != taxonomy.CodeChartDependency {
		t.Fatalf("item error = %+v, want TBY-CHT-001", itErr)
	}
	msg := taxonomy.Localize("fr", itErr.Taxonomy("run_test"))
	if !strings.Contains(msg.Cause, "mariadb") {
		t.Errorf("message does not name the dependency: %q", msg.Cause)
	}
	if _, err := dst.RepoInfo(ctx, brep.Repository); err == nil {
		t.Error("broken chart landed in the store despite the refusal")
	}
	if len(btask.ChartDependencies) != 1 || btask.ChartDependencies[0].Embedded {
		t.Errorf("broken report = %+v", btask.ChartDependencies)
	}
}
