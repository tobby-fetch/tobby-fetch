// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// FR-051 acceptance, checked by running the tools it names: "an export is
// readable by skopeo and oras; importing the same archive on another
// instance restores the content with identical digests".
//
// A conformance claim verified against our own reader is a claim about
// our reader. So the exports below are handed to the real binaries, and
// to crane's own layout package — used as a library, because
// go-containerregistry is already a dependency and the binary is not
// guaranteed on any runner. When a tool is absent the subtest skips
// LOUDLY, naming it: a skip that says what was not checked is
// information, a silent one is worse than no test.
package ocilayout_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cranelayout "github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

const (
	testRepo = "docker.io/library/alpine"
	testTag  = "3.22.1"
)

// testRef is the reference an exported entry is named by: the full
// relocated repository and its tag, which is also what `skopeo` matches
// on when it addresses one entry of a layout.
const testRef = testRepo + ":" + testTag

// seedStore opens an embedded store holding one small image.
func seedStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})

	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	layer := []byte("a small layer, uncompressed on purpose")
	descriptors := make([]ocispec.Descriptor, 0, 2)
	for _, blob := range []struct {
		mediaType string
		content   []byte
	}{
		{ocispec.MediaTypeImageConfig, config},
		{ocispec.MediaTypeImageLayer, layer},
	} {
		d := digest.FromBytes(blob.content)
		if err := st.WriteBlob(ctx, testRepo, d, strings.NewReader(string(blob.content))); err != nil {
			t.Fatalf("writing blob: %v", err)
		}
		descriptors = append(descriptors, ocispec.Descriptor{
			MediaType: blob.mediaType, Digest: d, Size: int64(len(blob.content)),
		})
	}
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    descriptors[0],
		Layers:    descriptors[1:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutManifest(ctx, testRepo, ocispec.MediaTypeImageManifest, manifest, testTag); err != nil {
		t.Fatalf("storing the manifest: %v", err)
	}
	return st
}

// exportFrom writes st to a fresh layout in the requested format.
func exportFrom(t *testing.T, st *store.Store, format ocilayout.Format, name string) string {
	t.Helper()
	svc := interop.New(st, nil, "", slog.New(slog.DiscardHandler))
	out := filepath.Join(t.TempDir(), name)
	if _, err := svc.Export(context.Background(), &interop.ExportRequest{
		Output: out, Format: format,
	}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	return out
}

// storedDigest is the digest the store holds a tag at.
func storedDigest(t *testing.T, st *store.Store) string {
	t.Helper()
	_, _, dgst, err := st.RawManifest(context.Background(), testRepo, testTag)
	if err != nil {
		t.Fatalf("reading the stored manifest: %v", err)
	}
	return dgst
}

// TestSkopeoReadsTheExport runs the tool FR-051 names, on both shapes.
func TestSkopeoReadsTheExport(t *testing.T) {
	skopeo := lookTool(t, "skopeo")
	st := seedStore(t)
	want := storedDigest(t, st)

	for _, tc := range []struct {
		format    ocilayout.Format
		name      string
		transport string
	}{
		{ocilayout.FormatDirectory, "layout", "oci:"},
		{ocilayout.FormatTar, "payload.tar", "oci-archive:"},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			path := exportFrom(t, st, tc.format, tc.name)
			out, err := exec.Command(skopeo, "inspect", "--raw", //nolint:gosec // G204: a tool path resolved from PATH and fixed arguments
				tc.transport+path+":"+testRef).CombinedOutput()
			if err != nil {
				t.Fatalf("skopeo inspect: %v\n%s", err, out)
			}
			if got := digest.FromBytes(trimTrailingNewline(out)).String(); got != want {
				t.Errorf("skopeo read a manifest hashing to %s, the store holds %s", got, want)
			}
		})
	}
}

