// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/opencontainers/go-digest"
)

// Read-only walks for the on-demand integrity verification (FR-057, R-31).
//
// Everything here reads and reports. Nothing repairs, nothing deletes,
// nothing is written — not even a timestamp: the verification's contract
// is a store byte-identical afterwards, and the one lock it takes is the
// shared side of the content lock.

// HoldContent takes the shared side of the FR-044 content lock and returns
// the function that releases it. While it is held a garbage collection
// cannot remove a blob under the walk's feet — which would read as a
// defect that was never there — and content writes carry on, since they
// hold the same side.
//
// The cost is stated, not hidden: a removal asked for during a long
// verification waits for it. The verification is one at a time and
// read-only, and a removal that waits is an operator who sees why.
func (s *Store) HoldContent() (release func()) {
	s.gcMu.RLock()
	return s.gcMu.RUnlock
}

// BlobFile is one blob of the filesystem backend, as the walk found it.
type BlobFile struct {
	// Digest is the digest the blob is stored under — the one its
	// directory name claims.
	Digest digest.Digest
	// Path is its data file relative to the store root, in slash form.
	Path string
}

// BlobCheck is the verdict on one blob.
type BlobCheck struct {
	BlobFile
	// Size is the number of bytes read.
	Size int64
	// Actual is the digest of the bytes read; empty when they could not be
	// read at all.
	Actual digest.Digest
	// Err says why the bytes could not be read (a missing data file, an I/O
	// error). A blob that reads fine and hashes to another digest has no
	// Err: its Actual differs from its Digest.
	Err error
}

// OK reports whether the blob is sound: readable and hashing to the
// digest it is stored under.
func (c *BlobCheck) OK() bool { return c.Err == nil && c.Actual == c.Digest }

// Blobs lists the blobs of the store's content-addressed tree, sorted by
// digest, without reading any of them. A store with no blob directory has
// none.
func (s *Store) Blobs() ([]BlobFile, error) {
	root := filepath.Join(s.root, "docker", "registry", "v2", "blobs", "sha256")
	prefixes, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading the blob directory: %w", err)
	}
	var out []BlobFile
	for _, p := range prefixes {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		hashes, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("store: reading %s: %w", dir, err)
		}
		for _, h := range hashes {
			if !h.IsDir() {
				continue
			}
			d := digest.NewDigestFromEncoded(digest.SHA256, h.Name())
			out = append(out, BlobFile{
				Digest: d,
				Path:   "docker/registry/v2/blobs/sha256/" + p.Name() + "/" + h.Name() + "/data",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out, nil
}

// CheckBlob re-hashes one blob and compares the result with the digest it
// is stored under. It streams (NFR-007: a blob is never loaded whole) and
// honours ctx between reads, so a cancelled verification stops inside a
// large blob instead of after it.
func (s *Store) CheckBlob(ctx context.Context, b BlobFile) BlobCheck {
	res := BlobCheck{BlobFile: b}
	if err := b.Digest.Validate(); err != nil {
		res.Err = fmt.Errorf("the directory name is not a sha256 digest: %w", err)
		return res
	}
	f, err := os.Open(filepath.Join(s.root, filepath.FromSlash(b.Path))) //nolint:gosec // G304: a path built from the store's own directory listing
	if err != nil {
		res.Err = err
		return res
	}
	defer f.Close() //nolint:errcheck // read side
	info, err := f.Stat()
	if err != nil {
		res.Err = err
		return res
	}
	if !info.Mode().IsRegular() {
		res.Err = errors.New("the data entry is not a regular file")
		return res
	}
	h := sha256.New()
	n, err := io.Copy(h, &ctxReader{ctx: ctx, r: f})
	res.Size = n
	if err != nil {
		res.Err = err
		return res
	}
	res.Actual = digest.NewDigestFromEncoded(digest.SHA256, hex.EncodeToString(h.Sum(nil)))
	return res
}

// ctxReader aborts a copy when its context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Reachable returns every digest reachable from any tag of any repository:
// tagged manifests, index children, configs and layers — the mark phase of
// the FR-044 collection, run without the sweep. A blob in the store whose
// digest is not in this set is unreferenced: exactly what a garbage
// collection would reclaim once its grace period has passed.
//
// Like the sweep's own mark it keeps going past a dangling reference (a
// sparse index child, an unreadable payload): what it could not walk is
// reported by the manifest walk of the verification, not hidden here.
func (s *Store) Reachable(ctx context.Context) (map[digest.Digest]bool, error) {
	repos, err := s.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	global := map[digest.Digest]bool{}
	for _, name := range repos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reach, err := s.markRepository(ctx, name)
		if err != nil {
			return nil, err
		}
		for d := range reach {
			global[d] = true
		}
	}
	return global, nil
}
