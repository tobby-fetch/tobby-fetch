// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// FR-066: the scriptable half of FR-051. The UC2 flow has to be
// runnable end to end from a shell — export on one side, import on the
// other — with exit codes a script can branch on and machine output it
// can parse. Both are checked here through the real command tree.

package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const layoutTestRepo = "docker.io/library/alpine"

// seedStoreRoot creates a storage directory holding one image and
// returns its path and the pinned digest.
func seedStoreRoot(t *testing.T) (root, dgst string) {
	t.Helper()
	root = t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	}()

	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("one small layer")
	descs := make([]ocispec.Descriptor, 0, 2)
	for i, blob := range [][]byte{config, layer} {
		d := digest.FromBytes(blob)
		if err := st.WriteBlob(ctx, layoutTestRepo, d, bytes.NewReader(blob)); err != nil {
			t.Fatalf("writing blob: %v", err)
		}
		mediaType := ocispec.MediaTypeImageConfig
		if i == 1 {
			mediaType = ocispec.MediaTypeImageLayerGzip
		}
		descs = append(descs, ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(blob))})
	}
	payload, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    descs[0],
		Layers:    descs[1:],
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(ctx, layoutTestRepo, ocispec.MediaTypeImageManifest, payload, "3.22.1")
	if err != nil {
		t.Fatalf("storing the manifest: %v", err)
	}
	return root, d.String()
}

// TestExportImportRoundTripThroughTheCLI is the scripted UC2 leg: one
// store out, another store in, identical digest.
func TestExportImportRoundTripThroughTheCLI(t *testing.T) {
	source, want := seedStoreRoot(t)
	out := filepath.Join(t.TempDir(), "payload.tar")

	if _, err := run(t, "export", "--storage-root", source, "--output", out, "--json"); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("the export produced nothing: %v", err)
	}

	destination := t.TempDir()
	if _, err := run(t, "import", "--storage-root", destination, out, "--json"); err != nil {
		t.Fatalf("import: %v", err)
	}

	st, err := store.Open(context.Background(), destination, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	}()
	_, _, got, err := st.RawManifest(context.Background(), layoutTestRepo, "3.22.1")
	if err != nil {
		t.Fatalf("the content did not arrive: %v", err)
	}
	if got != want {
		t.Errorf("digest after the round trip = %s, want %s", got, want)
	}
}

// TestExportDryRunWritesNothingAndReportsTheProjection: the FR-055
// numbers on stdout, in the machine form a pre-flight script reads, with
// the destination untouched.
func TestExportDryRunWritesNothingAndReportsTheProjection(t *testing.T) {
	source, _ := seedStoreRoot(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "payload.tar")

	stdout, _, err := runSplit(t, "export", "--storage-root", source, "--output", out, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var report struct {
		Format           string `json:"format"`
		TotalBytes       int64  `json:"totalBytes"`
		LargestFileBytes int64  `json:"largestFileBytes"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("parsing %q: %v", stdout, err)
	}
	if report.Format != "tar" || report.TotalBytes == 0 || report.LargestFileBytes == 0 {
		t.Errorf("projection = %+v, want a tar with non-zero sizes", report)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*")); len(entries) != 0 {
		t.Errorf("the dry run wrote %v", entries)
	}
}

// TestExportRefusesAnExistingDestinationWithItsOwnExitClass: a script
// must be able to tell a policy-shaped refusal from a broken store
// without parsing prose (FR-066 exit codes).
func TestExportRefusesAnExistingDestinationWithItsOwnCode(t *testing.T) {
	source, _ := seedStoreRoot(t)
	out := filepath.Join(t.TempDir(), "payload.tar")
	if err := os.WriteFile(out, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := run(t, "export", "--storage-root", source, "--output", out)
	if err == nil {
		t.Fatal("the export replaced an existing destination")
	}
	if code := taxonomyCode(t, err); code != taxonomy.CodeLayoutTarget {
		t.Errorf("code = %s, want %s", code, taxonomy.CodeLayoutTarget)
	}
	if _, err := run(t, "export", "--storage-root", source, "--output", out, "--overwrite"); err != nil {
		t.Fatalf("explicit overwrite: %v", err)
	}
}

// TestImportRefusesAHostileArchive: the NFR-011 refusal reaches the
// command line as its own catalog entry, and nothing lands.
func TestImportRefusesAHostileArchive(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "hostile.tar")
	if err := os.WriteFile(archive, hostileArchive(t), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	_, err := run(t, "import", "--storage-root", destination, archive)
	if err == nil {
		t.Fatal("the hostile archive was imported")
	}
	if code := taxonomyCode(t, err); code != taxonomy.CodeLayoutUnsafe {
		t.Errorf("code = %s, want %s", code, taxonomy.CodeLayoutUnsafe)
	}
}

func taxonomyCode(t *testing.T, err error) taxonomy.Code {
	t.Helper()
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want a taxonomy error", err)
	}
	return te.Code()
}

// hostileArchive builds a layout archive carrying an absolute path — the
// NFR-011 corpus in its shortest form.
func hostileArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	marker := []byte(`{"imageLayoutVersion":"1.0.0"}`)
	index := []byte(`{"schemaVersion":2,"manifests":[]}`)
	pwn := []byte("* * * * * root sh\n")
	for _, f := range []struct {
		name string
		body []byte
	}{
		{ocispec.ImageLayoutFile, marker},
		{ocispec.ImageIndexFile, index},
		{"/etc/cron.d/pwn", pwn},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: f.name, Mode: 0o644, Size: int64(len(f.body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
