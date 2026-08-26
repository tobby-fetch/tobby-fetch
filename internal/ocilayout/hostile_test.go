// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// NFR-011: an imported layout is untrusted data. The corpus below is
// what a hostile medium looks like — traversals, absolute paths, links,
// compression, substituted content — and every case must be refused with
// nothing written and the offending entry named.
//
// These are written as archives on disk rather than as unit calls into a
// validator, because the property under test is what the IMPORT does with
// a real file somebody hands it, not what a helper returns.

package ocilayout_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
)

// tarFile is one entry of a hand-built archive.
type tarFile struct {
	name     string
	typeflag byte
	linkname string
	body     []byte
	mode     int64
}

// writeTar builds an archive from raw entries — including entries no
// legitimate producer would emit, which is the point.
func writeTar(t *testing.T, files []tarFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hostile.tar")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		typeflag := f.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name: f.name, Typeflag: typeflag, Linkname: f.linkname,
			Size: int64(len(f.body)), Mode: mode,
		}
		if typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing header %q: %v", f.name, err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write(f.body); err != nil {
				t.Fatalf("writing %q: %v", f.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// minimalLayout returns the entries of a well-formed one-image layout,
// so each hostile case differs from a valid archive by exactly the thing
// under test.
func minimalLayout(t *testing.T) []tarFile {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("a layer")
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.FromBytes(config), Size: int64(len(config)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    digest.FromBytes(layer), Size: int64(len(layer)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.FromBytes(manifest), Size: int64(len(manifest)),
			Annotations: map[string]string{ocispec.AnnotationRefName: "docker.io/library/alpine:3.22.1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return []tarFile{
		{name: ocispec.ImageLayoutFile, body: []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{name: ocispec.ImageIndexFile, body: index},
		{name: blobEntry(manifest), body: manifest},
		{name: blobEntry(config), body: config},
		{name: blobEntry(layer), body: layer},
	}
}

func blobEntry(content []byte) string {
	return "blobs/sha256/" + digest.FromBytes(content).Encoded()
}

// TestHostileArchivesAreRefused walks the corpus. Each case asserts two
// things: the import fails, and the store was not touched.
func TestHostileArchivesAreRefused(t *testing.T) {
	victimDigest := digest.FromString("victim")
	cases := []struct {
		name    string
		extra   []tarFile
		wantMsg string
	}{
		{
			name:    "absolute path",
			extra:   []tarFile{{name: "/etc/cron.d/pwn", body: []byte("* * * * * root sh\n")}},
			wantMsg: "absolute",
		},
		{
			name:    "traversal out of the archive",
			extra:   []tarFile{{name: "../../etc/cron.d/pwn", body: []byte("* * * * * root sh\n")}},
			wantMsg: "escapes",
		},
		{
			name:    "traversal hidden inside a plausible path",
			extra:   []tarFile{{name: "blobs/../../../etc/cron.d/pwn", body: []byte("boom")}},
			wantMsg: "escapes",
		},
		{
			name:    "windows-style separator",
			extra:   []tarFile{{name: `..\..\Windows\System32\pwn.dll`, body: []byte("boom")}},
			wantMsg: "absolute",
		},
		{
			name: "symlink escaping the layout",
			extra: []tarFile{{
				name: "blobs/sha256/" + victimDigest.Encoded(),
				// A blob that is really a link to the host's key material:
				// refused on its TYPE, before the target is ever opened.
				typeflag: tar.TypeSymlink, linkname: "/etc/shadow",
			}},
			wantMsg: "links",
		},
		{
			name: "hard link into the host",
			extra: []tarFile{{
				name:     "blobs/sha256/" + victimDigest.Encoded(),
				typeflag: tar.TypeLink, linkname: "/etc/shadow",
			}},
			wantMsg: "links",
		},
		{
			name: "device node",
			extra: []tarFile{{
				name:     "blobs/sha256/" + victimDigest.Encoded(),
				typeflag: tar.TypeChar,
			}},
			wantMsg: "regular files",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTar(t, append(minimalLayout(t), tc.extra...))
			dst := newFakeStore()
			_, err := ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: path}, nil)
			if err == nil {
				t.Fatal("the hostile archive was imported")
			}
			var unsafeEntry *ocilayout.UnsafeEntryError
			if !errors.As(err, &unsafeEntry) {
				t.Fatalf("err = %v, want an UnsafeEntryError", err)
			}
			if !strings.Contains(unsafeEntry.Reason, tc.wantMsg) {
				t.Errorf("reason = %q, want it to mention %q", unsafeEntry.Reason, tc.wantMsg)
			}
			if unsafeEntry.Entry == "" {
				t.Error("the refusal does not name the entry it refused")
			}
			assertNothingWritten(t, dst)
		})
	}
}

// TestCompressedArchiveIsRefused: a gzip stream is not seekable, and
// inflating one to find out how big it is is how a decompression bomb
// wins. Refused with the one-line fix, which is what an operator needs
// at 3 a.m. in front of a locked rack.
func TestCompressedArchiveIsRefused(t *testing.T) {
	plain := writeTar(t, minimalLayout(t))
	raw, err := os.ReadFile(plain) //nolint:gosec // G304: the test's own temporary archive
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "payload.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := newFakeStore()
	_, err = ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: path}, nil)
	var unsafeEntry *ocilayout.UnsafeEntryError
	if !errors.As(err, &unsafeEntry) {
		t.Fatalf("err = %v, want an UnsafeEntryError", err)
	}
	if !strings.Contains(unsafeEntry.Reason, "decompress") {
		t.Errorf("reason = %q, want it to say what to do", unsafeEntry.Reason)
	}
	assertNothingWritten(t, dst)
}

