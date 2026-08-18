// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
)

// blobs is the resumable-fetch context threaded down the copy tree
// (FR-029, R-29): the SOURCE repository the bytes come from — the
// effective one after substitution, not the relocated one they land in —
// the instance resumer, and where per-blob progress is recorded.
//
// It travels as one value because the three are useless apart: a resumer
// without the source repository cannot address the blob, and a resumer
// without the progress sink resumes invisibly, which on a four-hour
// transfer is barely better than not resuming at all.
type blobs struct {
	src     name.Repository
	resumer *blobfetch.Resumer
	// track records one blob's advance. Nil where no task is watching —
	// the FR-022 index completion of the promotion path, which repairs a
	// sparse index outside any item.
	track func(dgst string, received, total int64, resumed, done bool)
}

// open returns the reader for one blob: the resumable path above the
// configured threshold, plain otherwise.
func (b blobs) open(ctx context.Context, dgst digest.Digest, size int64, plain func() (io.ReadCloser, error)) (io.ReadCloser, error) {
	return b.resumer.OpenOr(ctx, b.src, dgst, size, b.progress(dgst.String()), plain)
}

func (b blobs) progress(dgst string) blobfetch.Progress {
	if b.track == nil {
		return nil
	}
	return func(received, total int64, resumed bool) {
		b.track(dgst, received, total, resumed, false)
	}
}

func (b blobs) done(dgst string) {
	if b.track != nil {
		b.track(dgst, 0, 0, false, true)
	}
}

// Store is the direct-to-storage write surface the engine needs
// (ADR-0005), implemented by the embedded store. Every write verifies the
// pinned digest at commit: the copy is bit-exact or it does not land.
type Store interface {
	HasManifest(ctx context.Context, repo, dgst string) bool
	ResolveTag(ctx context.Context, repo, tag string) (string, bool)
	HasBlob(ctx context.Context, repo string, dgst digest.Digest) bool
	WriteBlob(ctx context.Context, repo string, dgst digest.Digest, r io.Reader) error
	PutManifest(ctx context.Context, repo, mediaType string, payload []byte, tag string) (digest.Digest, error)
}

// copyImage streams one image manifest's blobs — only the missing ones
// (FR-026) — then the manifest itself, bit-exactly (NFR-007: streamed,
// never whole in memory). Returns the transferred byte count.
func copyImage(ctx context.Context, dst Store, repo, tag string, img v1.Image, bl blobs) (int64, error) {
	man, err := img.Manifest()
	if err != nil {
		return 0, err
	}
	var transferred int64
	write := func(h v1.Hash, size int64, open func() (io.ReadCloser, error)) error {
		d := digest.NewDigestFromEncoded(digest.Algorithm(h.Algorithm), h.Hex)
		if dst.HasBlob(ctx, repo, d) {
			return nil
		}
		rc, err := bl.open(ctx, d, size, open)
		if err != nil {
			return err
		}
		defer rc.Close() //nolint:errcheck // read side; the digest-verified commit decides
		if err := dst.WriteBlob(ctx, repo, d, rc); err != nil {
			return err
		}
		bl.done(d.String())
		transferred += size
		return nil
	}

	layers, err := img.Layers()
	if err != nil {
		return transferred, err
	}
	for _, l := range layers {
		h, err := l.Digest()
		if err != nil {
			return transferred, err
		}
		size, _ := l.Size()
		if err := write(h, size, l.Compressed); err != nil {
			return transferred, err
		}
	}
	rawCfg, err := img.RawConfigFile()
	if err != nil {
		return transferred, err
	}
	if err := write(man.Config.Digest, man.Config.Size, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(rawCfg)), nil
	}); err != nil {
		return transferred, err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return transferred, err
	}
	if _, err := dst.PutManifest(ctx, repo, string(man.MediaType), raw, tag); err != nil {
		return transferred, err
	}
	return transferred, nil
}

