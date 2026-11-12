// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivytest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func entries(t *testing.T, layer []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(b)
	}
}

// TestVulnerableImageReadsAsAlpine310: the fixture is only worth something
// if it carries the package database Trivy reads, at the vulnerable
// version, on the platform the scan expects.
func TestVulnerableImageReadsAsAlpine310(t *testing.T) {
	files := entries(t, VulnerableLayer(t))
	if !strings.Contains(files["lib/apk/db/installed"], "P:musl\nV:1.1.22-r2\n") {
		t.Errorf("the package database does not list musl 1.1.22-r2: %q", files["lib/apk/db/installed"])
	}
	if files["etc/alpine-release"] != "3.10.0\n" {
		t.Errorf("alpine-release = %q", files["etc/alpine-release"])
	}
	img := VulnerableImage(t)
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OS != "linux" || cfg.Architecture != "amd64" {
		t.Errorf("platform = %s/%s", cfg.OS, cfg.Architecture)
	}
	if mt, _ := img.MediaType(); !strings.Contains(string(mt), "oci") {
		t.Errorf("media type = %s", mt)
	}
}

func TestDBArtifactShape(t *testing.T) {
	layer := SyntheticDBLayer(t, `{"Version":2,"UpdatedAt":"2026-11-10T00:00:00Z"}`)
	if files := entries(t, layer); files["trivy.db"] != "synthetic" || !strings.Contains(files["metadata.json"], "UpdatedAt") {
		t.Errorf("synthetic layer = %v", files)
	}
	img := DBArtifact(t, layer)
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Layers) != 1 || string(m.Layers[0].MediaType) != DBLayerType || string(m.Config.MediaType) != DBConfigType {
		t.Errorf("manifest = %+v", m)
	}
}

func TestDBLayerFromFileAndDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"trivy.db": "bolt", "metadata.json": "{}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	packed := DBLayer(t, dir)
	if files := entries(t, packed); files["trivy.db"] != "bolt" || files["metadata.json"] != "{}" {
		t.Errorf("packed = %v", files)
	}
	file := filepath.Join(t.TempDir(), "db.tar.gz")
	if err := os.WriteFile(file, packed, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DBLayer(t, file); !bytes.Equal(got, packed) {
		t.Error("a layer file is not returned as it is")
	}
	if d := FileDigest(t, file); !strings.HasPrefix(d, "sha256:") || len(d) != 71 {
		t.Errorf("digest = %q", d)
	}
}

func TestRequireSkipsWithoutEnvironment(t *testing.T) {
	t.Setenv(EnvBinary, "")
	t.Setenv(EnvRequire, "")
	ok := t.Run("inner", func(t *testing.T) {
		Require(t)
		t.Error("Require did not skip")
	})
	if !ok {
		t.Error("the skipped subtest failed")
	}
	t.Setenv(EnvBinary, "/bin/trivy")
	t.Setenv(EnvDB, "/db")
	if e := Require(t); e.Binary != "/bin/trivy" || e.DB != "/db" {
		t.Errorf("env = %+v", e)
	}
}

// TestSmuggledImageShapes: the two file shapes an image takes when it is
// smuggled through a FileSet (ADR-0017) carry what the detector reads —
// a docker-save manifest.json, an OCI layout's oci-layout and index.json —
// with forward-slash names on every host.
func TestSmuggledImageShapes(t *testing.T) {
	img := VulnerableImage(t)
	names := func(archive []byte) map[string]bool {
		t.Helper()
		tr := tar.NewReader(bytes.NewReader(archive))
		out := map[string]bool{}
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(hdr.Name, `\`) {
				t.Errorf("tar entry %q uses a backslash", hdr.Name)
			}
			out[hdr.Name] = true
		}
	}
	if got := names(DockerArchive(t, img)); !got["manifest.json"] {
		t.Errorf("docker archive misses manifest.json: %v", got)
	}
	got := names(OCILayoutTar(t, img))
	if !got["oci-layout"] || !got["index.json"] {
		t.Errorf("OCI layout tar misses oci-layout or index.json: %v", got)
	}
	blobs := 0
	for n := range got {
		if strings.HasPrefix(n, "blobs/sha256/") {
			blobs++
		}
	}
	if blobs < 3 { // manifest, config, one layer
		t.Errorf("OCI layout tar holds %d blobs, want at least 3", blobs)
	}
}
