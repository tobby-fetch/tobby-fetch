// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"context"
	"slices"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/trivytest"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Content inspection against the store, and the scan of what it finds
// (ADR-0017, B-054). The fake binary refuses anything but a one-image OCI
// layout, so every scan below also proves the materialization.

const disguisedRepo = "tobby/registry.example/models/llm"

// artifact builds a one-manifest OCI artifact with the given config media
// type and layers.
func artifact(t *testing.T, configType types.MediaType, layers ...v1.Layer) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(mutate.MediaType(empty.Image, types.OCIManifestSchema1), layers...)
	if err != nil {
		t.Fatal(err)
	}
	return mutate.ConfigMediaType(img, configType)
}

func inspectOK(t *testing.T, s *Scanner, kind, digest string) *scan.Content {
	t.Helper()
	c, err := s.Inspect(context.Background(), scan.Target{Digest: digest, Repository: disguisedRepo, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if c.Declared != kind {
		t.Errorf("declared = %s", c.Declared)
	}
	return c
}

func evidences(c *scan.Content) []string {
	out := []string{}
	for _, im := range c.Images {
		out = append(out, im.Evidence+":"+im.Path)
	}
	return out
}

// scanFound runs the scan of what Inspect found and returns the number
// of images the fake binary was given.
func scanFound(t *testing.T, f *fixture, s *Scanner, kind, digest string, c *scan.Content) int {
	t.Helper()
	before := f.calls.images(t)
	res, err := s.Scan(context.Background(), scan.Target{Digest: digest, Repository: disguisedRepo, Kind: kind, Content: c})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Findings, func(fd scan.Finding) bool { return fd.ID == trivytest.KnownCVE }) {
		t.Errorf("the replayed finding is missing: %+v", res.Findings)
	}
	return f.calls.images(t) - before
}

func readyScanner(t *testing.T, f *fixture) *Scanner {
	t.Helper()
	s := New(&f.opts)
	if st := s.Refresh(context.Background()); !st.Available || st.Database == nil {
		t.Fatalf("status = %+v", st)
	}
	return s
}

// An OCI artifact whose config is an image config IS an image.
func TestInspectArtifactWithImageConfig(t *testing.T) {
	f := newFixture(t, fakeConf{})
	d := f.st.putV1(t, disguisedRepo, "", trivytest.VulnerableImage(t))
	s := readyScanner(t, f)
	c := inspectOK(t, s, "OCIArtifact", d)
	if got := evidences(c); !slices.Equal(got, []string{scan.EvidenceImageConfig + ":"}) {
		t.Fatalf("evidences = %v", got)
	}
	if n := scanFound(t, f, s, "OCIArtifact", d, c); n != 1 {
		t.Errorf("%d images scanned", n)
	}
}

// A custom config does not hide filesystem layers that really are tars.
func TestInspectArtifactWithCustomConfigAndTarLayers(t *testing.T) {
	f := newFixture(t, fakeConf{})
	d := f.st.putV1(t, disguisedRepo, "", artifact(t, "application/vnd.example.llm.config.v1+json",
		static.NewLayer(trivytest.VulnerableLayer(t), types.OCILayer)))
	s := readyScanner(t, f)
	c := inspectOK(t, s, "OCIArtifact", d)
	if got := evidences(c); !slices.Equal(got, []string{scan.EvidenceFilesystemLayers + ":"}) {
		t.Fatalf("evidences = %v", got)
	}
	if n := scanFound(t, f, s, "OCIArtifact", d, c); n != 1 {
		t.Errorf("%d images scanned", n)
	}
}

// Genuine artifacts stay out of scope: a model, a plain file pushed by
// ORAS under the OCI tar type, the Trivy database itself, a Helm chart.
func TestInspectGenuineArtifacts(t *testing.T) {
	f := newFixture(t, fakeConf{})
	cases := map[string]v1.Image{
		"model": artifact(t, "application/vnd.example.llm.config.v1+json",
			static.NewLayer([]byte("GGUF\x03\x00\x00\x00weights"), "application/vnd.example.llm.weights.v1+gguf")),
		"oras-plain-file": artifact(t, "application/vnd.oci.empty.v1+json",
			static.NewLayer([]byte("not a tar at all, whatever its media type says"), types.OCIUncompressedLayer)),
		"trivy-db": trivytest.DBArtifact(t, trivytest.SyntheticDBLayer(t, `{"Version":2}`)),
		"helm-chart": artifact(t, "application/vnd.cncf.helm.config.v1+json",
			static.NewLayer(trivytest.TarGz(t, map[string]string{"chart/Chart.yaml": "name: x\n"}), "application/vnd.cncf.helm.chart.content.v1.tar+gzip")),
	}
	s := New(&f.opts)
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			d := f.st.putV1(t, disguisedRepo, "", img)
			if c := inspectOK(t, s, "OCIArtifact", d); c.IsImage() {
				t.Fatalf("a genuine artifact is taken for an image: %v", evidences(c))
			}
		})
	}
}

// An artifact whose layer blob is a `docker save` archive, under a custom
// media type: the archive is the image.
func TestInspectArtifactCarryingADockerArchive(t *testing.T) {
	f := newFixture(t, fakeConf{})
	d := f.st.putV1(t, disguisedRepo, "", artifact(t, "application/vnd.example.llm.config.v1+json",
		static.NewLayer(trivytest.DockerArchive(t, trivytest.VulnerableImage(t)), "application/vnd.example.llm.weights.v1")))
	s := readyScanner(t, f)
	c := inspectOK(t, s, "OCIArtifact", d)
	if got := evidences(c); !slices.Equal(got, []string{scan.EvidenceDockerArchive + ":"}) {
		t.Fatalf("evidences = %v", got)
	}
	if n := scanFound(t, f, s, "OCIArtifact", d, c); n != 1 {
		t.Errorf("%d images scanned", n)
	}
}

