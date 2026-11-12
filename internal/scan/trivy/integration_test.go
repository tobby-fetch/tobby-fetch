// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"context"
	"io"
	"slices"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/trivytest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// TestRealTrivyOffline is the integration half of the adapter: the real,
// pinned Trivy binary, a real vulnerability database delivered as an OCI
// artifact in the store, offline, against a known-vulnerable image built
// here — an Alpine 3.10.0 package database listing musl 1.1.22-r2, which
// carries CVE-2019-14697 (CVSS 9.8).
//
// It needs TOBBY_TEST_TRIVY (the binary) and TOBBY_TEST_TRIVY_DB (the
// upstream database layer, db.tar.gz as `oras pull` writes it, or an
// unpacked database directory); CI provides both, pinned, and sets
// TOBBY_TEST_REQUIRE_TRIVY so a runner that lost them fails instead of
// skipping.
func TestRealTrivyOffline(t *testing.T) {
	env := trivytest.Require(t)
	st := newMemStore()
	img := st.putV1(t, appRepo, "", trivytest.VulnerableImage(t))
	d := st.putV1(t, "tobby/ghcr.io/aquasecurity/trivy-db", DBTag, trivytest.DBArtifact(t, trivytest.DBLayer(t, env.DB)))
	st.record(store.IngredientRecord{Name: "trivy-db", Kind: "OCIArtifact", Repo: "tobby/ghcr.io/aquasecurity/trivy-db", Tag: DBTag, Digest: d})

	digest, err := fileDigest(env.Binary)
	if err != nil {
		t.Fatal(err)
	}
	s := New(&Options{
		Store: st, WorkDir: t.TempDir(), Path: env.Binary, Digest: digest, BasePrefix: "tobby",
		Timeout: 5 * time.Minute,
	})
	status := s.Refresh(context.Background())
	if !status.Available || status.Database == nil {
		t.Fatalf("status = %+v (%v)", status, s.Cause())
	}
	t.Logf("trivy %s, database built %s", status.Scanner.Version, status.Database.BuiltAt)
	res, err := s.Scan(context.Background(), scan.Target{Digest: img, Repository: appRepo, Kind: "ContainerImage"})
	if err != nil {
		t.Fatal(err)
	}
	var hit *scan.Finding
	for i := range res.Findings {
		if res.Findings[i].ID == trivytest.KnownCVE && res.Findings[i].Package == "musl" {
			hit = &res.Findings[i]
		}
	}
	if hit == nil {
		ids := make([]string, 0, len(res.Findings))
		for _, f := range res.Findings {
			ids = append(ids, f.ID+"/"+f.Package)
		}
		slices.Sort(ids)
		t.Fatalf("%s not found in %v", trivytest.KnownCVE, ids)
	}
	if hit.CVSS == nil || *hit.CVSS < 9 || hit.Severity != scan.SeverityCritical {
		t.Errorf("finding = %+v", *hit)
	}

	// Offline, and the policy blocks it at 7.0.
	pol := scan.Combine(&scan.Levels{InstanceMode: scan.ModeBlock, InstanceThreshold: 7})
	rep := scan.Evaluate(&pol,
		scan.Subject{Reference: appRepo + "@" + img}, res, scan.Freshness{MaxAge: 100 * 365 * 24 * time.Hour}, time.Now())
	if !rep.Blocks() || !slices.Contains(rep.Policy.Violations, trivytest.KnownCVE) {
		t.Errorf("evaluation = %+v", rep.Policy)
	}
	if rep.Database.AgeSeconds <= 0 {
		t.Errorf("the report states no database age: %+v", rep.Database)
	}
}

// TestRealTrivyByContent: the real binary, against images that are not
// declared as images (ADR-0017, B-054) — the known-vulnerable image pushed
// as an OCI artifact, and carried as a `docker save` file and as an OCI
// layout tar inside a FileSet. Each is detected by its content, handed to
// Trivy as a one-image layout, and the known CVE is found in it.
func TestRealTrivyByContent(t *testing.T) {
	env := trivytest.Require(t)
	st := newMemStore()
	d := st.putV1(t, "tobby/ghcr.io/aquasecurity/trivy-db", DBTag, trivytest.DBArtifact(t, trivytest.DBLayer(t, env.DB)))
	st.record(store.IngredientRecord{Name: "trivy-db", Kind: "OCIArtifact", Repo: "tobby/ghcr.io/aquasecurity/trivy-db", Tag: DBTag, Digest: d})
	vuln := trivytest.VulnerableImage(t)
	disguised := st.putV1(t, appRepo, "", vuln)
	files := trivytest.TarGz(t, map[string]string{
		"etc/site.conf":         "a=b\n",
		"opt/model/weights.bin": string(trivytest.DockerArchive(t, vuln)),
		"opt/model/layout.data": string(trivytest.OCILayoutTar(t, vuln)),
	})
	fsImg, err := mutate.AppendLayers(mutate.MediaType(empty.Image, types.OCIManifestSchema1), static.NewLayer(files, types.OCILayer))
	if err != nil {
		t.Fatal(err)
	}
	fileSet := st.putV1(t, appRepo, "", mutate.ConfigMediaType(fsImg, types.OCIConfigJSON))

	s := New(&Options{
		Store: st, WorkDir: t.TempDir(), Path: env.Binary, Digest: trivytest.FileDigest(t, env.Binary), BasePrefix: "tobby",
		Timeout: 5 * time.Minute,
	})
	if status := s.Refresh(context.Background()); !status.Available || status.Database == nil {
		t.Fatalf("status = %+v (%v)", status, s.Cause())
	}
	for kind, dgst := range map[string]string{"OCIArtifact": disguised, "FileSet": fileSet} {
		t.Run(kind, func(t *testing.T) {
			target := scan.Target{Digest: dgst, Repository: appRepo, Kind: kind}
			c, err := s.Inspect(context.Background(), target)
			if err != nil || !c.IsImage() {
				t.Fatalf("content = %+v, %v", c, err)
			}
			if kind == "FileSet" && len(c.Images) != 3 {
				t.Fatalf("images = %+v", c.Images)
			}
			target.Content = c
			res, err := s.Scan(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(res.Findings, func(f scan.Finding) bool { return f.ID == trivytest.KnownCVE && f.Package == "musl" }) {
				t.Fatalf("%s not found in %+v", trivytest.KnownCVE, res.Findings)
			}
		})
	}
}

// putV1 writes an image built with go-containerregistry into the store.
func (m *memStore) putV1(t *testing.T, repo, tag string, img v1.Image) string {
	t.Helper()
	raw, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	m.blob(cfg)
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		rc.Close() //nolint:errcheck,gosec // test read
		m.blob(b)
	}
	mt, err := img.MediaType()
	if err != nil {
		t.Fatal(err)
	}
	d := digestOf(raw)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manifests[repo+"@"+d] = raw
	m.types[repo+"@"+d] = string(mt)
	if tag != "" {
		m.manifests[repo+":"+tag] = raw
		m.types[repo+":"+tag] = string(mt)
	}
	return d
}
