// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Format-level tests of FR-051: what an export writes, what an import
// accepts, and what it refuses. The round trip against the real embedded
// store lives in internal/interop; here the store is a fake, so a
// failure is a failure of the layout and not of storage.

package ocilayout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
)

// exportAll plans and writes every tag of src into a fresh path.
func exportAll(t *testing.T, src *fakeStore, format ocilayout.Format, name string) (string, *ocilayout.Report, *ocilayout.Plan) {
	t.Helper()
	ctx := context.Background()
	sel, err := ocilayout.SelectAll(ctx, src)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	plan, err := ocilayout.NewPlan(ctx, src, sel)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	out := filepath.Join(t.TempDir(), name)
	report, err := ocilayout.Write(ctx, src, plan, ocilayout.ExportOptions{Output: out, Format: format})
	if err != nil {
		t.Fatalf("writing: %v", err)
	}
	return out, report, plan
}

// TestExportedDirectoryIsAConformantLayout checks the three things the
// image-spec says make a layout: the marker file, the index, and
// content-addressed blobs whose path IS their digest.
//
// Checked by running rather than by reading the spec: every blob file is
// re-hashed and compared with the name it sits under, so a writer that
// mangled a byte fails here and not on somebody else's `skopeo`.
func TestExportedDirectoryIsAConformantLayout(t *testing.T) {
	src := newFakeStore()
	src.image(t, "docker.io/library/alpine", "3.22.1", "amd64")

	out, report, _ := exportAll(t, src, ocilayout.FormatDirectory, "layout")

	raw, err := os.ReadFile(filepath.Join(out, ocispec.ImageLayoutFile)) //nolint:gosec // G304: the test's own temporary export
	if err != nil {
		t.Fatalf("reading the layout marker: %v", err)
	}
	var marker ocispec.ImageLayout
	if err := json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("parsing the layout marker: %v", err)
	}
	if marker.Version != ocispec.ImageLayoutVersion {
		t.Errorf("imageLayoutVersion = %q, want %q", marker.Version, ocispec.ImageLayoutVersion)
	}

	index := readIndex(t, out)
	if len(index.Manifests) != 1 {
		t.Fatalf("index.json lists %d entries, want 1", len(index.Manifests))
	}
	if got := index.Manifests[0].Annotations[ocispec.AnnotationRefName]; got != "docker.io/library/alpine:3.22.1" {
		t.Errorf("ref.name = %q, want the full repository and tag", got)
	}

	blobs := filepath.Join(out, ocispec.ImageBlobsDir, "sha256")
	entries, err := os.ReadDir(blobs)
	if err != nil {
		t.Fatalf("reading blobs/sha256: %v", err)
	}
	if len(entries) != report.Manifests+report.Blobs {
		t.Errorf("blobs/sha256 holds %d files, the report claims %d", len(entries), report.Manifests+report.Blobs)
	}
	for _, e := range entries {
		content, err := os.ReadFile(filepath.Join(blobs, e.Name())) //nolint:gosec // G304: the test's own temporary export
		if err != nil {
			t.Fatalf("reading blob %s: %v", e.Name(), err)
		}
		if got := digest.FromBytes(content).Encoded(); got != e.Name() {
			t.Errorf("blob file %s holds content hashing to %s", e.Name(), got)
		}
	}
	// Every entry of index.json must be reachable in blobs/.
	for _, desc := range index.Manifests {
		if _, err := os.Stat(filepath.Join(blobs, desc.Digest.Encoded())); err != nil {
			t.Errorf("index entry %s has no blob: %v", desc.Digest, err)
		}
	}
}

// TestRoundTripRestoresIdenticalDigests is the FR-051 acceptance
// criterion itself: importing what was exported restores the content at
// the same digests, byte for byte.
func TestRoundTripRestoresIdenticalDigests(t *testing.T) {
	for _, format := range []ocilayout.Format{ocilayout.FormatDirectory, ocilayout.FormatTar} {
		t.Run(string(format), func(t *testing.T) {
			src := newFakeStore()
			src.image(t, "docker.io/library/alpine", "3.22.1", "amd64")
			src.image(t, "docker.io/library/nginx", "1.25.0", "nginx")

			out, _, _ := exportAll(t, src, format, "payload")

			dst := newFakeStore()
			report, err := ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: out}, nil)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if n := report.Failed(); n != 0 {
				t.Fatalf("%d entries failed: %+v", n, report.Entries)
			}
			assertSameContent(t, src, dst)
		})
	}
}

