// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ocilayout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ImportOptions parameterizes one import.
type ImportOptions struct {
	// Input is the archive or directory to read.
	Input string
	// Repository places entries the layout does not name. A layout
	// produced by Tobby carries the full relocated repository in each
	// entry's reference annotation; one produced by `skopeo copy` carries
	// the bare tag, and there is no honest way to guess where it belongs —
	// so the operator says, once, for the whole archive.
	Repository string
}

// Entry is the outcome of importing one index entry.
type Entry struct {
	Ref Ref
	// Digest is the manifest digest the entry pointed at — unchanged by
	// the transfer, which is the whole claim of FR-051.
	Digest string
	// Err is the per-entry failure, if any. Entries are isolated from one
	// another: a medium that lost one image still delivers the rest, and
	// the report says exactly which one it lost.
	Err error
}

// ImportReport is the outcome of an import.
type ImportReport struct {
	Entries   []Entry
	Manifests int
	Blobs     int
	Bytes     int64
	// Missing is what the layout referenced and did not carry: sparse
	// index platforms (FR-042), and blobs whose absence failed an entry.
	Missing []Missing
	// Ignored lists archive entries that are not part of the layout.
	Ignored []string
}

// Failed counts the entries that did not land.
func (r *ImportReport) Failed() int {
	n := 0
	for i := range r.Entries {
		if r.Entries[i].Err != nil {
			n++
		}
	}
	return n
}

// Import restores a layout into the sink, at identical digests.
//
// Everything that lands is verified on the way in: a manifest is read
// only if its bytes hash to the digest that addressed it, and every blob
// is committed against the digest its manifest pinned. The archive is
// data, not authority — nothing in it decides where a byte goes.
//
// Signatures come back because they were tags (RECIPE-SPEC §12.2): the
// attached ".sig" artifact and the referrers fallback index are index
// entries like any other, and the bundle artifact the fallback index
// refers to is restored BY DIGEST as the index's child. That is the
// symmetry B-015 was missing — here it is not a second code path, it is
// the absence of one.
func Import(ctx context.Context, dst Sink, opts ImportOptions, onEntry func(*Entry)) (*ImportReport, error) {
	if opts.Input == "" {
		return nil, errors.New("ocilayout: an input path is required")
	}
	lay, err := openLayout(opts.Input)
	if err != nil {
		return nil, err
	}
	defer lay.close() //nolint:errcheck // read side

	if err := checkLayoutMarker(lay); err != nil {
		return nil, err
	}
	indexJSON, err := lay.marker(ocispec.ImageIndexFile, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexJSON, &index); err != nil {
		return nil, fmt.Errorf("%w: %s is not valid JSON: %w", ErrNotLayout, ocispec.ImageIndexFile, err)
	}

	im := &importer{dst: dst, lay: lay, seen: map[string]bool{}, report: &ImportReport{Ignored: lay.ignored()}}
	for i := range index.Manifests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		desc := &index.Manifests[i]
		entry := Entry{Digest: desc.Digest.String()}
		ref, err := refOf(desc, opts.Repository)
		entry.Ref = ref
		if err == nil {
			err = im.manifest(ctx, ref.Repo, desc.Digest, desc.MediaType, ref.Tag)
		}
		entry.Err = err
		im.report.Entries = append(im.report.Entries, entry)
		if onEntry != nil {
			onEntry(&im.report.Entries[len(im.report.Entries)-1])
		}
	}
	return im.report, nil
}

// checkLayoutMarker refuses anything whose oci-layout file does not
// declare a major version this build understands. The escape route is
// named in the message, not implied: a layout from a future spec is read
// by the tooling that speaks it, not by guessing here.
func checkLayoutMarker(lay layoutReader) error {
	raw, err := lay.marker(ocispec.ImageLayoutFile, maxLayoutMarkerBytes)
	if err != nil {
		return err
	}
	var marker ocispec.ImageLayout
	if err := json.Unmarshal(raw, &marker); err != nil {
		return fmt.Errorf("%w: %s is not valid JSON: %w", ErrNotLayout, ocispec.ImageLayoutFile, err)
	}
	if major, _, _ := strings.Cut(marker.Version, "."); major != "1" {
		return fmt.Errorf("%w: it declares layout version %q, and this build reads version %s",
			ErrNotLayout, marker.Version, ocispec.ImageLayoutVersion)
	}
	return nil
}

// importer carries the state of one import.
type importer struct {
	dst    Sink
	lay    layoutReader
	seen   map[string]bool
	report *ImportReport
}

