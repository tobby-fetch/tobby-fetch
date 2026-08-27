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
	"strings"
	"time"
)

// Store reset (FR-046).
//
// What a reset removes is the CONTENT and the ledgers that describe it:
// the registry tree, the provenance ledger and the recipe graph. What it
// keeps is the evidence: the operation history and the task logs, which
// are the audit trail of how the store got into the state somebody chose
// to discard (FR-094 — a trail that a reset erases is not a trail). The
// store format marker stays too, so the emptied directory is still a
// Tobby store rather than an unversioned hole.
//
// The removal is a rename followed by a deletion. The rename is what
// makes the instance immediately usable on an empty store, which FR-046
// asks for in as many words: a multi-gigabyte tree takes minutes to
// unlink, and an operator watching a progress bar to find out whether
// their instance is usable has been told nothing. The deletion that
// follows is bookkeeping; if it is interrupted, the leftovers are swept
// by the next reset and never served, because nothing points at them.

// resetTrashPrefix names the discarded trees while they are being
// deleted. It lives under the Tobby area of the store, whose leading
// underscore cannot collide with a repository name.
const resetTrashPrefix = "_tobby/discarded-"

// ResetResult reports what a reset discarded, so the surfaces can say
// what happened instead of "done".
type ResetResult struct {
	Repositories int
	Bytes        int64
}

// Reset empties the store of its content (FR-046).
//
// It holds the FR-044 exclusive lock for the whole operation: a reset is
// the most destructive store mutation there is, and it must not
// interleave with a transfer holding the shared half (B-017).
func (s *Store) Reset(ctx context.Context, logger *slog.Logger) (ResetResult, error) {
	var res ResetResult
	// Counted before the lock is taken exclusively? No: under it. The
	// numbers reported must describe what was actually discarded, and a
	// count taken outside the lock describes a store somebody was still
	// writing to.
	s.gcMu.Lock()
	defer s.gcMu.Unlock()

	repos, err := s.Repositories(ctx)
	if err != nil {
		return res, err
	}
	res.Repositories = len(repos)
	res.Bytes, err = dirSize(filepath.Join(s.root, "docker"))
	if err != nil {
		return res, fmt.Errorf("store: sizing the content directory: %w", err)
	}

	content := filepath.Join(s.root, "docker")
	if _, err := os.Stat(content); err == nil {
		trash := filepath.Join(s.root, filepath.FromSlash(resetTrashPrefix)+
			time.Now().UTC().Format("20060102T150405.000000000"))
		if err := os.MkdirAll(filepath.Dir(trash), 0o750); err != nil {
			return res, fmt.Errorf("store: creating %s: %w", filepath.Dir(trash), err)
		}
		if err := os.Rename(content, trash); err != nil {
			return res, fmt.Errorf("store: setting the content aside: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("store: inspecting the content directory: %w", err)
	}

	if err := s.clearContentLedgers(); err != nil {
		return res, err
	}
	swept := s.sweepDiscarded(ctx, logger)

	logger.LogAttrs(ctx, slog.LevelInfo, "store reset",
		slog.Int("repositories_removed", res.Repositories),
		slog.Int64("bytes_removed", res.Bytes),
		slog.Int("discarded_trees_deleted", swept),
		slog.String("requirement", "FR-046"))
	return res, nil
}

// clearContentLedgers forgets what the removed content was: the
// provenance ledger (FR-045) and the recipe graph (FR-044). Keeping them
// over an emptied store would make every screen name content that is not
// there.
func (s *Store) clearContentLedgers() error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	for _, name := range []string{"provenance.json", "recipes.json"} {
		path := filepath.Join(s.root, "meta", name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("store: clearing meta/%s: %w", name, err)
		}
	}
	return nil
}

// sweepDiscarded deletes the trees earlier resets set aside, including
// the one this reset just produced. A failure is logged and not fatal:
// the store is already empty and usable, and disk that has not come back
// is a line in the log rather than a reset that reports failure after
// succeeding.
func (s *Store) sweepDiscarded(ctx context.Context, logger *slog.Logger) int {
	dir := filepath.Join(s.root, "_tobby")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	prefix := strings.TrimPrefix(resetTrashPrefix, "_tobby/")
	deleted := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "deleting a discarded content tree",
				slog.String("tree", e.Name()), slog.String("error", err.Error()))
			continue
		}
		deleted++
	}
	return deleted
}
