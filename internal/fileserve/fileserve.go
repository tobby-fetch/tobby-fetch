// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package fileserve is the read-only HTTP file service over verified
// FileSets (FR-047, roadmap 3.6). A FileSet is a standard OCI image whose
// layers are filesystem tars; each enabled FileSet is extracted once into
// a local cache following the normative extraction semantics of
// RECIPE-SPEC §7.4 — layers applied in manifest order, OCI/overlayfs
// whiteouts honored (`.wh.<name>` deletes, `.wh..wh..opq` empties the
// directory) — under the §14.5 safety rules: absolute paths and ".."
// components rejected, no symlink or hardlink may escape the rootfs,
// device/FIFO/socket entries ignored, setuid/setgid bits stripped, and
// configurable anti-decompression-bomb limits (total bytes, file count,
// path depth). The extracted tree is then served under /files/<name>/… .
//
// Error surface: the clients of this endpoint are package managers (apt,
// dnf, zypper…), not humans, so responses are bare HTTP statuses — no
// taxonomy-formatted bodies here. The human-facing surfaces (R-03)
// remain the UI and the JSON API.
//
// Traversal resistance (NFR-011) is enforced twice: at extraction time by
// strict entry validation, and at serve time by opening every file
// through an os.Root anchored on the extracted rootfs, so that even a
// symlink planted inside the cache can never resolve outside it.
//
// The package also owns the other direction of the same contract:
// operator FileSet packing (FR-048, pack.go) turns a local file tree
// into a single-manifest FileSet image written straight into the store.
// The two halves live together because they enforce one rule set — what
// extraction refuses under §14.5, packing refuses first — and because a
// packed FileSet has to be one this server can serve.
package fileserve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/opencontainers/go-digest"
)

// Blobs is the store read surface (implemented elsewhere by the embedded
// store).
type Blobs interface {
	// Manifest returns the raw image-manifest bytes of repo@dgst.
	Manifest(ctx context.Context, repo, dgst string) ([]byte, error)
	// Blob opens a streaming reader over a blob of repo.
	Blob(ctx context.Context, repo, dgst string) (io.ReadCloser, error)
}

// Limits bounds an extraction (§14.5 anti-decompression-bomb). Zero
// values pick the documented defaults (8 GiB, 1M files, depth 64).
type Limits struct {
	// MaxBytes caps the total uncompressed bytes materialized per FileSet.
	MaxBytes int64
	// MaxFiles caps the number of entries materialized per FileSet.
	MaxFiles int
	// MaxDepth caps the number of path components of any entry.
	MaxDepth int
}

// Extraction limit defaults (§14.5), applied for zero Limits fields.
const (
	DefaultMaxBytes int64 = 8 << 30
	DefaultMaxFiles       = 1_000_000
	DefaultMaxDepth       = 64
)

func (l Limits) effective() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultMaxFiles
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// FileSet is one enabled, verified FileSet (FR-047: enablement is
// explicit, per FileSet, by configuration — wired by the caller).
type FileSet struct {
	Name string // the /files/<name>/ segment
	Repo string // relocated repository in the store
	// ManifestDigest is the concrete image manifest (platform already
	// selected by the caller — §7.4 step 1).
	ManifestDigest string
	// Anonymous is the anonymous-read opt-in (the caller enforces auth;
	// carried for reporting).
	Anonymous bool
}

// markerFile flags a completed extraction inside its digest directory.
// It is written in the temporary directory before the atomic rename, so
// its absence at the final path means the extraction must be redone.
const markerFile = ".complete"

// servedSet is one FileSet wired for serving: its rootfs is opened as an
// os.Root so no path — including through internal symlinks — can resolve
// outside it (NFR-011, §14.5).
type servedSet struct {
	set  FileSet
	root *os.Root
}

// Server extracts enabled FileSets into a cache directory and serves
// them read-only.
type Server struct {
	blobs    Blobs
	cacheDir string
	limits   Limits
	logger   *slog.Logger

	// syncMu serializes Sync calls; mu guards the served map against the
	// request path.
	syncMu sync.Mutex
	mu     sync.RWMutex
	served map[string]*servedSet
	order  []string // Sync order of served names, for stable Enabled()
}

// NewServer builds a Server over the given blob store and cache
// directory. Zero limits pick the documented defaults.
func NewServer(blobs Blobs, cacheDir string, limits Limits, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{
		blobs:    blobs,
		cacheDir: cacheDir,
		limits:   limits.effective(),
		logger:   logger,
		served:   map[string]*servedSet{},
	}
}

