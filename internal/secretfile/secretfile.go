// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package secretfile creates files holding secret material with
// permissions that grant nobody but the owning account (NFR-020).
//
// The rule is one sentence and two implementations: mode 0600 on Unix, an
// explicit owner-only ACL on Windows. It needs its own package because Go
// does not give it for free — os.Chmod on Windows only toggles the
// read-only bit, so a 0600 literal there produces a file the whole machine
// can read, and every call site that wrote such a literal was quietly
// wrong on one of the two operating systems in the validated scope
// (NFR-018).
//
// The package deliberately exposes no "mode" parameter. A caller choosing
// permissions per file is a caller that can choose wrong; the only
// question it may answer is whether the content is secret, and if it is,
// this package decides the rest.
package secretfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Mode is the Unix permission of a secret file: owner read/write, nothing
// else. On Windows it is not what enforces anything — the ACL is (see
// harden) — but it is still applied, so a store copied onto a Unix host
// arrives with the right bits.
const Mode os.FileMode = 0o600

// DirMode is the Unix permission of a directory holding secret files.
const DirMode os.FileMode = 0o700

// syncFile flushes a file to the platter. A variable so the refusal path
// has a test: a secret whose bytes never reached the disk must not be
// renamed over the one that works, and a full volume is exactly how that
// happens on the instance nobody is watching.
var syncFile = (*os.File).Sync

// Write replaces path with data, atomically, owner-only from the instant
// the file exists.
//
// The temporary file is created inside the target directory and hardened
// BEFORE anything is written into it: a secret must never exist, even for
// the width of one syscall, under permissions someone else could read it
// through. The rename is what makes the replacement atomic (NFR-010) —
// a crash leaves either the old file or the new one, never a torn one.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("secretfile: creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tobby-secret-*")
	if err != nil {
		return fmt.Errorf("secretfile: creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if err := harden(name); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("secretfile: writing %s: %w", path, err)
	}
	if err := syncFile(tmp); err != nil {
		cleanup()
		return fmt.Errorf("secretfile: flushing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("secretfile: closing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("secretfile: replacing %s: %w", path, err)
	}
	// The rename carries the temporary file's permissions onto the target
	// on both operating systems; hardening again costs one syscall and
	// covers the case where the target pre-existed with looser ones on a
	// platform where rename preserves the destination's ACL.
	return harden(path)
}

// Harden applies owner-only permissions to an existing file. It is the
// entry point for material this package did not write — a temporary file
// another package created, or a secret whose write path predates it.
func Harden(path string) error { return harden(path) }

// MkdirAll creates a directory meant to hold secret files, owner-only.
func MkdirAll(path string) error {
	if err := os.MkdirAll(path, DirMode); err != nil {
		return fmt.Errorf("secretfile: creating %s: %w", path, err)
	}
	return hardenDir(path)
}