// TestOrasReadsTheExport runs the second tool FR-051 names.
func TestOrasReadsTheExport(t *testing.T) {
	oras := lookTool(t, "oras")
	st := seedStore(t)
	want := storedDigest(t, st)
	path := exportFrom(t, st, ocilayout.FormatDirectory, "layout")

	// Addressed by digest, not by the reference annotation: oras splits a
	// layout reference on its LAST colon, so it cannot name an entry whose
	// annotation is a full "repository:tag". The annotation is what makes
	// an import able to restore repositories and what skopeo matches on;
	// oras reaches the same manifest through the digest, which is the
	// identity that matters (documented in the export's help text).
	out, err := exec.Command(oras, "manifest", "fetch", "--oci-layout", "--descriptor", //nolint:gosec // G204: a tool path resolved from PATH and fixed arguments
		path+"@"+want).CombinedOutput()
	if err != nil {
		t.Fatalf("oras manifest fetch: %v\n%s", err, out)
	}
	var desc ocispec.Descriptor
	if err := json.Unmarshal(trimTrailingNewline(out), &desc); err != nil {
		t.Fatalf("parsing the oras descriptor %q: %v", out, err)
	}
	if desc.Digest.String() != want {
		t.Errorf("oras read %s, the store holds %s", desc.Digest, want)
	}
}

// TestCraneLayoutPackageReadsTheExport uses crane's own layout reader.
// The binary is not installed anywhere the project controls; the library
// it is built on already is, and reading the export with it exercises
// exactly the code `crane pull --format=oci` would run.
func TestCraneLayoutPackageReadsTheExport(t *testing.T) {
	st := seedStore(t)
	want := storedDigest(t, st)
	path := exportFrom(t, st, ocilayout.FormatDirectory, "layout")

	idx, err := cranelayout.ImageIndexFromPath(path)
	if err != nil {
		t.Fatalf("crane's layout reader refused the export: %v", err)
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}
	if len(manifest.Manifests) != 1 {
		t.Fatalf("crane sees %d entries, want 1", len(manifest.Manifests))
	}
	entry := manifest.Manifests[0]
	if entry.Digest.String() != want {
		t.Errorf("crane sees %s, the store holds %s", entry.Digest, want)
	}
	// The image itself must be walkable, not merely listed: crane fetches
	// the manifest and every layer through the layout's blobs/.
	img, err := idx.Image(entry.Digest)
	if err != nil {
		t.Fatalf("crane could not open the image: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("crane could not read the layers: %v", err)
	}
	if len(layers) != 1 {
		t.Errorf("crane sees %d layers, want 1", len(layers))
	}
}

// TestSkopeoRoundTripKeepsDigests is the acceptance criterion end to
// end, through a foreign tool: Tobby exports, skopeo copies the export
// into a layout of its own making, and Tobby imports THAT. If the two
// implementations disagreed about anything that matters, the digest
// would not survive the trip.
func TestSkopeoRoundTripKeepsDigests(t *testing.T) {
	skopeo := lookTool(t, "skopeo")
	st := seedStore(t)
	want := storedDigest(t, st)
	exported := exportFrom(t, st, ocilayout.FormatDirectory, "layout")

	viaSkopeo := filepath.Join(t.TempDir(), "skopeo-layout")
	// --preserve-digests: without it skopeo may recompress layers, which
	// is a legitimate thing for a copy tool to do and would make this
	// test about skopeo's compression policy instead of about the layout.
	out, err := exec.Command(skopeo, "copy", "--preserve-digests", //nolint:gosec // G204: a tool path resolved from PATH and fixed arguments
		"oci:"+exported+":"+testRef, "oci:"+viaSkopeo+":"+testRef).CombinedOutput()
	if err != nil {
		t.Fatalf("skopeo copy: %v\n%s", err, out)
	}

	dst := seedEmptyStore(t)
	svc := interop.New(dst, nil, "", slog.New(slog.DiscardHandler))
	report, err := svc.Import(context.Background(), &interop.ImportRequest{Input: viaSkopeo},
		slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("importing what skopeo produced: %v", err)
	}
	if report.Failed() != 0 {
		t.Fatalf("%d entries failed: %+v", report.Failed(), report.Entries)
	}
	_, _, got, err := dst.RawManifest(context.Background(), testRepo, testTag)
	if err != nil {
		t.Fatalf("the round trip did not restore %s:%s: %v", testRepo, testTag, err)
	}
	if got != want {
		t.Errorf("digest after the round trip = %s, want %s", got, want)
	}
}

// seedEmptyStore opens a store with nothing in it.
func seedEmptyStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})
	return st
}

// lookTool resolves a client binary, skipping loudly when it is absent.
func lookTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed on this machine: the FR-051 interoperability claim is NOT checked against it here", name)
	}
	return path
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