// TestSparseIndexKeepsItsPinnedDigest locks FR-042 and RECIPE-SPEC §7.1
// across the transfer: an index listing a platform the store never
// fetched travels unchanged — the same bytes, therefore the same pinned
// digest, therefore every signature over it still verifies. An exporter
// that "helpfully" rebuilt the index over the platforms it actually has
// would produce a different digest and break the delivery it was trying
// to be tidy about.
func TestSparseIndexKeepsItsPinnedDigest(t *testing.T) {
	src := newFakeStore()
	indexDesc, absent := src.sparseIndex(t, "docker.io/library/alpine", "3.22.1")

	out, _, plan := exportAll(t, src, ocilayout.FormatTar, "sparse.tar")

	if len(plan.Missing) != 1 || plan.Missing[0].Reason != ocilayout.MissingPlatform {
		t.Fatalf("plan.Missing = %+v, want one missing platform", plan.Missing)
	}
	if plan.Missing[0].Digest != absent.Digest.String() {
		t.Errorf("missing platform = %s, want %s", plan.Missing[0].Digest, absent.Digest)
	}

	dst := newFakeStore()
	report, err := ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: out}, nil)
	if err != nil {
		t.Fatalf("importing: %v", err)
	}
	if n := report.Failed(); n != 0 {
		t.Fatalf("%d entries failed: %+v", n, report.Entries)
	}
	restored, ok := dst.tags["docker.io/library/alpine"]["3.22.1"]
	if !ok {
		t.Fatal("the tag did not come back")
	}
	if restored != indexDesc.Digest {
		t.Errorf("index digest = %s after the round trip, want %s (the pinned digest changed)", restored, indexDesc.Digest)
	}
	if _, present := dst.manifests["docker.io/library/alpine"][absent.Digest]; present {
		t.Error("the absent platform was invented on import")
	}
}

// TestSignaturesTravelInBothLayouts is the B-015 guard, one level up.
//
// The bug was an asymmetry: a verifier that read both cosign layouts and
// a copier that carried only the classic ".sig" tag, so a bundle-signed
// artifact verified on the zone that fetched it and was gone one hop
// down. An export is one more hop. Both layouts are exercised on the same
// medium — the attached tag, and the referrers fallback index with the
// referring artifact it names BY DIGEST, which is the piece a
// tag-only copier silently drops.
func TestSignaturesTravelInBothLayouts(t *testing.T) {
	src := newFakeStore()
	const repo = "docker.io/cookbook/nginx"
	attachedSubject := src.image(t, repo, "1.25.0", "nginx")
	src.signAttached(t, repo, attachedSubject.Digest)

	bundleSubject := src.image(t, repo, "1.26.0", "nginx-next")
	bundle, fallback := src.signBundle(t, repo, &bundleSubject)

	out, _, _ := exportAll(t, src, ocilayout.FormatTar, "signed.tar")

	dst := newFakeStore()
	report, err := ocilayout.Import(context.Background(), dst, ocilayout.ImportOptions{Input: out}, nil)
	if err != nil {
		t.Fatalf("importing: %v", err)
	}
	if n := report.Failed(); n != 0 {
		t.Fatalf("%d entries failed: %+v", n, report.Entries)
	}

	attachedTag := "sha256-" + attachedSubject.Digest.Encoded() + ".sig"
	if _, ok := dst.tags[repo][attachedTag]; !ok {
		t.Errorf("the attached signature tag %s did not travel", attachedTag)
	}
	fallbackTag := "sha256-" + bundleSubject.Digest.Encoded()
	got, ok := dst.tags[repo][fallbackTag]
	if !ok {
		t.Fatalf("the referrers fallback tag %s did not travel", fallbackTag)
	}
	if got != fallback.Digest {
		t.Errorf("fallback index digest = %s, want %s", got, fallback.Digest)
	}
	// The artifact the fallback index refers to: reachable only by
	// digest, which is precisely why it was the half that got lost.
	if _, ok := dst.manifests[repo][bundle.Digest]; !ok {
		t.Errorf("the referring bundle artifact %s did not travel", bundle.Digest)
	}
	assertSameContent(t, src, dst)
}

// TestProjectionMatchesWhatIsWritten locks the number FR-055's pre-flight
// is going to compare with free space and with a FAT32 volume's 4 GiB
// per-file limit. A projection that is merely close is a refusal that
// fires late or not at all, so it is checked against the bytes actually
// produced — not against a second implementation of the same arithmetic.
func TestProjectionMatchesWhatIsWritten(t *testing.T) {
	src := newFakeStore()
	src.image(t, "docker.io/library/alpine", "3.22.1", "amd64")
	src.sparseIndex(t, "docker.io/library/nginx", "1.25.0")

	t.Run("tar", func(t *testing.T) {
		out, _, plan := exportAll(t, src, ocilayout.FormatTar, "payload.tar")
		projection := plan.Project(ocilayout.FormatTar)
		info, err := os.Stat(out)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if projection.TotalBytes != info.Size() {
			t.Errorf("projected %d bytes, wrote %d", projection.TotalBytes, info.Size())
		}
		if projection.LargestFileBytes != info.Size() {
			t.Errorf("largest file projected at %d, the archive is %d — a single-tar export IS one file (FR-055)",
				projection.LargestFileBytes, info.Size())
		}
	})

	t.Run("directory", func(t *testing.T) {
		out, _, plan := exportAll(t, src, ocilayout.FormatDirectory, "layout")
		projection := plan.Project(ocilayout.FormatDirectory)
		var total, largest int64
		err := filepath.Walk(out, func(_ string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			total += info.Size()
			largest = max(largest, info.Size())
			return nil
		})
		if err != nil {
			t.Fatalf("walking the layout: %v", err)
		}
		if projection.TotalBytes != total {
			t.Errorf("projected %d bytes, wrote %d", projection.TotalBytes, total)
		}
		if projection.LargestFileBytes != largest {
			t.Errorf("largest file projected at %d, largest written is %d", projection.LargestFileBytes, largest)
		}
	})
}

