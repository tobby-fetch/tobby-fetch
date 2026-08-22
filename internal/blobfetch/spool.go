// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package blobfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// spool is one blob's partial download: the received bytes and the
// bookkeeping that lets a later attempt pick them up.
//
// It lives under <state.root>/partials/, and that placement is a
// requirement rather than a convenience (R-16). The store is
// transportable: it is copied onto physical media and carried across a
// zone boundary. A half-received blob is not content — it is this
// instance's progress on this link — so it must not board the media, must
// not survive a store copy, and must not be something a garbage collector
// on the far side has to reason about.
//
// Layout, content-addressed so a partial is valid whatever operation
// resumes it (a task retried, a task requeued after a restart, a
// synchronization that wants the same layer for a second ingredient):
//
//	<state.root>/partials/<algorithm>/<hex>.part   the received bytes
//	<state.root>/partials/<algorithm>/<hex>.json   offset and validator
//
// The recorded offset is the authority, never the file length. Bytes may
// reach the file without reaching the disk, so a crash can leave a .part
// longer than what was durably written; the file is truncated back to the
// recorded offset on open, which makes the two agree by construction.
type spool struct {
	dgst     digest.Digest
	partPath string
	metaPath string
	f        *os.File

	meta     spoolMeta
	verifier digest.Verifier
	// hashed is how many bytes the verifier has consumed. It tracks the
	// offset and exists only to make the mismatch visible in tests.
	hashed int64
	// resumed records that this spool started from bytes an earlier
	// attempt left, which the UI reports: an operator watching a 4 GB
	// layer needs to know it did not start over.
	resumed bool
}

// spoolMeta is the sidecar. It is small, rewritten atomically, and
// deliberately does not carry the digest algorithm or the repository:
// those are in the path and in the caller's request, and duplicating them
// would create two answers to one question.
type spoolMeta struct {
	// Offset is the number of bytes durably written to the .part file.
	Offset int64 `json:"offset"`
	// Validator is the origin's ETag (or Last-Modified) observed when the
	// download started, replayed as If-Range and compared on resume.
	Validator string `json:"validator,omitempty"`
	// Updated timestamps the last checkpoint, so an operator inspecting
	// the state directory can tell a live transfer from an abandoned one.
	Updated time.Time `json:"updated"`
}

// openSpool opens (or creates) the spool of one digest.
func openSpool(root string, dgst digest.Digest) (*spool, error) {
	if root == "" {
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("blobfetch: no spool directory configured"))
	}
	dir := filepath.Join(root, dgst.Algorithm().String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, spoolErr(dir, err)
	}
	s := &spool{
		dgst:     dgst,
		partPath: filepath.Join(dir, dgst.Encoded()+".part"),
		metaPath: filepath.Join(dir, dgst.Encoded()+".json"),
	}
	if raw, err := os.ReadFile(s.metaPath); err == nil {
		_ = json.Unmarshal(raw, &s.meta) // a corrupt sidecar simply means "start over"
	}
	f, err := os.OpenFile(s.partPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, spoolErr(s.partPath, err)
	}
	s.f = f
	// The recorded offset wins over the file length, both ways: a longer
	// file is a crash-truncated tail, a shorter one is a sidecar that
	// outlived its data.
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, spoolErr(s.partPath, err)
	}
	if s.meta.Offset > info.Size() {
		s.meta.Offset = info.Size()
	}
	if info.Size() > s.meta.Offset {
		if err := f.Truncate(s.meta.Offset); err != nil {
			_ = f.Close()
			return nil, spoolErr(s.partPath, err)
		}
	}
	s.resumed = s.meta.Offset > 0
	return s, nil
}

// rehash restores the running digest over the bytes already spooled, by
// reading them back.
//
// Re-reading rather than persisting a marshaled hash state is a deliberate
// trade: at local disk speed the prefix costs seconds where the network it
// replaces costs minutes, and it removes an on-disk format that would have
// to be versioned, validated, and distrusted after a crash. The digest is
// therefore always computed over the bytes that are actually on disk —
// which is exactly the property the integrity check needs.
func (s *spool) rehash() error {
	v := s.dgst.Verifier()
	s.verifier, s.hashed = v, 0
	if s.meta.Offset == 0 {
		return nil
	}
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return spoolErr(s.partPath, err)
	}
	n, err := io.CopyN(v, s.f, s.meta.Offset)
	s.hashed = n
	if err != nil {
		return spoolErr(s.partPath, err)
	}
	return nil
}

// offset is the number of durably received bytes.
func (s *spool) offset() int64 { return s.meta.Offset }

// validator is the origin's cache validator from the first attempt.
func (s *spool) validator() string { return s.meta.Validator }

// setValidator records the validator of a download that just started.
func (s *spool) setValidator(v string) { s.meta.Validator = v }

// reset empties the spool and the running digest: the transfer restarts
// from byte zero. Used when the origin ignores Range and sends the whole
// body, and when a validator moved.
func (s *spool) reset() error {
	if err := s.f.Truncate(0); err != nil {
		return spoolErr(s.partPath, err)
	}
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return spoolErr(s.partPath, err)
	}
	s.meta.Offset, s.meta.Validator = 0, ""
	s.resumed = false
	// A verifier cannot be rewound, so a reset needs a new one.
	s.verifier, s.hashed = s.dgst.Verifier(), 0
	return s.checkpoint()
}

