// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ocilayout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// maxManifestBytes bounds one manifest or index document, on the way out
// and on the way back in. Manifests are metadata: an index listing every
// platform of a large image is a few tens of kilobytes, and a document
// claiming more than this is not a manifest, it is an attempt at making
// the process hold it in memory (NFR-011).
const maxManifestBytes = 32 << 20

// Plan is the resolved shape of an export, computed before a single byte
// is written: the index the layout will carry, the exact list of blobs
// to copy, and what the selection named and the store does not hold.
//
// It exists as its own step because two different callers need it. The
// writer needs it to stream in one pass; the pre-flight of FR-055 needs
// its arithmetic — projected archive size and largest file written —
// without producing anything. Computing it twice would be two chances to
// disagree about what an export contains.
type Plan struct {
	// Index is the layout's index.json, in the order the selection was
	// given.
	Index ocispec.Index
	// Refs are the selected references that resolved.
	Refs []Ref
	// Missing is what did not (absent tag, sparse platform, absent blob).
	Missing []Missing

	// manifests are the manifest and index documents, payload included:
	// they are blobs of the layout like any other, and they are small
	// enough to hold.
	manifests []plannedBlob
	// blobs are the content blobs — configs and layers — streamed from
	// the store at write time and never held.
	blobs []plannedBlob
}

// plannedBlob is one file of the layout's blobs/ directory.
type plannedBlob struct {
	Digest digest.Digest
	Size   int64
	// Repo is a repository the source can serve this blob from. Blobs are
	// content-addressed and shared; any repository holding it will do.
	Repo string
	// Payload is set for manifests, which the walk has already read.
	Payload []byte
}

// Projection is what a planned export will cost on the medium — the
// numbers FR-055's pre-flight compares against free space and against a
// target filesystem's per-file limit.
//
// It is exposed here rather than recomputed by the pre-flight because
// only this package knows what an export actually writes: the tar's
// headers and padding, the deduplication of blobs shared between two
// images, the manifests that are themselves blobs. Two implementations
// of that arithmetic would differ, and the one that mattered would be
// the one that ran second.
type Projection struct {
	// Format is the shape the projection was computed for.
	Format Format
	// Manifests and Blobs count the files of blobs/ by role; Files is the
	// total number of files written, index.json and oci-layout included.
	Manifests int
	Blobs     int
	Files     int
	// ContentBytes is the payload total: the sum of the distinct blobs.
	ContentBytes int64
	// TotalBytes is what the export occupies on the medium — for a tar,
	// the archive itself, headers and padding included.
	TotalBytes int64
	// LargestFileBytes is the biggest single file the export will write:
	// the whole archive in tar form, the biggest blob in directory form.
	// This is the number a FAT32 target's 4 GiB limit is compared with
	// (FR-055) — Tobby's export is precisely the case that requirement
	// calls out.
	LargestFileBytes int64
}

// NewPlan resolves a selection against the source: every reference is
// read, every index walked into its children, every referenced blob
// listed with the size its descriptor declares.
//
// Absences are collected, never fatal. A tag that vanished between the
// listing and the walk, and an index whose platform manifests were never
// fetched (FR-042 sparse index) are both normal states of a real store;
// the export carries what is there and says what was not.
func NewPlan(ctx context.Context, src Source, sel Selection) (*Plan, error) {
	p := &Plan{
		Index: ocispec.Index{
			Versioned: specs.Versioned{SchemaVersion: 2},
			MediaType: ocispec.MediaTypeImageIndex,
			Manifests: []ocispec.Descriptor{},
		},
	}
	seen := map[digest.Digest]bool{}
	for _, ref := range sel.Refs {
		payload, mediaType, dgst, err := src.RawManifest(ctx, ref.Repo, ref.Tag)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				p.Missing = append(p.Missing, Missing{Ref: ref, Reason: MissingTag})
				continue
			}
			return nil, fmt.Errorf("ocilayout: reading %s: %w", ref, err)
		}
		if len(payload) > maxManifestBytes {
			return nil, fmt.Errorf("ocilayout: manifest of %s is %d bytes, over the %d-byte limit", ref, len(payload), maxManifestBytes)
		}
		d, err := digest.Parse(dgst)
		if err != nil {
			return nil, fmt.Errorf("ocilayout: manifest digest of %s: %w", ref, err)
		}
		p.Index.Manifests = append(p.Index.Manifests, ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    d,
			Size:      int64(len(payload)),
			// The full relocated repository AND the tag, so an import —
			// Tobby's or anyone else's — knows where the entry belongs.
			// skopeo addresses an entry by this annotation, so the same
			// string that restores the repository is the one that makes
			// `skopeo copy oci:layout:<repo>:<tag>` work.
			Annotations: map[string]string{ocispec.AnnotationRefName: ref.String()},
		})
		p.Refs = append(p.Refs, ref)
		if err := p.walk(ctx, src, ref.Repo, d, mediaType, payload, seen); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// walk records one manifest and everything below it. Index children are