// Sync ensures the cache holds exactly the given filesets: it extracts
// the missing ones and drops removed or superseded digests. Extraction
// is atomic — temporary directory, completion marker, rename — so a
// partial extraction is invisible and redone on the next Sync. FileSets
// whose extraction fails are reported in the returned (joined) error and
// are not served; the others are.
func (s *Server) Sync(ctx context.Context, sets []FileSet) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if err := os.MkdirAll(s.cacheDir, 0o750); err != nil {
		return fmt.Errorf("fileserve: creating cache directory: %w", err)
	}

	var errs []error
	next := make(map[string]*servedSet, len(sets))
	var order []string
	// wanted maps on-disk name directory → digest directory to keep.
	wanted := map[string]map[string]bool{}

	for _, set := range sets {
		if err := validateSet(set); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := next[set.Name]; dup {
			errs = append(errs, fmt.Errorf("fileserve: duplicate fileset name %q", set.Name))
			continue
		}
		nameDir := sanitizeName(set.Name)
		digestDir := strings.ReplaceAll(set.ManifestDigest, ":", "_")
		target := filepath.Join(s.cacheDir, nameDir, digestDir)

		if !isComplete(target) {
			// The extraction clears whatever sits at target before it
			// renames the new tree over it, and what sits there may be a
			// directory THIS server still holds open — the case is a set
			// whose completion marker went missing under an unchanged
			// digest, which keeps serving from the very tree the
			// re-extraction is about to remove. Unix removes a directory
			// out from under an open handle without complaining; Windows
			// refuses, and the fileset can then never be re-extracted
			// while the instance runs (B-024).
			//
			// The release is scoped to that collision and not to every
			// re-extraction: a set moving to a NEW digest extracts into a
			// different directory, so its current tree is untouched and
			// must keep answering until the new one is in place.
			s.releaseServedDigest(set.Name, set.ManifestDigest)
			if err := s.extract(ctx, set, target); err != nil {
				errs = append(errs, fmt.Errorf("fileserve: fileset %q: %w", set.Name, err))
				continue
			}
		}
		root, err := os.OpenRoot(filepath.Join(target, "rootfs"))
		if err != nil {
			errs = append(errs, fmt.Errorf("fileserve: fileset %q: opening rootfs: %w", set.Name, err))
			continue
		}
		next[set.Name] = &servedSet{set: set, root: root}
		order = append(order, set.Name)
		if wanted[nameDir] == nil {
			wanted[nameDir] = map[string]bool{}
		}
		wanted[nameDir][digestDir] = true
	}

	s.mu.Lock()
	previous := s.served
	s.served = next
	s.order = order
	s.mu.Unlock()
	// In-flight requests open their file under the read lock, so closing
	// the superseded roots here cannot invalidate an open transfer.
	for _, ss := range previous {
		_ = ss.root.Close()
	}

	// A purge failure is reported, not returned. The cache is idempotent:
	// whatever could not be removed now is still unwanted on the next
	// Sync and will be removed then. Folding it into the error would
	// report a Sync that installed everything it was asked to install as
	// a failure — and on Windows that is the normal outcome whenever a
	// client is still streaming a file out of a superseded digest, since
	// an open handle blocks the removal there (B-024).
	if err := s.purge(wanted); err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "fileset cache not fully reclaimed",
			slog.String("error", err.Error()),
			slog.String("action", "the entries will be retried on the next synchronization"))
	}
	return errors.Join(errs...)
}

// releaseServedDigest closes and forgets the root currently serving name,
// but only when it is serving THIS digest — that is, only when the caller
// is about to delete the very directory the root is anchored on (see
// Sync). Any other digest lives elsewhere on disk and keeps serving.
func (s *Server) releaseServedDigest(name, dgst string) {
	s.mu.Lock()
	ss := s.served[name]
	if ss == nil || ss.set.ManifestDigest != dgst {
		s.mu.Unlock()
		return
	}
	delete(s.served, name)
	s.mu.Unlock()
	_ = ss.root.Close()
}

