// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// What a store SAYS about itself, read without hashing a byte.
//
// Verify is minutes of I/O on a full disk; the two functions below are a
// single small read, and they exist because three surfaces need an answer
// long before anyone is willing to pay for verification:
//
//   - the serving gate (FR-054) has to decide at STARTUP whether this
//     store is a medium that arrived from elsewhere;
//   - the Media screen shows the inventory summary — zone, medium
//     identity, resolution timestamp, recipes, volumes — as the first
//     thing an operator sees, before they decide to spend twenty minutes
//     re-hashing a disk (FR-062 amendment R-02);
//   - the same screen exists on the SOURCE side, where there is nothing
//     to verify at all: the medium was just written here, and the summary
//     is the whole screen.
//
// Nothing read here is trusted for anything. Every field is a claim from
// an unsigned document (ADR-0006/ADR-0007); the ones that can be checked
// against bytes are checked by Verify and by nothing else.

// IsMedium reports whether the store at root carries a media manifest —
// that is, whether it is a transportable store a mirror synchronization
// finished (FR-050, FR-054), as opposed to an ordinary working store.
//
// A false answer includes every error: a directory that cannot be read is
// not a medium this instance can reason about, and the gate that asks
// this question must not open on an unreadable disk.
func IsMedium(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(ManifestPath)))
	return err == nil && info.Mode().IsRegular()
}

// ReadManifest reads and validates the medium's manifest.
//
// The block return is the manifest's OWN refusal, in the exact shape
// Verify would have reported it (R-19: an absent, unparseable or
// wrong-version manifest blocks the medium as a whole) — so a screen that
// only summarizes renders the same taxonomy entry, with the same
// parameters, as the verification that would have refused it. The error
// return is for a directory that cannot be opened at all, which is a
// property of the host and not of the medium.
//
// The read goes through an os.Root anchored at the store, like every other
// read of foreign content in this package: a symlink planted at
// meta/media.json must not make Tobby read outside the directory it was
// handed (NFR-011).
func ReadManifest(root string) (*Manifest, *Block, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &Block{
				Code:   taxonomy.CodeMediaManifestMissing,
				Params: map[string]string{"path": ManifestPath},
			}, nil
		}
		return nil, nil, fmt.Errorf("media: opening the store: %w", err)
	}
	defer dir.Close() //nolint:errcheck // read side

	m, block := readManifest(dir)
	return m, block, nil
}
