// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// StoreReader is the byte-exact read surface the promotion path pushes
// from: content that has already been fetched, digest-verified, and
// committed locally. Promotion never re-reads the source registry — the
// bytes that cross into the next zone are the bytes this instance
// verified, not a second download that might have moved underneath it.
type StoreReader interface {
	RawManifest(ctx context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error)
	BlobReader(ctx context.Context, repo, dgst string) (io.ReadCloser, error)
}

// Status computes an item's status relative to the destination BEFORE any
// transfer (FR-026): new when the destination holds nothing under the
// tag, up-to-date when it already points at this exact digest, outdated
// otherwise.
//
// One HEAD answers it, and answering it first is what makes the steady
// state free: a cycle over content that has not moved performs this
// request per artifact and nothing else — no blob enumeration, no upload
// negotiation, no manifest write.
func (d *Destination) Status(ctx context.Context, repo name.Repository, tag, want string) (importer.PlatformStatus, error) {
	desc, err := remote.Head(repo.Tag(tag), d.options(ctx)...)
	if err != nil {
		if isNotFound(err) {
			return importer.StatusNew, nil
		}
		return "", err
	}
	if desc.Digest.String() == want {
		return importer.StatusUpToDate, nil
	}
	return importer.StatusOutdated, nil
}

// PushManifest copies one manifest and everything it references from the
// store into the destination repository, transferring only what the
// destination does not already hold (FR-028), and reports the number of
// bytes actually moved.
//
// The order is the guarantee, not an implementation detail. Blobs first,
// then the manifest that names them: a registry only exposes a manifest
// once every blob it points at is already there, so a push interrupted by
// SIGTERM (FR-093) leaves the destination with unreferenced blobs —
// invisible to pulls, collectable by the registry's own GC — and never
// with a manifest that resolves to a missing layer. The same rule applies
// one level up for an index: children complete before the index listing
// them.
//
// An empty tag pushes the manifest by digest only, which is what an
// index's children need: the tagged object is the index, not its members.
func (d *Destination) PushManifest(ctx context.Context, src StoreReader, srcRepo string, dst name.Repository, ref, tag string) (int64, error) {
	var moved int64
	err := d.pushManifest(ctx, src, srcRepo, dst, ref, tag, &moved)
	return moved, err
}

func (d *Destination) pushManifest(ctx context.Context, src StoreReader, srcRepo string, dst name.Repository, ref, tag string, moved *int64) error {
	payload, mediaType, dgst, err := src.RawManifest(ctx, srcRepo, ref)
	if err != nil {
		return fmt.Errorf("reading %s@%s from the store: %w", srcRepo, ref, err)
	}

	if types.MediaType(mediaType).IsIndex() {
		err = d.pushIndexChildren(ctx, src, srcRepo, dst, payload, moved)
	} else {
		err = d.pushBlobs(ctx, src, srcRepo, dst, payload, moved)
	}
	if err != nil {
		return err
	}

	var target name.Reference = dst.Digest(dgst)
	if tag != "" {
		target = dst.Tag(tag)
	}
	if err := remote.Put(target, rawManifest{bytes: payload, mediaType: types.MediaType(mediaType)}, d.options(ctx)...); err != nil {
		return fmt.Errorf("writing the manifest %s: %w", target, err)
	}
	return nil
}

