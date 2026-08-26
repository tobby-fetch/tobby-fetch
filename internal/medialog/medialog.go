// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package medialog is the operation log written onto a transport medium
// (FR-053, FR-056, ADR-0012).
//
// # What it is for
//
// A medium changes hands. The source side records what it put on it; the
// destination side records what it did with it — the return audit
// channel of FR-052. Both write here, in the same JSON schema as every
// other Tobby log (FR-090), so that whoever holds the medium next can
// read the whole history of the object they are holding without access to
// either instance.
//
// # Where it may live, and where it may not
//
// Outside the media manifest's coverage, always. The manifest inventories
// paths, sizes and digests; a log file inside coverage would invalidate
// the very inventory it accompanies, one line at a time, and the medium
// would fail its own checksum the moment anyone wrote to it. The
// coverage rule is not restated here: Open asks media.Covered, which is
// the single definition the writer and the verifier already share, and
// refuses a configured path that falls inside it (FR-054).
//
// # Durability
//
// The file is written with plain, unbuffered writes and fsync'd at task
// boundaries (FR-056): a medium yanked or a process killed loses at most
// the entries of the task in progress. Rotation is by size, so a long
// campaign of imports cannot fill the medium it is trying to deliver.
//
// The writer is an io.Writer, not a slog.Handler: the logging package
// already knows how to render Tobby's record schema onto any writer
// (logging.New), and a second handler would be a second schema waiting to
// drift from the first.
package medialog

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
)

// DefaultPath is where the operation log lands when nothing is
// configured: under the medium's own area, outside manifest coverage.
const DefaultPath = media.LogsDir + "/operations.log"

// Default rotation budget. Ten megabytes over three generations bounds
// the log at thirty on a medium whose whole point is to carry gigabytes
// of content — small enough never to matter, large enough to hold the
// full narration of several campaigns.
const (
	DefaultMaxBytes int64 = 10 << 20
	DefaultKeep           = 3
)

// Options parameterizes one media log.
type Options struct {
	// Path is the log file relative to the store root, in slash form.
	// Empty means DefaultPath. It MUST fall outside manifest coverage;
	// Open refuses it otherwise rather than corrupting the inventory the
	// destination side is about to verify.
	Path string
	// MaxBytes is the rotation threshold. Zero means DefaultMaxBytes; a
	// negative value disables rotation, which is a deliberate choice and
	// not a default.
	MaxBytes int64
	// Keep is how many rotated generations to retain. Zero means
	// DefaultKeep.
	Keep int
}

// Writer is the append-only operation log on a medium.
//
// Safe for concurrent use: slog handlers write from whichever goroutine
// logs, and a rotation must not tear a record in half.
type Writer struct {
	path     string
	rel      string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open creates or reopens the operation log inside storeRoot.
//
// An unwritable medium is an error, not a degradation: FR-053 makes the
// log part of what the medium carries, and an instance that silently
// stopped writing it would deliver a medium nobody can audit.
func Open(storeRoot string, opts Options) (*Writer, error) {
	rel := opts.Path
	if rel == "" {
		rel = DefaultPath
	}
	if err := checkPath(rel); err != nil {
		return nil, err
	}
	w := &Writer{
		path:     filepath.Join(storeRoot, filepath.FromSlash(rel)),
		rel:      rel,
		maxBytes: opts.MaxBytes,
		keep:     opts.Keep,
	}
	if w.maxBytes == 0 {
		w.maxBytes = DefaultMaxBytes
	}
	if w.keep <= 0 {
		w.keep = DefaultKeep
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o750); err != nil {
		return nil, fmt.Errorf("medialog: creating %s: %w", filepath.Dir(w.path), err)
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// checkPath refuses a configured path that would put the log where it
// does not belong. Two refusals, both about the same thing: a path
// escaping the store writes outside the medium entirely, and a path
// inside manifest coverage invalidates the inventory (FR-054).
func checkPath(rel string) error {
	switch {
	case strings.ContainsRune(rel, '\\'):
		return fmt.Errorf("medialog: %q uses a backslash: the media log path is slash-separated on every platform (NFR-018)", rel)
	case path.IsAbs(rel):
		return fmt.Errorf("medialog: %q is absolute: the media log lives inside the transported store (FR-053)", rel)
	case path.Clean(rel) != rel:
		return fmt.Errorf("medialog: %q is not in clean relative form", rel)
	case strings.HasPrefix(rel, "../"):
		return fmt.Errorf("medialog: %q leaves the transported store", rel)
	}
	if media.Covered(rel) {
		return fmt.Errorf("medialog: %q lies inside the media manifest's coverage: "+
			"writing there would invalidate the inventory the destination verifies (FR-054); "+
			"put the log under %s/", rel, media.TobbyDir)
	}
	return nil
}

// open attaches to the log file and learns its current size.
func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // G302,G304: operation log under the store the operator named
	if err != nil {
		return fmt.Errorf("medialog: opening %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("medialog: sizing %s: %w", w.path, err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

// Path reports the log file's location on the medium, for the startup
// line that tells an operator where to look.
func (w *Writer) Path() string { return w.path }

// Write appends one record, rotating first when the record would take the
// file past its budget.
//
// The rotation decision is taken BEFORE the write and on the whole
// record: splitting a JSON line across two generations would produce two
// unparseable halves, and a log an operator cannot parse is a log they
// cannot audit.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, fmt.Errorf("medialog: %s is closed", w.rel)
	}
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Sync flushes the medium's log to stable storage (FR-056).
//
// Called at task boundaries, which is the granularity the requirement
// fixes: an fsync per record would make every log line a disk barrier on
// a device that is often a USB stick, and an fsync only at close would
// lose everything a yanked medium had not flushed. A closed writer syncs
// nothing and says so is not an error — the boundary hook fires on a
// shutting-down instance too.
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	return w.f.Sync()
}

// Close syncs and releases the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	syncErr := f.Sync()
	if err := f.Close(); err != nil {
		return err
	}
	return syncErr
}

// rotate shifts the generations and starts a fresh file. Callers hold
// w.mu.
//
// The file is closed before it is renamed and reopened afterwards, rather
// than renamed underneath an open handle: renaming an open file fails on
// Windows (NFR-018), and the mirror flow is a release criterion there.
func (w *Writer) rotate() error {
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("medialog: flushing %s before rotation: %w", w.path, err)
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("medialog: closing %s before rotation: %w", w.path, err)
	}
	w.f = nil
	// Oldest first, so no generation overwrites one that has not moved yet.
	if err := os.Remove(w.generation(w.keep)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("medialog: dropping %s: %w", w.generation(w.keep), err)
	}
	for i := w.keep - 1; i >= 1; i-- {
		if err := os.Rename(w.generation(i), w.generation(i+1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("medialog: rotating %s: %w", w.generation(i), err)
		}
	}
	if err := os.Rename(w.path, w.generation(1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("medialog: rotating %s: %w", w.path, err)
	}
	w.size = 0
	return w.open()
}

// generation is the path of the nth rotated file ("operations.log.1").
func (w *Writer) generation(n int) string {
	return fmt.Sprintf("%s.%d", w.path, n)
}