// fileSet stores a FileSet — an image whose files are the delivery — with
// the given files in its one layer.
func fileSet(t *testing.T, f *fixture, files map[string]string) string {
	t.Helper()
	layer := trivytest.TarGz(t, files)
	img := artifact(t, types.OCIConfigJSON, static.NewLayer(layer, types.OCILayer))
	return f.st.putV1(t, disguisedRepo, "", img)
}

func TestInspectFileSetCarryingImageArchives(t *testing.T) {
	vuln := trivytest.VulnerableImage(t)
	cases := map[string]struct {
		file     string
		evidence string
	}{
		"docker-archive":   {string(trivytest.DockerArchive(t, vuln)), scan.EvidenceDockerArchive},
		"oci-layout-tar":   {string(trivytest.OCILayoutTar(t, vuln)), scan.EvidenceOCILayout},
		"ordinary-archive": {string(trivytest.TarGz(t, map[string]string{"x": "y"})), ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fakeConf{})
			d := fileSet(t, f, map[string]string{"etc/site.conf": "a=b\n", "data/weights.bin": c.file})
			s := readyScanner(t, f)
			content := inspectOK(t, s, "FileSet", d)
			want := []string{scan.EvidenceImageConfig + ":"}
			if c.evidence != "" {
				want = append(want, c.evidence+":data/weights.bin")
			}
			if got := evidences(content); !slices.Equal(got, want) {
				t.Fatalf("evidences = %v, want %v", got, want)
			}
			// The FileSet's own image and each archive, every one a
			// one-image layout of its own.
			if n := scanFound(t, f, s, "FileSet", d, content); n != len(want) {
				t.Errorf("%d images scanned, want %d", n, len(want))
			}
		})
	}
}

// An archive that the scan cannot materialize fails the scan: under a
// blocking policy the item is refused as not scanned.
func TestDetectedArchiveThatCannotBeMaterialized(t *testing.T) {
	f := newFixture(t, fakeConf{})
	d := fileSet(t, f, map[string]string{"x.tar": "y"})
	s := readyScanner(t, f)
	bogus := &scan.Content{Declared: "FileSet", Images: []scan.DetectedImage{{Digest: d, Path: "missing.tar", Evidence: scan.EvidenceDockerArchive}}}
	m, _, err := readManifest(context.Background(), f.st, disguisedRepo, d)
	if err != nil {
		t.Fatal(err)
	}
	bogus.Images[0].Digest = m.Layers[0].Digest
	_, err = s.Scan(context.Background(), scan.Target{Digest: d, Repository: disguisedRepo, Kind: "FileSet", Content: bogus})
	if codeOf(err) != taxonomy.CodeScanFailed {
		t.Fatalf("err = %v", err)
	}
}

// Content that cannot be judged — a truncated gzip layer — is an
// inspection error, never "not an image".
func TestInspectUnreadableContent(t *testing.T) {
	f := newFixture(t, fakeConf{})
	gzLayer := trivytest.TarGz(t, map[string]string{"a": string(make([]byte, 1<<16))})
	d := f.st.putV1(t, disguisedRepo, "", artifact(t, "application/vnd.example.config", static.NewLayer(gzLayer[:len(gzLayer)/2], "application/vnd.example.blob")))
	s := New(&f.opts)
	_, err := s.Inspect(context.Background(), scan.Target{Digest: d, Repository: disguisedRepo, Kind: "OCIArtifact"})
	if codeOf(err) != taxonomy.CodeScanFailed {
		t.Fatalf("err = %v", err)
	}
}

// Inspection needs neither the binary nor the database: an image hiding
// under another kind is recognized in the degraded mode too.
func TestInspectWithoutScanner(t *testing.T) {
	f := newFixture(t, fakeConf{})
	d := f.st.putV1(t, disguisedRepo, "", trivytest.VulnerableImage(t))
	s := New(&f.opts) // never refreshed: no binary, no database
	if c := inspectOK(t, s, "OCIArtifact", d); !c.IsImage() {
		t.Fatal("not detected without a scanner")
	}
	if _, err := s.Scan(context.Background(), scan.Target{Digest: d, Repository: disguisedRepo, Kind: "OCIArtifact"}); codeOf(err) != taxonomy.CodeScannerUnavailable {
		t.Errorf("scan err = %v", err)
	}
}

// An index child is inspected like the manifest it is.
func TestInspectIndexChildren(t *testing.T) {
	f := newFixture(t, fakeConf{})
	child := f.st.putV1(t, disguisedRepo, "", trivytest.VulnerableImage(t))
	raw, _, _, err := f.st.RawManifest(context.Background(), disguisedRepo, child)
	if err != nil {
		t.Fatal(err)
	}
	idx := f.st.manifest(disguisedRepo, "", ociIndexType, map[string]any{
		"schemaVersion": 2, "mediaType": ociIndexType,
		"manifests": []map[string]any{
			{"mediaType": ociManifestType, "digest": child, "size": len(raw), "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
			{"mediaType": ociManifestType, "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000", "size": 1},
		},
	})
	s := New(&f.opts)
	c := inspectOK(t, s, "OCIArtifact", idx)
	if len(c.Images) != 1 || c.Images[0].Digest != child {
		t.Fatalf("images = %+v", c.Images)
	}
}