// pushIndexChildren pushes every child of an index that the store holds.
//
// A missing child is skipped rather than fatal: platform selection
// (FR-022) makes a stored index legitimately sparse — the pinned index
// digest is preserved bit for bit, so it keeps listing platforms this
// instance was never asked to carry. Pushing the index as it stands is
// the correct behaviour; the destination then resolves the platforms that
// are there and answers 404 for the rest, exactly as this instance's own
// store does.
func (d *Destination) pushIndexChildren(ctx context.Context, src StoreReader, srcRepo string, dst name.Repository, payload []byte, moved *int64) error {
	var idx struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(payload, &idx); err != nil {
		return fmt.Errorf("parsing the index of %s: %w", srcRepo, err)
	}
	for _, child := range idx.Manifests {
		err := d.pushManifest(ctx, src, srcRepo, dst, child.Digest, "", moved)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// pushBlobs uploads the config and layer blobs of one manifest.
//
// The differential of FR-028 is enforced at two levels, and both matter.
// Status already skipped this artifact entirely when the destination's
// tag pointed at this digest — the steady-state case, one HEAD and
// nothing else. This is the other case: a partially present image, where
// the registry's own blob existence check decides per blob, so a shared
// base layer is never re-uploaded even when the image on top of it is new.
func (d *Destination) pushBlobs(ctx context.Context, src StoreReader, srcRepo string, dst name.Repository, payload []byte, moved *int64) error {
	var man artifactManifest
	if err := json.Unmarshal(payload, &man); err != nil {
		return fmt.Errorf("parsing the manifest of %s: %w", srcRepo, err)
	}
	blobs := append([]blobDescriptor{{
		MediaType: man.Config.MediaType, Digest: man.Config.Digest, Size: man.Config.Size,
	}}, man.Layers...)
	for _, b := range blobs {
		if b.Digest == "" {
			continue
		}
		h, err := v1.NewHash(b.Digest)
		if err != nil {
			return fmt.Errorf("blob digest %q of %s: %w", b.Digest, srcRepo, err)
		}
		layer := &storeLayer{
			ctx: ctx, src: src, repo: srcRepo, hash: h, size: b.Size,
			mediaType: types.MediaType(b.MediaType), moved: moved,
		}
		if err := remote.WriteLayer(dst, layer, d.options(ctx)...); err != nil {
			return fmt.Errorf("uploading blob %s to %s: %w", b.Digest, dst, err)
		}
	}
	return nil
}

// storeLayer presents one stored blob as a v1.Layer, so the registry
// client can negotiate and stream it.
//
// Compressed returns the stored bytes as they lie: content arrived
// digest-verified and is pushed byte for byte, so there is no compression
// step to redo and nowhere for the bytes to change. Uncompressed and
// DiffID are not implemented because the push path never calls them —
// asked for either, this type says so rather than inventing a
// transformation the pinned digest would not survive.
type storeLayer struct {
	// ctx is the pushing task's context (2026-08 robustness audit).
	// v1.Layer's Compressed cannot receive one — the interface is fixed
	// by go-containerregistry — so the layer carries the context of the
	// push that created it: a canceled task (shutdown, FR-093) must stop
	// the blob stream it feeds, not leave it reading the store on a
	// context.Background() nothing ever cancels.
	ctx       context.Context
	src       StoreReader
	repo      string
	hash      v1.Hash
	size      int64
	mediaType types.MediaType
	// moved accumulates the bytes actually read, which is the bytes
	// actually transferred: a blob the destination already holds is never
	// opened, so it never counts.
	moved *int64
}

func (l *storeLayer) Digest() (v1.Hash, error) { return l.hash, nil }

func (l *storeLayer) Size() (int64, error) { return l.size, nil }

func (l *storeLayer) MediaType() (types.MediaType, error) { return l.mediaType, nil }

func (l *storeLayer) DiffID() (v1.Hash, error) {
	return v1.Hash{}, errors.New("engine: a stored blob is pushed as it lies; its uncompressed identity is never computed")
}

func (l *storeLayer) Uncompressed() (io.ReadCloser, error) {
	return nil, errors.New("engine: a stored blob is pushed as it lies; decompressing it would not survive its pinned digest")
}

func (l *storeLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.src.BlobReader(l.ctx, l.repo, l.hash.String())
	if err != nil {
		return nil, err
	}
	return &countingReader{rc: rc, moved: l.moved}, nil
}

// countingReader tallies what crossed the wire.
type countingReader struct {
	rc    io.ReadCloser
	moved *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		atomic.AddInt64(c.moved, int64(n))
	}
	return n, err
}

// Close releases the store handle and always reports success.
//
// This reader is a request body, and net/http treats a Close error as a
// failed request. The embedded store's reader (distribution's fileReader)
// returns a sentinel error from Close unconditionally — closing is how it
// marks itself spent — so propagating it would fail every upload that had
// just succeeded. Nothing is being hidden: a read that ended early is
// reported by Read, and whether the right bytes arrived is settled by the
// digest the destination computes, not by how the source handle was let go.
func (c *countingReader) Close() error {
	_ = c.rc.Close()
	return nil
}
