// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	distribution "github.com/distribution/distribution/v3"
	"github.com/opencontainers/go-digest"
)

// Removal and garbage collection (FR-044, amendment 2026-08-12).
//
// The model is mark-and-sweep from tags: everything reachable from any tag
// of any repository — manifests, index children, configs, layers, and the
// cosign signature artifacts, which are tagged objects themselves
// (sha256-<digest>.sig, ADR-0007) — survives; unreachable manifest
// revisions and unreferenced blobs go. GC runs as part of the removal
// operations, under the store's exclusive lock (content writes hold it
// shared); it never runs on a schedule.
//
// Crash-safety (NFR-010): removal deletes reference links before blobs,
// every step is idempotent, and a kill -9 at any point leaves a store
// whose reachable content is intact — the next removal completes the
// sweep. Concurrent standard-client pushes through /v2/ bypass the
// process-level lock; the minimum-age grace period on unlinked blobs
// covers the commit-to-reference window.
//
// Consequence of the grace period, reported rather than hidden: blobs
// and repository links unlinked while still younger than sweepGrace
// survive this run and are reclaimed by the next one. Every removal logs
// how many it deferred, so disk that has not come back is visible
// instead of mysterious; the on-demand integrity-and-cleanup command of
// R-31 (milestone 6) gives the operator a way to reclaim them without
// deleting something else.

// sweepGrace is the FR-044 minimum age of unreferenced content — blob
// data AND repository links (B-017) — before the sweep may reclaim it:
// freshly committed pieces whose manifest is still on its way are never
// collected. Variable for white-box tests.
var sweepGrace = 5 * time.Minute

// sweepResult counts one sweep in pieces of content — blob data files
// and repository links alike (B-017: both sides of a reference age
// under the same grace): pieces reclaimed, and pieces left behind only
// because the grace period still protects them. Counting both kinds
// keeps the ledger symmetric: what one sweep defers, a later sweep
// removes, and the two numbers must be comparable for that story to be
// checkable.
type sweepResult struct {
	Removed  int
	Deferred int
}

// DeleteRepository removes one repository — tags, manifests, layer links —
// then garbage-collects unreferenced blobs. The policy of WHAT may be
// removed (unit-import provenance only, FR-044 amendment) belongs to the
// caller; ErrRecipeManaged is the mechanism-level guard.
func (s *Store) DeleteRepository(ctx context.Context, name string, logger *slog.Logger) error {
	if managing := s.ManagingRecipes(name); len(managing) > 0 {
		return &RecipeManagedError{Repo: name, Recipes: managing}
	}
	s.gcMu.Lock()
	defer s.gcMu.Unlock()

	dir := filepath.Join(s.root, "docker", "registry", "v2", "repositories", filepath.FromSlash(name))
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("store: removing repository %s: %w", name, err)
	}
	if err := s.dropProvenance(name); err != nil {
		return err
	}
	res, err := s.sweep(ctx)
	if err != nil {
		return err
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "repository removed",
		slog.String("repository", name),
		slog.Int("content_swept", res.Removed),
		slog.Int("content_deferred", res.Deferred))
	return nil
}

// DeleteTag removes one tag of a repository, then garbage-collects: the
// recipe-removal path unlinks content tag by tag (relocated repositories
// are shared across recipes; reachability decides what survives, FR-044).
func (s *Store) DeleteTag(ctx context.Context, name, tag string) error {
	s.gcMu.Lock()
	defer s.gcMu.Unlock()
	repo, err := s.repository(ctx, name)
	if err != nil {
		return err
	}
	if err := repo.Tags(ctx).Untag(ctx, tag); err != nil {
		return mapBrowseErr("untagging "+name+":"+tag, err)
	}
	_, err = s.sweep(ctx)
	return err
}

// RecipeManagedError refuses individual removal of recipe-managed content
// (FR-044 amendment): it names the managing recipes so every surface can
// explain "managed by recipe X — it goes away by removing the recipe".
type RecipeManagedError struct {
	Repo    string
	Recipes []string
}

func (e *RecipeManagedError) Error() string {
	return fmt.Sprintf("repository %s is managed by recipe(s) %v: it goes away by removing the recipe", e.Repo, e.Recipes)
}

// Sweep garbage-collects unreachable manifest revisions and unreferenced
// blobs under the exclusive lock. Exposed for the recipe-removal flow,
// which untags several repositories before one sweep.
func (s *Store) Sweep(ctx context.Context, logger *slog.Logger) error {
	s.gcMu.Lock()
	defer s.gcMu.Unlock()
	res, err := s.sweep(ctx)
	if err != nil {
		return err
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "store swept",
		slog.Int("content_swept", res.Removed),
		slog.Int("content_deferred", res.Deferred))
	return nil
}