// TestExportRefusesAnExistingDestination keeps an export from quietly
// eating a medium somebody else prepared.
func TestExportRefusesAnExistingDestination(t *testing.T) {
	src := newFakeStore()
	src.image(t, "docker.io/library/alpine", "3.22.1", "amd64")
	ctx := context.Background()
	sel, err := ocilayout.SelectAll(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ocilayout.NewPlan(ctx, src, sel)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "payload.tar")
	if err := os.WriteFile(out, []byte("someone else's medium"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ocilayout.Write(ctx, src, plan, ocilayout.ExportOptions{Output: out}); !errors.Is(err, ocilayout.ErrTargetExists) {
		t.Fatalf("err = %v, want ErrTargetExists", err)
	}
	kept, err := os.ReadFile(out) //nolint:gosec // G304: the test's own temporary export
	if err != nil || string(kept) != "someone else's medium" {
		t.Errorf("the refused export touched the destination: %q, %v", kept, err)
	}

	if _, err := ocilayout.Write(ctx, src, plan, ocilayout.ExportOptions{Output: out, Overwrite: true}); err != nil {
		t.Fatalf("explicit overwrite: %v", err)
	}
	if _, err := ocilayout.Import(ctx, newFakeStore(), ocilayout.ImportOptions{Input: out}, nil); err != nil {
		t.Errorf("the overwritten destination is not a layout: %v", err)
	}
}

// TestInterruptedExportLeavesNothingBehind: a failed write must not leave
// a partial archive that looks finished (NFR-010).
func TestInterruptedExportLeavesNothingBehind(t *testing.T) {
	src := newFakeStore()
	desc := src.image(t, "docker.io/library/alpine", "3.22.1", "amd64")
	// Corrupt one blob so the digest check fails mid-write.
	manifest := src.manifests["docker.io/library/alpine"][desc.Digest]
	var doc ocispec.Manifest
	if err := json.Unmarshal(manifest.payload, &doc); err != nil {
		t.Fatal(err)
	}
	src.blobs[doc.Layers[0].Digest] = []byte("not the layer that was pinned")

	ctx := context.Background()
	sel, err := ocilayout.SelectAll(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ocilayout.NewPlan(ctx, src, sel)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "payload.tar")
	if _, err := ocilayout.Write(ctx, src, plan, ocilayout.ExportOptions{Output: out}); err == nil {
		t.Fatal("a blob that does not hash to its digest was exported anyway")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the failed export left %d files behind: %v", len(entries), entries)
	}
}

// readIndex reads index.json of a directory layout.
func readIndex(t *testing.T, root string) ocispec.Index {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ocispec.ImageIndexFile)) //nolint:gosec // G304: the test's own temporary export
	if err != nil {
		t.Fatalf("reading index.json: %v", err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatalf("parsing index.json: %v", err)
	}
	return index
}

// assertSameContent compares two stores manifest by manifest, tag by tag
// and blob by blob: the transfer is bit-exact or it is not a transfer.
func assertSameContent(t *testing.T, want, got *fakeStore) {
	t.Helper()
	for repo, manifests := range want.manifests {
		for d, m := range manifests {
			landed, ok := got.manifests[repo][d]
			if !ok {
				t.Errorf("manifest %s of %s did not travel", d, repo)
				continue
			}
			if !bytes.Equal(landed.payload, m.payload) {
				t.Errorf("manifest %s of %s changed bytes in transit", d, repo)
			}
			if landed.mediaType != m.mediaType {
				t.Errorf("manifest %s of %s arrived as %q, was %q", d, repo, landed.mediaType, m.mediaType)
			}
		}
	}
	for repo, tags := range want.tags {
		for tag, d := range tags {
			if got.tags[repo][tag] != d {
				t.Errorf("tag %s:%s = %s after transfer, want %s", repo, tag, got.tags[repo][tag], d)
			}
		}
	}
	for d, content := range want.blobs {
		landed, ok := got.blobs[d]
		if !ok {
			// Blobs of a manifest nobody references are not carried; the
			// ones that are must be identical.
			continue
		}
		if !bytes.Equal(landed, content) {
			t.Errorf("blob %s changed bytes in transit", d)
		}
	}
}