// manifest restores one manifest and everything below it, children
// first, so nothing is ever tagged before what it references has landed.
func (im *importer) manifest(ctx context.Context, repo string, dgst digest.Digest, mediaType, tag string) error {
	key := repo + "@" + dgst.String() + "#" + tag
	if im.seen[key] {
		return nil
	}
	im.seen[key] = true

	payload, err := im.readManifest(dgst)
	if err != nil {
		return err
	}
	doc, err := parseManifest(payload)
	if err != nil {
		return fmt.Errorf("ocilayout: parsing manifest %s: %w", dgst, err)
	}
	if mediaType == "" {
		mediaType = doc.MediaType
	}
	if mediaType == "" {
		return fmt.Errorf("%w: manifest %s declares no media type and its index entry names none",
			ErrNotLayout, dgst)
	}

	if isIndexMediaType(mediaType) {
		for _, child := range doc.Manifests {
			if !im.lay.has(child.Digest) {
				// A platform the medium does not carry. The index bytes
				// are restored unchanged all the same, so the pinned
				// digest survives (FR-042, RECIPE-SPEC §7.1) — which is
				// the entire point of not rebuilding it.
				im.report.Missing = append(im.report.Missing, Missing{
					Ref: Ref{Repo: repo}, Digest: child.Digest.String(), Reason: MissingPlatform,
				})
				continue
			}
			if err := im.manifest(ctx, repo, child.Digest, child.MediaType, ""); err != nil {
				return err
			}
		}
	} else {
		for _, desc := range append([]ocispec.Descriptor{doc.Config}, doc.Layers...) {
			if desc.Digest == "" {
				continue // an artifact manifest may carry no config
			}
			if err := im.blob(ctx, repo, desc.Digest, desc.Size); err != nil {
				return err
			}
		}
	}

	landed, err := im.dst.PutManifest(ctx, repo, mediaType, payload, tag)
	if err != nil {
		return fmt.Errorf("ocilayout: storing manifest %s in %s: %w", dgst, repo, err)
	}
	if landed != dgst {
		return fmt.Errorf("ocilayout: manifest %s landed as %s in %s", dgst, landed, repo)
	}
	im.report.Manifests++
	im.report.Bytes += int64(len(payload))
	return nil
}

// readManifest reads and verifies one manifest document from the layout.
func (im *importer) readManifest(dgst digest.Digest) ([]byte, error) {
	rc, size, err := im.lay.blob(dgst)
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	if size > maxManifestBytes {
		return nil, fmt.Errorf("%w: manifest %s is %d bytes, over the %d-byte limit",
			ErrNotLayout, dgst, size, maxManifestBytes)
	}
	payload, err := readBounded(rc, blobPath(dgst), maxManifestBytes)
	if err != nil {
		return nil, err
	}
	if got := digest.FromBytes(payload); got != dgst {
		return nil, fmt.Errorf("%w: %s holds %s", ErrNotLayout, blobPath(dgst), got)
	}
	return payload, nil
}

// blob streams one content blob into the sink. The size the manifest
// declares is the bound: the archive does not get to decide how much of
// the store it fills, and a blob that does not hash to its pinned digest
// is refused by the commit — which is where a substituted layer stops.
func (im *importer) blob(ctx context.Context, repo string, dgst digest.Digest, size int64) error {
	rc, stored, err := im.lay.blob(dgst)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			im.report.Missing = append(im.report.Missing, Missing{
				Ref: Ref{Repo: repo}, Digest: dgst.String(), Reason: MissingBlob,
			})
			return fmt.Errorf("ocilayout: %s does not carry blob %s of %s", ocispec.ImageBlobsDir, dgst, repo)
		}
		return err
	}
	defer rc.Close() //nolint:errcheck // read side
	if stored > size {
		return fmt.Errorf("ocilayout: blob %s is %d bytes in the layout, %d in the manifest that pins it",
			dgst, stored, size)
	}
	if err := im.dst.WriteBlob(ctx, repo, dgst, io.LimitReader(rc, size)); err != nil {
		return fmt.Errorf("ocilayout: storing blob %s in %s: %w", dgst, repo, err)
	}
	im.report.Blobs++
	im.report.Bytes += size
	return nil
}

// refOf reads where an index entry belongs.
//
// A layout Tobby produced names the full relocated repository and tag in
// the reference annotation. A layout `skopeo` produced names the tag
// alone, which is not a location — hence the explicit repository the
// operator supplies for the whole archive, and an error naming the entry
// when they supplied none. Inventing a repository from a bare tag would
// scatter somebody's images into names nobody chose.
func refOf(desc *ocispec.Descriptor, fallbackRepo string) (Ref, error) {
	name := desc.Annotations[ocispec.AnnotationRefName]
	switch {
	case name == "":
		if fallbackRepo == "" {
			return Ref{}, fmt.Errorf("%w: entry %s carries no %s annotation and no repository was given",
				ErrNotLayout, desc.Digest, ocispec.AnnotationRefName)
		}
		return validRef(fallbackRepo, "")
	case !strings.Contains(name, "/"):
		if fallbackRepo == "" {
			return Ref{}, fmt.Errorf("%w: entry %s is named %q, which is a tag and not a repository; "+
				"give the repository the archive should be imported into",
				ErrNotLayout, desc.Digest, name)
		}
		return validRef(fallbackRepo, name)
	default:
		repo, tag := name, ""
		if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
			repo, tag = name[:i], name[i+1:]
		}
		return validRef(repo, tag)
	}
}

// validRef checks a repository and tag against the registry grammar
// before either reaches the store. Belt over the store's own validation:
// the message an operator gets should name the archive entry, not a
// storage-layer complaint two packages down.
func validRef(repo, tag string) (Ref, error) {
	named, err := reference.WithName(repo)
	if err != nil {
		return Ref{}, fmt.Errorf("%w: %q is not a valid repository name: %w", ErrNotLayout, repo, err)
	}
	if tag != "" {
		if _, err := reference.WithTag(named, tag); err != nil {
			return Ref{}, fmt.Errorf("%w: %q is not a valid tag: %w", ErrNotLayout, tag, err)
		}
	}
	return Ref{Repo: repo, Tag: tag}, nil
}
