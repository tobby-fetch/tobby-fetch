// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tobby-fetch/tobby-fetch/internal/secretfile"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
)

// ErrStateHeld reports a state directory another process holds: a running
// instance, or a host command in the middle of writing it (B-082, B-093).
var ErrStateHeld = errors.New("the state directory is held by another process")

// StateLock is the advisory, exclusive lock on a state directory's serving
// marker (statedir.Serving).
//
// A running instance holds it for its whole lifetime, because it keeps
// the accounts in memory and rewrites accounts.yaml from that copy at
// every save (a login included): anything written to the file behind its
// back is reverted. The host commands that write the state take it for the
// duration of their write, and refuse when they cannot.
//
// It is an operating-system lock — flock(2) on Unix, LockFileEx on Windows
// — so it says nothing about how the instance is reached (plain HTTP,
// self-signed or supplied TLS) and it dies with the process that held it:
// a marker a crashed instance left behind holds nobody.
type StateLock struct {
	f *os.File
}

// LockState takes the lock on stateRoot without waiting. A lock another
// process (or another open of the file in this one) holds answers
// ErrStateHeld. The marker file is created if missing and never removed:
// a lock is only meaningful on a path that keeps naming the same file.
func LockState(stateRoot string) (*StateLock, error) {
	if stateRoot == "" {
		return nil, errors.New("auth: state root is required")
	}
	if err := secretfile.MkdirAll(stateRoot); err != nil {
		return nil, fmt.Errorf("auth: creating state directory: %w", err)
	}
	path := statedir.Path(stateRoot, statedir.Serving)
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("auth: opening %s: %w", path, err)
	}
	held, err := tryLock(f)
	if err != nil || held {
		_ = f.Close()
		if held {
			return nil, fmt.Errorf("auth: %s: %w", path, ErrStateHeld)
		}
		return nil, fmt.Errorf("auth: locking %s: %w", path, err)
	}
	return &StateLock{f: f}, nil
}

// StateHeld reports whether another process holds stateRoot's lock. It
// takes the lock and gives it back at once, so it never blocks the holder.
func StateHeld(stateRoot string) (bool, error) {
	l, err := LockState(stateRoot)
	if errors.Is(err, ErrStateHeld) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, l.Release()
}

// Write replaces the marker's content with raw, through the locked handle.
// A reader may observe the file empty for an instant; the lock, not the
// content, is what says an instance is alive.
func (l *StateLock) Write(raw []byte) error {
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := l.f.WriteAt(raw, 0)
	return err
}

// Release empties the marker and gives the lock back.
func (l *StateLock) Release() error {
	_ = l.f.Truncate(0)
	uerr := unlock(l.f)
	cerr := l.f.Close()
	if uerr != nil {
		return uerr
	}
	return cerr
}