// copyIndexChildren copies every child of an index — nested indexes
// recursively — untagged (the tagged object is the original index), then
// stores the ORIGINAL index bytes under the tag: the pinned digest is
// preserved even when a platform selection makes it sparse (FR-022).
// selected nil copies every child; otherwise only the listed platform
// digests are copied.
func copyIndexChildren(ctx context.Context, dst Store, repo, tag string, desc *remote.Descriptor, selected map[string]bool, bl blobs) (int64, error) {
	idx, err := desc.ImageIndex()
	if err != nil {
		return 0, err
	}
	man, err := idx.IndexManifest()
	if err != nil {
		return 0, err
	}
	var transferred int64
	for i := range man.Manifests {
		child := &man.Manifests[i]
		if selected != nil && !selected[child.Digest.String()] {
			continue
		}
		if dst.HasManifest(ctx, repo, child.Digest.String()) {
			continue
		}
		if child.MediaType.IsIndex() {
			nested, err := idx.ImageIndex(child.Digest)
			if err != nil {
				return transferred, err
			}
			n, err := copyNestedIndex(ctx, dst, repo, nested, bl)
			transferred += n
			if err != nil {
				return transferred, err
			}
			continue
		}
		img, err := idx.Image(child.Digest)
		if err != nil {
			return transferred, err
		}
		n, err := copyImage(ctx, dst, repo, "", img, bl)
		transferred += n
		if err != nil {
			return transferred, err
		}
	}
	raw, err := idx.RawManifest()
	if err != nil {
		return transferred, err
	}
	if _, err := dst.PutManifest(ctx, repo, string(desc.MediaType), raw, tag); err != nil {
		return transferred, err
	}
	return transferred, nil
}

// copyNestedIndex copies a nested index and all its children, untagged.
func copyNestedIndex(ctx context.Context, dst Store, repo string, idx v1.ImageIndex, bl blobs) (int64, error) {
	man, err := idx.IndexManifest()
	if err != nil {
		return 0, err
	}
	var transferred int64
	for i := range man.Manifests {
		child := &man.Manifests[i]
		if dst.HasManifest(ctx, repo, child.Digest.String()) {
			continue
		}
		if child.MediaType.IsIndex() {
			nested, err := idx.ImageIndex(child.Digest)
			if err != nil {
				return transferred, err
			}
			n, err := copyNestedIndex(ctx, dst, repo, nested, bl)
			transferred += n
			if err != nil {
				return transferred, err
			}
			continue
		}
		img, err := idx.Image(child.Digest)
		if err != nil {
			return transferred, err
		}
		n, err := copyImage(ctx, dst, repo, "", img, bl)
		transferred += n
		if err != nil {
			return transferred, err
		}
	}
	raw, err := idx.RawManifest()
	if err != nil {
		return transferred, err
	}
	mt, err := idx.MediaType()
	if err != nil {
		return transferred, err
	}
	if _, err := dst.PutManifest(ctx, repo, string(mt), raw, ""); err != nil {
		return transferred, err
	}
	return transferred, nil
}

// artifactManifest is the handful of OCI manifest fields the generic copy
// paths read: which blobs to move, and what kind of artifact this is. It
// is NOT the §11.2 recipe layout check — that rule belongs to the format
// and lives in the recipe-spec SDK (cookbook.VerifyManifest).
type artifactManifest struct {
	ArtifactType string           `json:"artifactType"`
	Config       blobDescriptor   `json:"config"`
	Layers       []blobDescriptor `json:"layers"`
}

// blobDescriptor is the descriptor subset those paths read. Size matters
// on the config slot too, and only on the promotion path: pushing a blob
// means declaring its length to the destination before streaming it
// (FR-028), where copying one into the local store only ever needed the
// digest to verify against.
type blobDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// copyArtifact copies a small artifact manifest (a recipe, a cosign
// signature) with its config and layer blobs, fetched from the nominal
// repository, into dst under the given repository and tag. Artifact blobs
// are small by construction; the read is bounded.
func copyArtifact(ctx context.Context, dst Store, remotes *Remotes, nominalRepo, localRepo, tag string, manifestBytes []byte, mediaType string) error {
	var man artifactManifest
	if err := json.Unmarshal(manifestBytes, &man); err != nil {
		return fmt.Errorf("parsing artifact manifest: %w", err)
	}
	repo, _, err := remotes.Repository(nominalRepo)
	if err != nil {
		return err
	}
	blobs := append([]blobDescriptor{man.Config}, man.Layers...)
	for _, b := range blobs {
		if b.Digest == "" {
			continue
		}
		d, err := digest.Parse(b.Digest)
		if err != nil {
			return fmt.Errorf("artifact blob digest %q: %w", b.Digest, err)
		}
		if dst.HasBlob(ctx, localRepo, d) {
			continue
		}
		layer, err := remote.Layer(repo.Digest(b.Digest), remotes.options(ctx)...)
		if err != nil {
			return err
		}
		rc, err := layer.Compressed()
		if err != nil {
			return err
		}
		payload, err := readBounded(rc, maxRecipeBytes)
		_ = rc.Close()
		if err != nil {
			return err
		}
		if err := dst.WriteBlob(ctx, localRepo, d, bytes.NewReader(payload)); err != nil {
			return err
		}
	}
	_, err = dst.PutManifest(ctx, localRepo, mediaType, manifestBytes, tag)
	return err
}