// Close releases every directory handle the server holds and stops
// serving.
//
// It exists because a Server owns one os.Root per served FileSet for its
// whole life, and nothing but a superseding Sync ever closed them. On
// Unix that is invisible — the process exits and the descriptors go with
// it, and a directory can be removed while it is open. On Windows an open
// handle is what stops the cache directory from being deleted at all, so
// an embedder that reconfigures or shuts an instance down had no way to
// let go of it (B-024, NFR-018).
//
// A closed Server serves 404 for everything; Sync may be called again to
// bring it back.
func (s *Server) Close() error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.mu.Lock()
	previous := s.served
	s.served = map[string]*servedSet{}
	s.order = nil
	s.mu.Unlock()

	// In-flight requests hold their own *os.File, opened under the read
	// lock; closing the root they came from does not invalidate them.
	var errs []error
	for name, ss := range previous {
		if err := ss.root.Close(); err != nil {
			errs = append(errs, fmt.Errorf("fileserve: closing the rootfs of %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Enabled returns the currently served FileSets (for the UI/status), in
// Sync order.
func (s *Server) Enabled() []FileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FileSet, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.served[name].set)
	}
	return out
}

// purge removes every cache entry that is not a wanted (name, digest)
// pair: stale temporary directories, disabled filesets, superseded
// digests.
func (s *Server) purge(wanted map[string]map[string]bool) error {
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return fmt.Errorf("fileserve: reading cache directory: %w", err)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		keep := wanted[name]
		if keep == nil || strings.HasPrefix(name, ".tmp-") {
			if err := os.RemoveAll(filepath.Join(s.cacheDir, name)); err != nil {
				errs = append(errs, fmt.Errorf("fileserve: purging %q: %w", name, err))
			}
			continue
		}
		digests, err := os.ReadDir(filepath.Join(s.cacheDir, name))
		if err != nil {
			errs = append(errs, fmt.Errorf("fileserve: reading cache of %q: %w", name, err))
			continue
		}
		for _, d := range digests {
			if keep[d.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(s.cacheDir, name, d.Name())); err != nil {
				errs = append(errs, fmt.Errorf("fileserve: purging %q/%q: %w", name, d.Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// isComplete reports whether target holds a finished extraction: the
// completion marker and the rootfs directory both present.
func isComplete(target string) bool {
	if fi, err := os.Stat(filepath.Join(target, markerFile)); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	fi, err := os.Stat(filepath.Join(target, "rootfs"))
	return err == nil && fi.IsDir()
}

// validateSet rejects FileSets that cannot be addressed or stored
// safely; these are configuration errors, not tar content.
func validateSet(set FileSet) error {
	switch {
	case set.Name == "" || set.Name == "." || set.Name == "..":
		return fmt.Errorf("fileserve: invalid fileset name %q", set.Name)
	case strings.ContainsAny(set.Name, "/\x00"):
		return fmt.Errorf("fileserve: invalid fileset name %q", set.Name)
	case set.Repo == "":
		return fmt.Errorf("fileserve: fileset %q: repository is required", set.Name)
	}
	if _, err := digest.Parse(set.ManifestDigest); err != nil {
		return fmt.Errorf("fileserve: fileset %q: manifest digest: %w", set.Name, err)
	}
	return nil
}

// sanitizeName maps a FileSet name to a safe cache directory segment.
// Names made only of lower-case alphanumerics, dots and dashes are used
// verbatim; anything else becomes "_" plus a truncated content hash —
// underscore is outside the verbatim alphabet, so the two forms cannot
// collide.
//
// The mapping is a pure function of the name and gives the same answer on
// every platform, which is a requirement and not a convenience: a store
// is carried from one machine to another, and a name resolving to one
// directory on Linux and another on Windows would make the destination
// re-extract every fileset it was just handed (NFR-018).
func sanitizeName(name string) string {
	if len(name) <= 100 && safeNamePattern(name) {
		return name
	}
	return "_" + digest.FromString(name).Encoded()[:16]
}

// safeNamePattern reports whether a name can stand as its own directory
// segment on every operating system in the validated scope (NFR-018).
//
// The alphabet is narrower than the characters a filesystem accepts, and
// the extra refusals are all Windows semantics that Linux does not
// reproduce:
//
//   - Upper-case letters, because a Windows volume is case-insensitive:
//     "Docs" and "docs" would share ONE cache directory, so extracting
//     the second would os.RemoveAll the tree of the first, and purge —
//     which matches directory entries against the wanted names — would
//     then reclaim the shared directory out from under whichever set was
//     still being served.
//   - A trailing dot, because Windows strips it: "docs." and "docs"
//     collide for exactly the same reason.
//   - The DOS device names, with or without an extension, because
//     Windows resolves them ahead of any file of that name and
//     os.MkdirAll simply fails on them.
//
// None of these is rejected as a FileSet name: each merely falls through
// to sanitizeName's hashed form, which is a legal segment everywhere.
func safeNamePattern(name string) bool {
	if name == "" || name[0] == '.' || name[0] == '-' || name[len(name)-1] == '.' {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '-':
		default:
			return false
		}
	}
	// Only the part before the first dot decides: "com1.log" is the serial
	// port, not a file. The list is already lower-case because the
	// alphabet above admits nothing else.
	base, _, _ := strings.Cut(name, ".")
	switch base {
	case "con", "prn", "aux", "nul",
		"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
		"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		return false
	}
	return true
}
