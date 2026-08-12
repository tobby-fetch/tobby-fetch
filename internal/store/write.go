// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"fmt"
	"io"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Write-side accessors: the direct-to-storage import path (ADR-0005). The
// import pipeline streams blobs straight into the storage backend — never
// through the HTTP loopback — and every commit verifies the pinned digest:
// a corrupted or substituted stream cannot land (bit-exact copy, SRS §7.1).

// HasManifest reports whether repo already holds the manifest digest — the
// FR-026 presence check behind new / outdated / up-to-date.
func (s *Store) HasManifest(ctx context.Context, name, dgst string) bool {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return false
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return false
	}
	d, err := digest.Parse(dgst)
	if err != nil {
		return false
	}
	ok, err := ms.Exists(ctx, d)
	return err == nil && ok
}

// ResolveTag returns the digest a local tag currently points at.
func (s *Store) ResolveTag(ctx context.Context, name, tag string) (string, bool) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return "", false
	}
	desc, err := repo.Tags(ctx).Get(ctx, tag)
	if err != nil {
		return "", false
	}
	return desc.Digest.String(), true
}

// HasBlob reports whether repo can already serve the blob (the storage
// backend deduplicates blob content globally; the link is per-repository).
func (s *Store) HasBlob(ctx context.Context, name string, dgst digest.Digest) bool {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return false
	}
	_, err = repo.Blobs(ctx).Stat(ctx, dgst)
	return err == nil
}

// WriteBlob streams one blob into repo. The commit verifies the expected
// digest: a stream that does not hash to dgst is rejected and leaves no
// trace (crash-safe by the library's upload transaction).
func (s *Store) WriteBlob(ctx context.Context, name string, dgst digest.Digest, r io.Reader) error {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return err
	}
	bs := repo.Blobs(ctx)
	if _, err := bs.Stat(ctx, dgst); err == nil {
		return nil // already present: zero transfer (FR-026)
	}
	w, err := bs.Create(ctx)
	if err != nil {
		return fmt.Errorf("store: opening blob upload in %s: %w", name, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Cancel(ctx)
		return fmt.Errorf("store: writing blob %s: %w", dgst, err)
	}
	if _, err := w.Commit(ctx, v1.Descriptor{Digest: dgst}); err != nil {
		_ = w.Cancel(ctx)
		return fmt.Errorf("store: committing blob %s: %w", dgst, err)
	}
	return nil
}

// PutManifest stores a manifest (or index) payload bit-exactly and
// optionally tags it. Index payloads are stored as received — sparse
// indexes keep their pinned digest (FR-022), so dependency verification is
// skipped: presence per platform is the browsing accessors' concern.
func (s *Store) PutManifest(ctx context.Context, name, mediaType string, payload []byte, tag string) (digest.Digest, error) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return "", err
	}
	ms, err := repo.Manifests(ctx, storage.SkipLayerVerification())
	if err != nil {
		return "", fmt.Errorf("store: opening manifests of %s: %w", name, err)
	}
	man, _, err := distribution.UnmarshalManifest(mediaType, payload)
	if err != nil {
		return "", fmt.Errorf("store: parsing manifest payload (%s): %w", mediaType, err)
	}
	dgst, err := ms.Put(ctx, man)
	if err != nil {
		return "", fmt.Errorf("store: storing manifest in %s: %w", name, err)
	}
	if tag != "" {
		if err := repo.Tags(ctx).Tag(ctx, tag, v1.Descriptor{
			Digest:    dgst,
			MediaType: mediaType,
			Size:      int64(len(payload)),
		}); err != nil {
			return "", fmt.Errorf("store: tagging %s:%s: %w", name, tag, err)
		}
	}
	return dgst, nil
}
