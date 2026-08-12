// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// upstream starts a real in-memory OCI registry (go-containerregistry's
// reference implementation) — the source registry of the tests. The OCI
// protocol is never mocked (DEVELOPMENT-PLAN test pyramid).
func upstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// pushIndex seeds a random multi-arch index and returns its digest.
func pushIndex(t *testing.T, ref string, platforms ...string) v1.ImageIndex {
	t.Helper()
	var idx v1.ImageIndex = empty.Index
	for _, p := range platforms {
		parts := strings.SplitN(p, "/", 3)
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		pf := v1.Platform{OS: parts[0], Architecture: parts[1]}
		if len(parts) == 3 {
			pf.Variant = parts[2]
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &pf},
		})
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

// memLocal fakes the store presence checks.
type memLocal struct {
	manifests map[string]bool // repo@digest
	tags      map[string]string
}

func (m *memLocal) HasManifest(_ context.Context, repo, digest string) bool {
	return m.manifests[repo+"@"+digest]
}

func (m *memLocal) ResolveTag(_ context.Context, repo, tag string) (string, bool) {
	d, ok := m.tags[repo+":"+tag]
	return d, ok
}

func TestInspectMultiArchIndex(t *testing.T) {
	host := upstream(t)
	ref := host + "/bitnami/wordpress:6.4.2"
	pushIndex(t, ref, "linux/amd64", "linux/arm64", "linux/arm/v7")

	rep, err := Inspect(context.Background(), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.MultiArch || len(rep.Platforms) != 3 {
		t.Fatalf("platforms = %+v", rep.Platforms)
	}
	// Relocation: the repository keeps the canonicalized source host prefix
	// (ADR-0013). The test registry host is 127.0.0.1:port → port folds to
	// _port per the convention; just check the suffix.
	if !strings.HasSuffix(rep.Repository, "/bitnami/wordpress") {
		t.Errorf("relocated repository = %q", rep.Repository)
	}
	if rep.Tag != "6.4.2" || rep.IndexDigest == "" || rep.Kind != KindImage {
		t.Errorf("report header = %+v", rep)
	}
	for _, p := range rep.Platforms {
		if p.Status != StatusNew || p.SizeBytes <= 0 || p.Digest == "" {
			t.Errorf("platform %s: %+v", p.Name(), p)
		}
	}
	names := []string{rep.Platforms[0].Name(), rep.Platforms[1].Name(), rep.Platforms[2].Name()}
	if strings.Join(names, ",") != "linux/amd64,linux/arm64,linux/arm/v7" {
		t.Errorf("platform names = %v", names)
	}
}

func TestInspectPerDigestStatus(t *testing.T) {
	host := upstream(t)
	ref := host + "/library/redis:7.2"
	idx := pushIndex(t, ref, "linux/amd64", "linux/arm64")
	man, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}

	// The amd64 digest is already local → up-to-date; arm64 is absent but
	// the tag exists locally → outdated (FR-026).
	repo := "" // resolved after a first inspect
	rep, err := Inspect(context.Background(), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo = rep.Repository
	local := &memLocal{
		manifests: map[string]bool{repo + "@" + man.Manifests[0].Digest.String(): true},
		tags:      map[string]string{repo + ":7.2": "sha256:something-older"},
	}
	rep, err = Inspect(context.Background(), ref, local)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Platforms[0].Status != StatusUpToDate {
		t.Errorf("amd64 status = %s, want up-to-date", rep.Platforms[0].Status)
	}
	if rep.Platforms[1].Status != StatusOutdated {
		t.Errorf("arm64 status = %s, want outdated", rep.Platforms[1].Status)
	}
}

func TestInspectSingleImageAndChartKind(t *testing.T) {
	host := upstream(t)

	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	iref, err := name.ParseReference(host + "/library/single:1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(iref, img); err != nil {
		t.Fatal(err)
	}
	rep, err := Inspect(context.Background(), host+"/library/single:1.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MultiArch || len(rep.Platforms) != 1 || rep.Kind != KindImage {
		t.Errorf("single image report = %+v", rep)
	}

	// A chart: config media type flips the kind (FR-024 groundwork).
	chart := mutate.ConfigMediaType(img, helmConfigMediaType)
	cref, err := name.ParseReference(host + "/bitnamicharts/wordpress:19.2.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(cref, chart); err != nil {
		t.Fatal(err)
	}
	rep, err = Inspect(context.Background(), host+"/bitnamicharts/wordpress:19.2.6", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Kind != KindChart {
		t.Errorf("chart kind = %s", rep.Kind)
	}
}

// pushImage seeds one random single-manifest image at ref.
func pushImage(t *testing.T, ref string) v1.Image {
	t.Helper()
	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	return img
}

// TestInspectOCIScheme locks B-008: the helm "oci://" notation is accepted
// and normalized away — never round-tripped into the canonical reference.
func TestInspectOCIScheme(t *testing.T) {
	host := upstream(t)
	pushImage(t, host+"/bitnamicharts/wordpress:19.2.6")

	rep, err := Inspect(context.Background(), "oci://"+host+"/bitnamicharts/wordpress:19.2.6", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tag != "19.2.6" || !strings.HasSuffix(rep.Repository, "/bitnamicharts/wordpress") {
		t.Errorf("report = %+v", rep)
	}
	if strings.Contains(rep.Reference, "oci://") {
		t.Errorf("canonical reference %q keeps the scheme", rep.Reference)
	}
}

// TestInspectUntaggedResolvesHighestStable locks B-009: without an
// explicit tag or digest, a missing "latest" resolves to the highest
// stable semver tag; pre-releases and non-semver tags never win; an
// existing "latest" keeps winning; and a repository with no semver tag at
// all still answers the TBY-REG-005 refusal.
func TestInspectUntaggedResolvesHighestStable(t *testing.T) {
	host := upstream(t)
	ctx := context.Background()

	repo := host + "/bitnamicharts/victoriametrics"
	for _, tag := range []string{"0.9.0", "0.10.1", "1.0.0-rc.1", "not-semver"} {
		pushImage(t, repo+":"+tag)
	}
	rep, err := Inspect(ctx, repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tag != "0.10.1" || !strings.HasSuffix(rep.Reference, ":0.10.1") {
		t.Errorf("resolved tag = %q (reference %q), want 0.10.1", rep.Tag, rep.Reference)
	}

	// An existing "latest" keeps the historical behavior: no fallback.
	repo2 := host + "/library/app"
	pushImage(t, repo2+":latest")
	pushImage(t, repo2+":9.9.9")
	rep2, err := Inspect(ctx, repo2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Tag != "latest" {
		t.Errorf("tag = %q, want latest untouched", rep2.Tag)
	}

	// No semver tag at all: TBY-REG-005 stands — never a silent fallback.
	repo3 := host + "/library/words"
	pushImage(t, repo3+":stable")
	_, err = Inspect(ctx, repo3, nil)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeRefNotFound {
		t.Errorf("no-semver err = %v, want %s", err, taxonomy.CodeRefNotFound)
	}
}

// TestInspectErrorTaxonomy locks the R-03 mapping: bad reference, unknown
// reference, unreachable registry, and timeout each carry their own code.
func TestInspectErrorTaxonomy(t *testing.T) {
	host := upstream(t)

	check := func(name string, ref string, ctx context.Context, want taxonomy.Code) {
		t.Helper()
		_, err := Inspect(ctx, ref, nil)
		var te *taxonomy.Error
		if !errors.As(err, &te) || te.Code() != want {
			t.Errorf("%s: err = %v, want %s", name, err, want)
		}
	}

	check("bad reference", "spaces are not valid!", context.Background(), taxonomy.CodeBadReference)
	check("unknown reference", host+"/library/ghost:1.0", context.Background(), taxonomy.CodeRefNotFound)
	// 127.0.0.1:1 refuses connections instantly.
	check("unreachable", "127.0.0.1:1/library/redis:7.2", context.Background(), taxonomy.CodeRegistryUnreachable)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	check("timeout", host+"/library/redis:7.2", ctx, taxonomy.CodeInspectTimeout)
}