// followed; a child the store does not hold is a sparse platform, and
// the index bytes travel unchanged so the pinned digest survives
// (FR-042, RECIPE-SPEC §7.1) — inventing a synthesized index over the
// platforms that happen to be present would change that digest and break
// every signature over it.
func (p *Plan) walk(ctx context.Context, src Source, repo string, dgst digest.Digest, mediaType string, payload []byte, seen map[digest.Digest]bool) error {
	if seen[dgst] {
		return nil
	}
	seen[dgst] = true
	p.manifests = append(p.manifests, plannedBlob{
		Digest: dgst, Size: int64(len(payload)), Repo: repo, Payload: payload,
	})

	doc, err := parseManifest(payload)
	if err != nil {
		return fmt.Errorf("ocilayout: parsing manifest %s of %s: %w", dgst, repo, err)
	}
	if isIndexMediaType(mediaType) {
		for _, child := range doc.Manifests {
			childPayload, childMediaType, childDigest, err := src.RawManifest(ctx, repo, child.Digest.String())
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					p.Missing = append(p.Missing, Missing{
						Ref: Ref{Repo: repo}, Digest: child.Digest.String(), Reason: MissingPlatform,
					})
					continue
				}
				return fmt.Errorf("ocilayout: reading %s of %s: %w", child.Digest, repo, err)
			}
			if len(childPayload) > maxManifestBytes {
				return fmt.Errorf("ocilayout: manifest %s of %s is over the %d-byte limit", child.Digest, repo, maxManifestBytes)
			}
			cd, err := digest.Parse(childDigest)
			if err != nil {
				return fmt.Errorf("ocilayout: manifest digest %s of %s: %w", child.Digest, repo, err)
			}
			if err := p.walk(ctx, src, repo, cd, childMediaType, childPayload, seen); err != nil {
				return err
			}
		}
		return nil
	}

	// A plain manifest references blobs: its config and its layers. The
	// subject descriptor of a referring artifact (§12.2 bundle layout) is
	// deliberately NOT followed — it names a manifest, not a blob, and
	// the subject travels because it was selected in its own right.
	for _, desc := range append([]ocispec.Descriptor{doc.Config}, doc.Layers...) {
		if desc.Digest == "" {
			continue // artifact manifests may carry no config (OCI 1.1)
		}
		if seen[desc.Digest] {
			continue
		}
		seen[desc.Digest] = true
		p.blobs = append(p.blobs, plannedBlob{Digest: desc.Digest, Size: desc.Size, Repo: repo})
	}
	return nil
}

// Project computes what the planned export costs in the given format.
func (p *Plan) Project(format Format) Projection {
	pr := Projection{
		Format:    format,
		Manifests: len(p.manifests),
		Blobs:     len(p.blobs),
	}
	indexJSON, err := marshalIndex(&p.Index)
	if err != nil {
		// The index is built from values this package produced; a
		// marshalling failure is a build defect, and reporting a zero
		// projection would be worse than reporting the sizes it can.
		indexJSON = nil
	}
	layoutJSON := layoutMarkerBytes()

	// Files: oci-layout, index.json, and every blob.
	pr.Files = 2 + pr.Manifests + pr.Blobs
	for _, b := range p.manifests {
		pr.ContentBytes += b.Size
		pr.LargestFileBytes = max(pr.LargestFileBytes, b.Size)
	}
	for _, b := range p.blobs {
		pr.ContentBytes += b.Size
		pr.LargestFileBytes = max(pr.LargestFileBytes, b.Size)
	}

	if format == FormatTar {
		total := tarEntrySize(int64(len(layoutJSON))) + tarEntrySize(int64(len(indexJSON)))
		// The two directory entries the archive carries (blobs/,
		// blobs/sha256/) plus the end-of-archive marker.
		total += 2 * tarHeaderSize
		for _, b := range p.manifests {
			total += tarEntrySize(b.Size)
		}
		for _, b := range p.blobs {
			total += tarEntrySize(b.Size)
		}
		total += tarTrailerSize
		pr.TotalBytes = total
		// One file crosses the medium, and it is the archive: that is the
		// number a 4 GiB per-file limit refuses (FR-055).
		pr.LargestFileBytes = total
		return pr
	}

	pr.TotalBytes = pr.ContentBytes + int64(len(indexJSON)) + int64(len(layoutJSON))
	pr.LargestFileBytes = max(pr.LargestFileBytes, int64(len(indexJSON)))
	return pr
}

// manifestDoc is the union of what a manifest and an index declare, read
// from the raw payload. The typed structs of the libraries are one
// version behind the artifact fields Tobby meets (B-018 taught the same
// lesson from the writing side), so the bytes are the authority.
type manifestDoc struct {
	MediaType string               `json:"mediaType"`
	Config    ocispec.Descriptor   `json:"config"`
	Layers    []ocispec.Descriptor `json:"layers"`
	Manifests []ocispec.Descriptor `json:"manifests"`
	Subject   *ocispec.Descriptor  `json:"subject"`
}

func parseManifest(payload []byte) (*manifestDoc, error) {
	var doc manifestDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// isIndexMediaType reports whether the media type is an image index or a
// Docker manifest list — the two whose references are manifests.
func isIndexMediaType(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex ||
		mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

// marshalIndex renders index.json. Compact, with a trailing newline: it
// is a file an operator will open on the medium.
func marshalIndex(index *ocispec.Index) ([]byte, error) {
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("ocilayout: encoding index.json: %w", err)
	}
	return append(raw, '\n'), nil
}

// layoutMarkerBytes renders the oci-layout marker file, the one file
// whose presence makes a directory a layout at all.
func layoutMarkerBytes() []byte {
	raw, err := json.Marshal(ocispec.ImageLayout{Version: ocispec.ImageLayoutVersion})
	if err != nil {
		panic("ocilayout: encoding the layout marker: " + err.Error())
	}
	return append(raw, '\n')
}
