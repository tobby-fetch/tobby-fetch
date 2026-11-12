// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package detect

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/klauspost/compress/zstd"

	"github.com/tobby-fetch/tobby-fetch/internal/scan/trivytest"
)

// ADR-0017: what is an image is decided by content. These tests pin the
// detector: media types, archive formats, compression by magic, bounds.

type entry struct {
	name string
	data []byte
	link string
}

func tarBytes(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg}
		if e.link != "" {
			h = &tar.Header{Name: e.name, Linkname: e.link, Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gz(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zst(t testing.TB, b []byte) []byte {
	t.Helper()
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer zw.Close() //nolint:errcheck // encoder only
	return zw.EncodeAll(b, nil)
}

// smallImage is a one-layer image with an OCI image config.
func smallImage(t testing.TB) v1.Image {
	t.Helper()
	layer := gz(t, tarBytes(t, entry{name: "etc/os-release", data: []byte("ID=alpine\n")}))
	img, err := mutate.AppendLayers(mutate.MediaType(empty.Image, types.OCIManifestSchema1), static.NewLayer(layer, types.OCILayer))
	if err != nil {
		t.Fatal(err)
	}
	return mutate.ConfigMediaType(img, types.OCIConfigJSON)
}

// DockerArchive and OCILayoutTar are the shared fixtures.
var (
	DockerArchive = trivytest.DockerArchive
	OCILayoutTar  = trivytest.OCILayoutTar
)

func inspectBytes(t testing.TB, b []byte) *Report {
	t.Helper()
	rep, err := Inspect(context.Background(), bytes.NewReader(b), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestMediaTypes(t *testing.T) {
	for mt, want := range map[string]bool{
		"application/vnd.oci.image.config.v1+json":       true,
		"application/vnd.docker.container.image.v1+json": true,
		"application/vnd.oci.empty.v1+json":              false,
		"application/vnd.aquasec.trivy.config.v1+json":   false,
		"application/vnd.cncf.helm.config.v1+json":       false,
	} {
		if IsImageConfig(mt) != want {
			t.Errorf("IsImageConfig(%s) = %v", mt, !want)
		}
	}
	for mt, want := range map[string]bool{
		"application/vnd.oci.image.layer.v1.tar":                       true,
		"application/vnd.oci.image.layer.v1.tar+gzip":                  true,
		"application/vnd.oci.image.layer.v1.tar+zstd":                  true,
		"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip": true,
		"application/vnd.docker.image.rootfs.diff.tar.gzip":            true,
		"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip":    true,
		// Ends like a filesystem layer, is not one: judged by content.
		"application/vnd.aquasec.trivy.db.layer.v1.tar+gzip":     false,
		"application/vnd.cncf.helm.chart.content.v1.tar+gzip":    false,
		"application/vnd.example.model.v1+gguf":                  false,
		"application/vnd.oci.image.layer.v1.tar+gzip; charset=x": false,
	} {
		if IsFilesystemLayer(mt) != want {
			t.Errorf("IsFilesystemLayer(%s) = %v", mt, !want)
		}
	}
	if !ManifestIsImage("application/vnd.example.custom", []string{"application/vnd.example.blob", "application/vnd.oci.image.layer.v1.tar"}) {
		t.Error("one filesystem layer among custom ones does not make the manifest an image")
	}
	if ManifestIsImage("application/vnd.oci.empty.v1+json", []string{"application/vnd.example.model.v1+gguf"}) {
		t.Error("a genuine artifact is taken for an image")
	}
}

func TestDockerArchiveDetected(t *testing.T) {
	archive := DockerArchive(t, smallImage(t))
	for name, b := range map[string][]byte{"plain": archive, "gzip": gz(t, archive), "zstd": zst(t, archive)} {
		t.Run(name, func(t *testing.T) {
			rep := inspectBytes(t, b)
			if !rep.Tar || rep.Archive != EvidenceDockerArchive {
				t.Fatalf("report = %+v", rep)
			}
		})
	}
}

func TestOCILayoutTarDetected(t *testing.T) {
	rep := inspectBytes(t, OCILayoutTar(t, smallImage(t)))
	if rep.Archive != EvidenceOCILayout {
		t.Fatalf("report = %+v", rep)
	}
}

// An OCI layout holding only an artifact (a chart, a model) is not an
// image archive: the layout rule is the manifest rule.
func TestOCILayoutOfAnArtifactIsNotAnImage(t *testing.T) {
	art, err := mutate.AppendLayers(mutate.MediaType(empty.Image, types.OCIManifestSchema1),
		static.NewLayer([]byte("GGUF weights"), "application/vnd.example.model.v1+gguf"))
	if err != nil {
		t.Fatal(err)
	}
	art = mutate.ConfigMediaType(art, "application/vnd.example.model.config.v1+json")
	if rep := inspectBytes(t, OCILayoutTar(t, art)); rep.Archive != "" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestNestedArchivesInAFilesystemLayer(t *testing.T) {
	archive := DockerArchive(t, smallImage(t))
	layer := gz(t, tarBytes(t,
		entry{name: "etc/app.conf", data: []byte("key=value\n")},
		// Named like anything: the content decides, not the extension.
		entry{name: "opt/models/weights.bin", data: archive},
		entry{name: "./srv/layout.dat", data: gz(t, OCILayoutTar(t, smallImage(t)))},
		entry{name: "docs/manual.tar", data: tarBytes(t, entry{name: "index.html", data: []byte("<p>")})},
	))
	rep := inspectBytes(t, layer)
	if !rep.Tar || rep.Archive != "" {
		t.Fatalf("report = %+v", rep)
	}
	want := map[string]Evidence{"opt/models/weights.bin": EvidenceDockerArchive, "srv/layout.dat": EvidenceOCILayout}
	if len(rep.Nested) != len(want) {
		t.Fatalf("nested = %+v", rep.Nested)
	}
	for _, n := range rep.Nested {
		if want[n.Path] != n.Evidence {
			t.Errorf("nested %s = %s", n.Path, n.Evidence)
		}
	}
}

func TestOrdinaryContentIsNotAnImage(t *testing.T) {
	for name, b := range map[string][]byte{
		"gguf":  []byte("GGUF\x03\x00\x00\x00 model weights"),
		"empty": nil,
		"zeros": make([]byte, 2048),
		"text":  bytes.Repeat([]byte("hello "), 200),
		// A tar of a database, as Trivy publishes it.
		"trivy-db": gz(t, tarBytes(t, entry{name: "trivy.db", data: []byte("bolt")}, entry{name: "metadata.json", data: []byte(`{"Version":2}`)})),
		// A manifest.json that names files the archive does not hold.
		"lone-manifest": tarBytes(t, entry{name: "manifest.json", data: []byte(`[{"Config":"c.json","Layers":["l/layer.tar"]}]`)}),
		// oci-layout without index.json.
		"lone-marker": tarBytes(t, entry{name: "oci-layout", data: []byte(`{"imageLayoutVersion":"1.0.0"}`)}),
	} {
		t.Run(name, func(t *testing.T) {
			rep := inspectBytes(t, b)
			if rep.Archive != "" || len(rep.Nested) != 0 {
				t.Fatalf("report = %+v", rep)
			}
		})
	}
}

// docker save links a layer shared by two images.
func TestDockerArchiveWithLinkedLayer(t *testing.T) {
	b := tarBytes(t,
		entry{name: "a/layer.tar", data: tarBytes(t, entry{name: "f", data: []byte("x")})},
		entry{name: "b/layer.tar", link: "../a/layer.tar"},
		entry{name: "cfg.json", data: []byte(`{}`)},
		entry{name: "manifest.json", data: []byte(`[{"Config":"cfg.json","Layers":["a/layer.tar","b/layer.tar"]}]`)},
	)
	if rep := inspectBytes(t, b); rep.Archive != EvidenceDockerArchive {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBounds(t *testing.T) {
	archive := DockerArchive(t, smallImage(t))
	lim := DefaultLimits
	lim.MaxBytes = 1024
	if _, err := Inspect(context.Background(), bytes.NewReader(archive), lim); !errors.Is(err, ErrLimit) {
		t.Errorf("bytes bound: %v", err)
	}
	lim = DefaultLimits
	lim.MaxEntries = 1
	if _, err := Inspect(context.Background(), bytes.NewReader(archive), lim); !errors.Is(err, ErrLimit) {
		t.Errorf("entries bound: %v", err)
	}
	// A compressed bomb nested in a layer draws on the same budget.
	bomb := gz(t, tarBytes(t, entry{name: "x", data: make([]byte, 1<<20)}))
	layer := tarBytes(t, entry{name: "a.gz", data: bomb}, entry{name: "b.gz", data: bomb})
	lim = DefaultLimits
	lim.MaxBytes = int64(len(layer)) + 1<<20
	if _, err := Inspect(context.Background(), bytes.NewReader(layer), lim); !errors.Is(err, ErrLimit) {
		t.Errorf("shared budget: %v", err)
	}
	// A non-tar stream is recognized from its head: no read past it.
	r := &countingReader{r: bytes.NewReader(make([]byte, 64<<20))}
	if rep, err := Inspect(context.Background(), r, DefaultLimits); err != nil || rep.Tar {
		t.Fatalf("%+v %v", rep, err)
	}
	if r.n > 64<<10 {
		t.Errorf("read %d bytes of a stream that is not a tar", r.n)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestCorruptCompressedContentIsAnError(t *testing.T) {
	bad := gz(t, tarBytes(t, entry{name: "f", data: bytes.Repeat([]byte("a"), 4096)}))
	bad = bad[:len(bad)/2]
	if _, err := Inspect(context.Background(), bytes.NewReader(bad), DefaultLimits); err == nil {
		t.Error("a truncated archive was judged")
	}
}

// FuzzInspect: no input panics, hangs or escapes its bounds.
func FuzzInspect(f *testing.F) {
	img := smallImage(f)
	f.Add(DockerArchive(f, img))
	f.Add(OCILayoutTar(f, img))
	f.Add(gz(f, tarBytes(f, entry{name: "x", data: DockerArchive(f, img)})))
	f.Add([]byte("GGUF"))
	f.Add(make([]byte, 1024))
	lim := Limits{MaxBytes: 4 << 20, MaxEntries: 1024, MaxMetaBytes: 64 << 10, MaxHeldBytes: 256 << 10}
	f.Fuzz(func(t *testing.T, b []byte) {
		rep, err := Inspect(context.Background(), bytes.NewReader(b), lim)
		if err == nil && rep == nil {
			t.Fatal("no report and no error")
		}
	})
}

func TestIsTarHeader(t *testing.T) {
	b := tarBytes(t, entry{name: "f", data: []byte("x")})
	if !IsTarHeader(b) {
		t.Error("a tar header is not recognized")
	}
	b[0] ^= 0xff
	if IsTarHeader(b) {
		t.Error("a corrupted header is recognized")
	}
	if IsTarHeader(make([]byte, 512)) || IsTarHeader([]byte("short")) {
		t.Error("a zero or short block is recognized")
	}
}