// append streams the response body onto the spool, hashing as it goes,
// checkpointing periodically, and reporting progress.
func (s *spool) append(ctx context.Context, body io.Reader, total int64, report Progress) error {
	if _, err := s.f.Seek(s.meta.Offset, io.SeekStart); err != nil {
		return spoolErr(s.partPath, err)
	}
	// Starting anywhere but zero IS the resume, whether the bytes come
	// from an earlier attempt in this call or from an earlier instance
	// lifetime. The distinction matters to no one watching the screen.
	if s.meta.Offset > 0 {
		s.resumed = true
	}
	buf := make([]byte, 256<<10)
	sinceCheckpoint := int64(0)
	lastReport := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			_ = s.checkpoint()
			return err
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := s.f.Write(buf[:n]); werr != nil {
				return spoolErr(s.partPath, werr)
			}
			if _, herr := s.verifier.Write(buf[:n]); herr != nil {
				return spoolErr(s.partPath, herr)
			}
			s.meta.Offset += int64(n)
			s.hashed += int64(n)
			sinceCheckpoint += int64(n)
			if sinceCheckpoint >= checkpoint {
				if err := s.checkpoint(); err != nil {
					return err
				}
				sinceCheckpoint = 0
			}
			if report != nil && time.Since(lastReport) >= progressEvery {
				report(s.meta.Offset, total, s.resumed)
				lastReport = time.Now()
			}
		}
		if rerr != nil {
			if err := s.checkpoint(); err != nil {
				return err
			}
			if report != nil {
				report(s.meta.Offset, total, s.resumed)
			}
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			// A cut mid-stream is the ordinary case this whole package
			// exists for: the offset is checkpointed, so the next attempt
			// asks for the rest.
			return taxonomy.New(taxonomy.CodeRegistryUnreachable, taxonomy.Params{
				"host": "the source registry",
			}).WithCause(rerr)
		}
	}
}

// checkpoint makes the received bytes durable and records the offset they
// reached. The order matters and is the whole crash-safety argument: the
// data is synced BEFORE the sidecar claims it, so a crash between the two
// loses bytes rather than inventing them.
func (s *spool) checkpoint() error {
	if err := s.f.Sync(); err != nil {
		return spoolErr(s.partPath, err)
	}
	s.meta.Updated = time.Now().UTC()
	raw, err := json.Marshal(s.meta)
	if err != nil {
		return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	tmp := s.metaPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return spoolErr(tmp, err)
	}
	if err := os.Rename(tmp, s.metaPath); err != nil {
		return spoolErr(s.metaPath, err)
	}
	return nil
}

// verified reports whether the spooled bytes hash to the expected digest.
func (s *spool) verified() bool {
	return s.verifier != nil && s.hashed == s.meta.Offset && s.verifier.Verified()
}

// actual renders what the spool actually holds, for the mismatch message.
// The verifier does not expose the computed digest, so the honest answer
// is the length — which is what an operator can act on anyway.
func (s *spool) actual() string {
	return fmt.Sprintf("%d bytes that do not hash to it", s.meta.Offset)
}

// reader hands the verified bytes to the store: closing the reader after
// a COMPLETE read removes the partial file and its sidecar, so a
// completed transfer leaves nothing behind in the state directory; a
// reader abandoned early keeps them (see Close).
func (s *spool) reader(release func()) (io.ReadCloser, error) {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, spoolErr(s.partPath, err)
	}
	return &spoolReader{s: s, release: release}, nil
}

type spoolReader struct {
	s       *spool
	release func()
	// drained records that the consumer read through EOF — the signal
	// that separates "the store write completed its copy" from "the
	// store write failed mid-blob and abandoned the reader".
	drained bool
}

func (r *spoolReader) Read(p []byte) (int, error) {
	n, err := r.s.f.Read(p)
	if errors.Is(err, io.EOF) {
		r.drained = true
	}
	return n, err
}

// Close finishes the hand-off, and only then releases the per-digest
// lock: a caller waiting on the same blob must never observe the file
// between "another reader still has it" and "it is gone".
//
// The spool is destroyed only when the consumer drained it (v0.4.2 fix):
// the store's io.Copy reads through EOF exactly when its own writes all
// succeeded, while a write failure — disk full, store fault — abandons
// the reader mid-blob. These VERIFIED bytes used to be discarded on that
// failure, which forced a full re-download for a store-side problem; in
// the constrained zones this tool exists for, re-downloadable never
// means cheap (FR-029 is the same argument one layer down). Kept, the
// spool makes the retry free: the next Open finds it complete, verifies
// it, and offers it again without one network byte.
//
// The one corner this signal cannot see: a store that drains the reader
// and THEN fails to commit (a rename error) still costs a re-download.
// Accepted — telling those apart needs an explicit keep/discard contract
// on every call site, for a failure mode where the state disk is usually
// in trouble too.
func (r *spoolReader) Close() error {
	var err error
	if r.drained {
		err = r.s.discard()
	} else {
		err = r.s.close()
	}
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return err
}

// discard closes and removes the spool.
func (s *spool) discard() error {
	err := s.close()
	_ = os.Remove(s.partPath)
	_ = os.Remove(s.metaPath)
	return err
}

// close releases the file handle, leaving the partial content in place for
// a later attempt.
func (s *spool) close() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// spoolErr taxonomizes a state-directory failure. It is its own code
// rather than CodeStoreWrite: the two directories have different owners,
// different sizing, and different corrective actions, and telling an
// operator to check the store when the state disk is full sends them to
// the wrong machine.
func spoolErr(path string, err error) error {
	return taxonomy.New(taxonomy.CodeResumeSpool, taxonomy.Params{
		"path":   path,
		"detail": err.Error(),
	}).WithCause(err)
}
