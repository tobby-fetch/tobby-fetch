// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package ocilayout exports the embedded store to, and imports it from,
// the standard OCI image layout — directory or single tar (FR-051,
// ADR-0006).
//
// This is the product's exit ramp, and it is deliberate: an operator who
// stops using Tobby must be able to leave with their bytes, readable by
// `skopeo`, `oras` and `crane`. Nothing here is a Tobby-specific
// container format — the layout produced is the one the image-spec
// defines, and a layout produced by those tools imports back.
//
// Three properties the rest of the package is built around:
//
//   - Bit-exact transfer. Manifests and blobs are copied as bytes,
//     never re-serialized. The original index survives — including a
//     partial platform set (sparse index, RECIPE-SPEC §7.1) — so the
//     pinned digest of FR-042 is the same on both sides.
//   - Signatures travel with the content (RECIPE-SPEC §12.2). Both
//     cosign layouts are tags in the subject's repository — the attached
//     "sha256-<hex>.sig" and the OCI referrers fallback "sha256-<hex>",
//     whose index refers to the bundle artifact by digest — so a
//     selection that names them carries them, and the recursive walk
//     carries what they point at. B-015 was exactly the asymmetry
//     between a reader that knew both layouts and a copier that knew
//     one; here there is one walk, and it has no layout opinion at all.
//   - An imported archive is untrusted data (NFR-011). Nothing read from
//     it ever becomes a filesystem path: blobs are addressed by the
//     digest they hash to, and every entry name is matched against the
//     three shapes the layout defines before it is looked at.
//
// Tobby signs nothing (ADR-0006): signatures are transported, never
// produced.
package ocilayout

import (
	"context"
	"errors"
	"io"

	"github.com/opencontainers/go-digest"
)

// ErrNotFound reports content absent from the source. Sources map their
// own "unknown" error onto it — the same seam sigverify uses, so this
// package stays a format implementation rather than a store consumer.
var ErrNotFound = errors.New("ocilayout: not found")

// Format is the shape an export takes on the medium.
type Format string

// The two shapes ADR-0006 names. Both are the same layout; the tar is
// one file, which is what a decontamination station and a FAT32 volume
// each have an opinion about (FR-055).
const (
	FormatDirectory Format = "directory"
	FormatTar       Format = "tar"
)

// ParseFormat reads a format name, defaulting an empty one to the tar:
// a single file is what crosses a physical gap.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case "":
		return FormatTar, nil
	case FormatTar:
		return FormatTar, nil
	case FormatDirectory:
		return FormatDirectory, nil
	default:
		return "", errors.New("unknown layout format " + s + `: use "tar" or "directory"`)
	}
}

// Source is the read side of the store an export walks. The embedded
// store satisfies it; the interface exists so this package is testable
// without one and so the walk cannot reach for anything else.
type Source interface {
	// Repositories lists every repository of the store.
	Repositories(ctx context.Context) ([]string, error)
	// Tags lists one repository's tags.
	Tags(ctx context.Context, repo string) ([]string, error)
	// RawManifest returns the exact stored bytes of a manifest addressed
	// by tag or by digest. Absent content yields ErrNotFound.
	RawManifest(ctx context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error)
	// BlobReader opens one blob for streaming. Absent content yields
	// ErrNotFound.
	BlobReader(ctx context.Context, repo, dgst string) (io.ReadCloser, error)
}

// Sink is the write side an import lands on. Implementations verify the
// committed digest and hold the FR-044 lock shared while they write
// (B-017): an import IS a content write, and a garbage collection must
// not run over it.
type Sink interface {
	WriteBlob(ctx context.Context, repo string, dgst digest.Digest, r io.Reader) error
	PutManifest(ctx context.Context, repo, mediaType string, payload []byte, tag string) (digest.Digest, error)
}

// Ref is one exported (repository, tag) pair. A tag, not a digest: the
// layout's index entries are what a third-party tool addresses by name,
// and every cosign signature artifact is itself a tag (§12.2), so a
// selection expressed in tags carries the signatures without knowing
// anything about them.
type Ref struct {
	Repo string
	Tag  string
}

// String renders the reference the way a registry client writes it.
func (r Ref) String() string { return r.Repo + ":" + r.Tag }

// Selection is the ordered, de-duplicated set of references an export
// carries.
type Selection struct {
	Refs []Ref
}

// Add appends a reference unless it is already selected.
func (s *Selection) Add(refs ...Ref) {
	for _, ref := range refs {
		if ref.Repo == "" || ref.Tag == "" {
			continue
		}
		if s.has(ref) {
			continue
		}
		s.Refs = append(s.Refs, ref)
	}
}

func (s *Selection) has(ref Ref) bool {
	for _, existing := range s.Refs {
		if existing == ref {
			return true
		}
	}
	return false
}

// SelectAll selects every tag of every repository — the whole-store
// export. Signature artifacts need no special case: they are tags.
func SelectAll(ctx context.Context, src Source) (Selection, error) {
	var sel Selection
	repos, err := src.Repositories(ctx)
	if err != nil {
		return sel, err
	}
	for _, repo := range repos {
		tags, err := src.Tags(ctx, repo)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // repository without any tag
			}
			return sel, err
		}
		for _, tag := range tags {
			sel.Add(Ref{Repo: repo, Tag: tag})
		}
	}
	return sel, nil
}

// SelectRepositories selects every tag of the named repositories.
func SelectRepositories(ctx context.Context, src Source, repos []string) (Selection, error) {
	var sel Selection
	for _, repo := range repos {
		tags, err := src.Tags(ctx, repo)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return sel, err
		}
		for _, tag := range tags {
			sel.Add(Ref{Repo: repo, Tag: tag})
		}
	}
	return sel, nil
}

// SignatureRefs names the two tags a subject's signatures may sit under
// in its own repository (RECIPE-SPEC §12.2): the classic cosign attached
// artifact and the OCI 1.1 referrers fallback index, whose entries point
// at the bundle artifact by digest. Both are listed unconditionally —
// which of them exists is the publisher's choice, and an exporter that
// guessed would reproduce B-015 one level up.
func SignatureRefs(repo, subjectDigest string) []Ref {
	d, err := digest.Parse(subjectDigest)
	if err != nil {
		return nil
	}
	hex := d.Encoded()
	return []Ref{
		{Repo: repo, Tag: "sha256-" + hex + ".sig"},
		{Repo: repo, Tag: "sha256-" + hex},
	}
}

// Missing is one piece of content a selection named and the store does
// not hold. Reported rather than fatal: a sparse index (FR-042) is
// missing platform manifests by construction, and an export that refused
// to run on one would refuse to run on the normal case.
type Missing struct {
	// Ref is the selected reference, when the absence is a whole entry.
	Ref Ref
	// Digest names the absent manifest or blob.
	Digest string
	// Reason is a short, stable, untranslated label ("tag", "platform",
	// "blob"): surfaces localize around it, they do not print it raw.
	Reason string
}

// The Missing reasons. Untranslated labels, like the kind glossary.
const (
	MissingTag      = "tag"
	MissingPlatform = "platform"
	MissingBlob     = "blob"
)
