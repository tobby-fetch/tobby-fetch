// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package trivytest holds the fixtures the scanning tests share across
// packages: the environment of the real-Trivy tests, a known-vulnerable
// image built from nothing (no image is pulled, none is committed), and
// the vulnerability database packaged the way upstream publishes it.
//
// It is imported by tests only.
package trivytest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Environment variables of the real-Trivy tests.
const (
	EnvBinary  = "TOBBY_TEST_TRIVY"
	EnvDB      = "TOBBY_TEST_TRIVY_DB"
	EnvRequire = "TOBBY_TEST_REQUIRE_TRIVY"
)

// KnownCVE is the vulnerability the known-vulnerable image carries: musl
// 1.1.22-r2 on Alpine 3.10, CVSS 9.8 (NVD).
const KnownCVE = "CVE-2019-14697"

// Media types of the upstream database artifact.
const (
	DBLayerType  = "application/vnd.aquasec.trivy.db.layer.v1.tar+gzip"
	DBConfigType = "application/vnd.aquasec.trivy.config.v1+json"
)

// Env locates the real binary and database.
type Env struct {
	Binary string
	DB     string
}

// Require returns the real-Trivy environment, or skips — fails, when
// EnvRequire is set: a suite that reports "ok" for a check it never ran is
// worse than one without the check.
func Require(t testing.TB) Env {
	t.Helper()
	e := Env{Binary: os.Getenv(EnvBinary), DB: os.Getenv(EnvDB)}
	if e.Binary == "" || e.DB == "" {
		msg := EnvBinary + " and " + EnvDB + " are not both set: the real-Trivy test does not run here"
		if os.Getenv(EnvRequire) != "" {
			t.Fatal(msg + " (" + EnvRequire + " is set)")
		}
		t.Skip(msg)
	}
	return e
}

// VulnerableFiles make an image read, to Trivy, as Alpine 3.10.0 with
// musl 1.1.22-r2 installed.
var VulnerableFiles = map[string]string{
	"etc/alpine-release": "3.10.0\n",
	"etc/os-release":     "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.10.0\nPRETTY_NAME=\"Alpine Linux v3.10\"\n",
	"lib/apk/db/installed": "C:Q1rjZG3xDQ6yzcn8Hc2GpL7K4WtEk=\nP:musl\nV:1.1.22-r2\nA:x86_64\nS:368301\nI:598016\n" +
		"T:the musl c library (libc) implementation\nU:http://www.musl-libc.org/\nL:MIT\no:musl\nm:Timo Teräs <timo.teras@iki.fi>\n" +
		"t:1560960413\nc:4d2a74eee3a2d4c3bd2bd7e2ad41ad4a14b0f9ba\nF:lib\nR:libc.musl-x86_64.so.1\nR:ld-musl-x86_64.so.1\n\n",
}

// VulnerableLayer is the one filesystem layer of the known-vulnerable image.
func VulnerableLayer(t testing.TB) []byte {
	t.Helper()
	return TarGz(t, VulnerableFiles)
}

// VulnerableImage builds the known-vulnerable image (linux/amd64).
func VulnerableImage(t testing.TB) v1.Image {
	t.Helper()
	img, err := BuildVulnerableImage()
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// BuildVulnerableImage is VulnerableImage for a caller without a test —
// the crucible's seeding tool.
func BuildVulnerableImage() (v1.Image, error) {
	layerBytes, err := TarGzBytes(VulnerableFiles)
	if err != nil {
		return nil, err
	}
	img, err := mutate.AppendLayers(mutate.MediaType(empty.Image, types.OCIManifestSchema1), static.NewLayer(layerBytes, types.OCILayer))
	if err != nil {
		return nil, err
	}
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "linux", "amd64"
	return mutate.ConfigFile(img, cfg)
}

// BuildDBArtifact wraps a database layer as upstream publishes it, for a
// caller without a test.
func BuildDBArtifact(layer []byte) (v1.Image, error) {
	img, err := mutate.AppendLayers(mutate.MediaType(empty.Image, types.OCIManifestSchema1), static.NewLayer(layer, DBLayerType))
	if err != nil {
		return nil, err
	}
	return mutate.ConfigMediaType(img, DBConfigType), nil
}

// DBLayer returns the database layer: the file at path as it is (the
// upstream db.tar.gz), or the trivy.db and metadata.json of the directory
// at path packed the way upstream packs them.
func DBLayer(t testing.TB, path string) []byte {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: the test's own fixture path
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, name := range []string{"trivy.db", "metadata.json"} {
		p := filepath.Join(path, name)
		f, err := os.Open(p) //nolint:gosec // G304: the test's own fixture path
		if err != nil {
			t.Fatal(err)
		}
		fi, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: fi.Size(), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, f); err != nil {
			t.Fatal(err)
		}
		f.Close() //nolint:errcheck,gosec // read side
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// SyntheticDBLayer is a database layer whose trivy.db is NOT a database:
// enough for every test that carries the database without running Trivy
// on it — transport, unpacking, age — and never for one that scans.
func SyntheticDBLayer(t testing.TB, metadataJSON string) []byte {
	t.Helper()
	return TarGz(t, map[string]string{"trivy.db": "synthetic", "metadata.json": metadataJSON})
}

// DBArtifact wraps a database layer as the OCI artifact upstream
// publishes: one layer of DBLayerType, config of DBConfigType.
func DBArtifact(t testing.TB, layer []byte) v1.Image {
	t.Helper()
	img, err := BuildDBArtifact(layer)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// TarGz packs files into a gzip-compressed tar.
func TarGz(t testing.TB, files map[string]string) []byte {
	t.Helper()
	b, err := TarGzBytes(files)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TarGzBytes packs files into a gzip-compressed tar, in name order.
func TarGzBytes(files map[string]string) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(tw, data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FileDigest is the sha256 digest of a file, in OCI notation — the pin a
// test gives the real binary it runs.
func FileDigest(t testing.TB, path string) string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: the test's own binary path
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read side
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// DockerArchive is `docker save` output for img: the shape an image takes
// when it is smuggled as a file (ADR-0017).
func DockerArchive(t testing.TB, img v1.Image) []byte {
	t.Helper()
	ref, err := name.ParseReference("example.invalid/smuggled:1")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tarball.Write(ref, img, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// OCILayoutTar is an OCI image layout holding img, packed as a tar.
func OCILayoutTar(t testing.TB, img v1.Image) []byte {
	t.Helper()
	dir := t.TempDir()
	p, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendImage(img); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p) //nolint:gosec // G304: the test's own temporary directory
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