// sweep is the mark-and-sweep core. Caller holds gcMu exclusively.
func (s *Store) sweep(ctx context.Context) (sweepResult, error) {
	var res sweepResult
	repos, err := s.Repositories(ctx)
	if err != nil {
		return res, err
	}

	// One cutoff for the whole sweep: blobs and repository links age
	// against the same instant (B-017 — the grace protects the interstice
	// between a link being laid and the manifest that makes it reachable
	// being tagged, on both sides of the reference).
	cutoff := time.Now().Add(-sweepGrace)

	// Mark: per-repository reachable digests (manifest revisions and layer
	// links are repository-scoped), and the global blob set.
	global := map[digest.Digest]bool{}
	for _, name := range repos {
		reach, err := s.markRepository(ctx, name)
		if err != nil {
			return res, err
		}
		for d := range reach {
			global[d] = true
		}
		pruned, err := s.pruneRepositoryLinks(name, reach, cutoff)
		if err != nil {
			return res, err
		}
		res.Removed += pruned.Removed
		res.Deferred += pruned.Deferred
	}

	// Sweep: unreferenced blobs older than the grace period.
	blobs := filepath.Join(s.root, "docker", "registry", "v2", "blobs", "sha256")
	entries, err := os.ReadDir(blobs)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("store: reading blob directory: %w", err)
	}
	for _, prefix := range entries {
		if !prefix.IsDir() {
			continue
		}
		prefixDir := filepath.Join(blobs, prefix.Name())
		hashes, err := os.ReadDir(prefixDir)
		if err != nil {
			return res, fmt.Errorf("store: reading %s: %w", prefixDir, err)
		}
		for _, h := range hashes {
			d := digest.NewDigestFromEncoded(digest.SHA256, h.Name())
			if global[d] {
				continue
			}
			data := filepath.Join(prefixDir, h.Name(), "data")
			if info, err := os.Stat(data); err == nil && info.ModTime().After(cutoff) {
				// Freshly committed: the grace period protects it. Counted,
				// not hidden — the next sweep reclaims it.
				res.Deferred++
				continue
			}
			if err := os.RemoveAll(filepath.Join(prefixDir, h.Name())); err != nil {
				return res, fmt.Errorf("store: sweeping blob %s: %w", d, err)
			}
			res.Removed++
		}
	}
	return res, nil
}

// markRepository walks every tag of one repository and returns the digests
// reachable from them: tagged manifests, index children (recursively),
// configs and layers. Signature artifacts are tagged, so they are walked
// like any other root (FR-044: signatures of retained manifests survive).
func (s *Store) markRepository(ctx context.Context, name string) (map[digest.Digest]bool, error) {
	reach := map[digest.Digest]bool{}
	repo, err := s.repository(ctx, name)
	if err != nil {
		return nil, err
	}
	tags, err := repo.Tags(ctx).All(ctx)
	if err != nil {
		if errors.Is(mapBrowseErr("", err), ErrNotFound) {
			return reach, nil // repository without tags: nothing reachable
		}
		return nil, mapBrowseErr("listing tags of "+name, err)
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return nil, mapBrowseErr("opening manifests of "+name, err)
	}
	for _, tag := range tags {
		desc, err := repo.Tags(ctx).Get(ctx, tag)
		if err != nil {
			continue // racing tag removal: skip
		}
		if err := s.markManifest(ctx, ms, desc.Digest, reach); err != nil {
			return nil, err
		}
	}
	return reach, nil
}

// markManifest marks one manifest and everything it references.
func (s *Store) markManifest(ctx context.Context, ms distribution.ManifestService, d digest.Digest, reach map[digest.Digest]bool) error {
	if reach[d] {
		return nil
	}
	reach[d] = true
	m, err := ms.Get(ctx, d)
	if err != nil {
		return nil //nolint:nilerr // dangling reference (sparse index child): nothing more to mark
	}
	mediaType, _, err := m.Payload()
	if err != nil {
		return nil //nolint:nilerr // unreadable payload: keep the digest marked, mark nothing below
	}
	for _, ref := range m.References() {
		if isIndex(mediaType) {
			// An index references child manifests: walk them.
			if err := s.markManifest(ctx, ms, ref.Digest, reach); err != nil {
				return err
			}
			continue
		}
		reach[ref.Digest] = true
	}
	return nil
}

// pruneRepositoryLinks removes manifest revisions and layer links of one
// repository that are no longer reachable from its tags — except links
// younger than the grace cutoff, which are deferred to a later sweep and
// counted, like deferred blobs, instead of hidden.
//
// The grace on links mirrors the grace on blob data and covers the same
// window from the other side (B-017): the direct-to-storage import
// commits blobs — links first — before the manifest that will make them
// reachable is tagged. A removal sweeping an UNRELATED repository during
// that window used to collect the fresh links unconditionally (only
// blob data enjoyed the grace), leaving the in-flight transfer's
// repository unable to serve content it had just committed. Age is
// judged on the link file itself: the library writes it at commit time,
// so its mtime is the instant the grace protects.
func (s *Store) pruneRepositoryLinks(name string, reach map[digest.Digest]bool, cutoff time.Time) (res sweepResult, err error) {
	base := filepath.Join(s.root, "docker", "registry", "v2", "repositories", filepath.FromSlash(name))
	for _, sub := range []string{
		filepath.Join("_manifests", "revisions", "sha256"),
		filepath.Join("_layers", "sha256"),
	} {
		dir := filepath.Join(base, sub)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return res, fmt.Errorf("store: reading %s: %w", dir, err)
		}
		for _, e := range entries {
			d := digest.NewDigestFromEncoded(digest.SHA256, e.Name())
			if reach[d] {
				continue
			}
			linkDir := filepath.Join(dir, e.Name())
			if linkFresh(linkDir, cutoff) {
				res.Deferred++
				continue
			}
			if err := os.RemoveAll(linkDir); err != nil {
				return res, fmt.Errorf("store: pruning link %s of %s: %w", d, name, err)
			}
			res.Removed++
		}
	}
	return res, nil
}

// linkFresh reports whether the link under dir was laid after the
// cutoff. The "link" file is what the library writes at commit; when it
// is missing, the directory's own mtime answers — and an unreadable
// entry counts as fresh, because deferring a stray directory to the
// next sweep is harmless while deleting an in-flight one is not.
func linkFresh(dir string, cutoff time.Time) bool {
	info, err := os.Stat(filepath.Join(dir, "link"))
	if err != nil {
		info, err = os.Stat(dir)
		if err != nil {
			return true
		}
	}
	return info.ModTime().After(cutoff)
}
