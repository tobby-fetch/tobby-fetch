// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/opencontainers/go-digest"
)

// Raw read accessors: the byte-exact reads the recipe engine, the
// signature verifier (FR-033: reusable offline on an opened store), and
// the FileSet server (FR-047) build on. Always the storage library over
// the shared root, never the HTTP loopback (ADR-0005).

// RawManifest returns the exact stored bytes of a manifest addressed by
// tag or by digest, with its media type and digest. Unknown references
// yield ErrNotFound.
func (s *Store) RawManifest(ctx context.Context, name, reference string) (payload []byte, mediaType, dgst string, err error) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return nil, "", "", err
	}
	d, derr := digest.Parse(reference)
	if derr != nil {
		desc, err := repo.Tags(ctx).Get(ctx, reference)
		if err != nil {
			return nil, "", "", mapBrowseErr("resolving tag "+reference+" of "+name, err)
		}
		d = desc.Digest
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return nil, "", "", mapBrowseErr("opening manifests of "+name, err)
	}
	m, err := ms.Get(ctx, d)
	if err != nil {
		return nil, "", "", mapBrowseErr("reading manifest "+d.String()+" of "+name, err)
	}
	mt, raw, err := m.Payload()
	if err != nil {
		return nil, "", "", fmt.Errorf("store: manifest payload of %s: %w", name, err)
	}
	return raw, mt, d.String(), nil
}

// BlobReader opens a streaming reader over one blob of a repository
// (NFR-007: blobs are never loaded whole in memory). Unknown digests
// yield ErrNotFound.
func (s *Store) BlobReader(ctx context.Context, name, dgst string) (io.ReadCloser, error) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return nil, err
	}
	d, err := digest.Parse(dgst)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid digest %q", ErrNotFound, dgst)
	}
	rc, err := repo.Blobs(ctx).Open(ctx, d)
	if err != nil {
		return nil, mapBrowseErr("opening blob "+dgst+" of "+name, err)
	}
	return rc, nil
}

// Tags lists the tags of one repository, sorted ascending. Unknown
// repositories yield ErrNotFound.
func (s *Store) Tags(ctx context.Context, name string) ([]string, error) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return nil, err
	}
	tags, err := repo.Tags(ctx).All(ctx)
	if err != nil {
		return nil, mapBrowseErr("listing tags of "+name, err)
	}
	sort.Strings(tags)
	return tags, nil
}