// TestSubstitutedContentIsRefused: a blob file whose bytes do not hash to
// the digest naming it. The digest is the only thing that decides what a
// blob is, so the substitution stops at the read — before anything of the
// entry lands.
func TestSubstitutedContentIsRefused(t *testing.T) {
	files := minimalLayout(t)
	for i := range files {
		if strings.HasPrefix(files[i].name, "blobs/") && bytes.Contains(files[i].body, []byte("schemaVersion")) {
			files[i].body = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
		}
	}
	path := writeTar(t, files)

	dst := newFakeStore()
	report, err := ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: path}, nil)
	if err != nil {
		t.Fatalf("importing: %v", err)
	}
	if report.Failed() != 1 {
		t.Fatalf("failed entries = %d, want 1: %+v", report.Failed(), report.Entries)
	}
	assertNothingWritten(t, dst)
}

// TestRepositoryNamesFromTheArchiveAreValidated: the reference
// annotation is attacker-controlled text. It never becomes a path, but
// it does become a repository name, and a name the registry grammar
// rejects must be refused here — with the entry named — rather than two
// packages down.
func TestRepositoryNamesFromTheArchiveAreValidated(t *testing.T) {
	for _, name := range []string{"../../etc/passwd:1", "/absolute/repo:1", "double//slash:1"} {
		t.Run(name, func(t *testing.T) {
			files := minimalLayout(t)
			for i := range files {
				if files[i].name != ocispec.ImageIndexFile {
					continue
				}
				var index ocispec.Index
				if err := json.Unmarshal(files[i].body, &index); err != nil {
					t.Fatal(err)
				}
				index.Manifests[0].Annotations[ocispec.AnnotationRefName] = name
				raw, err := json.Marshal(index)
				if err != nil {
					t.Fatal(err)
				}
				files[i].body = raw
			}
			dst := newFakeStore()
			report, err := ocilayout.Import(context.Background(), dst,
				ocilayout.ImportOptions{Input: writeTar(t, files)}, nil)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if report.Failed() != 1 {
				t.Fatalf("failed entries = %d, want 1: %+v", report.Failed(), report.Entries)
			}
			assertNothingWritten(t, dst)
		})
	}
}

// TestUnrelatedArchiveEntriesAreReportedNotSilentlyDropped: a stray file
// is not hostile, and refusing the whole medium over it would be worse
// than useless. It is ignored and listed, because content that vanishes
// from a transfer without a word is how an operator finds out too late.
func TestUnrelatedArchiveEntriesAreReportedNotSilentlyDropped(t *testing.T) {
	files := append(minimalLayout(t), tarFile{name: "manifest.json", body: []byte(`[{"Config":"x"}]`)})
	dst := newFakeStore()
	report, err := ocilayout.Import(context.Background(), dst,
		ocilayout.ImportOptions{Input: writeTar(t, files)}, nil)
	if err != nil {
		t.Fatalf("importing: %v", err)
	}
	if report.Failed() != 0 {
		t.Fatalf("the stray entry failed the import: %+v", report.Entries)
	}
	if len(report.Ignored) != 1 || report.Ignored[0] != "manifest.json" {
		t.Errorf("ignored = %v, want the stray entry listed", report.Ignored)
	}
}

// TestDirectoryLayoutRefusesALinkedBlob: the directory form has the same
// property as the archive form. A blob that is a symlink is refused on
// its type; following it and letting the digest check decide would mean
// reading whatever it points at first.
func TestDirectoryLayoutRefusesALinkedBlob(t *testing.T) {
	src := newFakeStore()
	src.image(t, "docker.io/library/alpine", "3.22.1", "amd64")
	out, _, _ := exportAll(t, src, ocilayout.FormatDirectory, "layout")

	blobs := filepath.Join(out, ocispec.ImageBlobsDir, "sha256")
	entries, err := os.ReadDir(blobs)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(blobs, entries[0].Name())
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", victim); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	dst := newFakeStore()
	report, err := ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: out}, nil)
	if err != nil {
		var unsafeEntry *ocilayout.UnsafeEntryError
		if !errors.As(err, &unsafeEntry) {
			t.Fatalf("err = %v, want an UnsafeEntryError", err)
		}
		return
	}
	if report.Failed() != 1 {
		t.Fatalf("failed entries = %d, want 1: %+v", report.Failed(), report.Entries)
	}
	var unsafeEntry *ocilayout.UnsafeEntryError
	if !errors.As(report.Entries[0].Err, &unsafeEntry) {
		t.Fatalf("entry error = %v, want an UnsafeEntryError", report.Entries[0].Err)
	}
}

// assertNothingWritten proves the refusal happened before any content
// landed: a guard that refuses after writing is not a guard.
func assertNothingWritten(t *testing.T, dst *fakeStore) {
	t.Helper()
	if len(dst.manifests) != 0 || len(dst.blobs) != 0 || len(dst.tags) != 0 {
		t.Errorf("the refused import wrote content: %d repositories, %d blobs, %d tag sets",
			len(dst.manifests), len(dst.blobs), len(dst.tags))
	}
}
