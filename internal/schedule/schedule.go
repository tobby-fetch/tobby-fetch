// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package schedule paces the continuous promotion loop (FR-013): the
// interval at which a passthrough instance re-fetches its Retriever and
// reconciles the destination.
//
// The interval exists in two layers, and the split is the requirement.
// FR-013 asks for a configurable interval AND for it to be changeable
// without redeployment, which the configuration file alone cannot give:
// a value that only comes from the file, the environment, or a flag is
// changeable only by restarting the process. So the configured value is
// the floor an instance starts from, and an operator override — set from
// the UI or the API, persisted in the state directory — wins over it and
// survives a restart. The state directory is deliberate: the override is
// instance identity, and instance identity never travels on the
// transportable store (R-16).
//
// Changing how often an instance reaches into another zone is a sensitive
// configuration change, so every mutation is audited (FR-094). This
// package does not emit the audit record itself: the actor and the
// network origin belong to the caller that authenticated them, and a
// record written here would have to invent both.
package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// overrideFile is the persisted override, inside the state directory.
const overrideFile = "schedule.json"

// MinOverride is the floor an operator-supplied interval may not go
// below. It bounds only the override, never the configured value: the
// configuration file is the operator's own considered writing, while the
// override is a live control on a service that talks to another zone —
// one mistyped field there is a request storm against a peer registry
// that nobody asked for. Zero remains accepted and means "stop the loop".
const MinOverride = time.Minute

// Interval is the effective reconciliation interval: the configured
// value, and the persisted operator override that supersedes it.
//
// Safe for concurrent use: the scheduler reads it on every cycle while
// the API and the UI write it.
type Interval struct {
	path       string
	configured time.Duration

	mu       sync.RWMutex
	override *time.Duration
	// changed is closed — never sent on — whenever the effective value
	// moves, and replaced under the same lock. A waiting scheduler
	// observes the change immediately instead of at the end of a sleep it
	// no longer agrees with, which is what makes "changeable without
	// redeployment" true rather than "changeable within one old period".
	changed chan struct{}
}

// state is the on-disk record. The interval is stored as a duration
// string rather than a count of nanoseconds so that an operator reading
// the state directory sees "30m", not 1800000000000.
type state struct {
	Interval string    `json:"interval"`
	SetAt    time.Time `json:"set_at"`
	Actor    string    `json:"actor,omitempty"`
}

// Open loads the interval for an instance: the configured value, plus any
// override left behind by a previous run.
//
// stateRoot may be empty (an instance running without a state directory,
// which the FR-075 authentication override permits): the interval then
// has nowhere to persist an override, and Set says so rather than
// accepting a change that would evaporate on restart.
//
// An unreadable or malformed override file is a startup error, not a
// silent fallback to the configured value. The whole point of the
// override is that the running cadence is not the one in the
// configuration file; falling back to the file on a parse error would
// change the instance's behaviour at exactly the moment nobody is
// looking.
func Open(stateRoot string, configured time.Duration) (*Interval, error) {
	iv := &Interval{configured: configured, changed: make(chan struct{})}
	if stateRoot == "" {
		return iv, nil
	}
	iv.path = filepath.Join(stateRoot, overrideFile)
	raw, err := os.ReadFile(iv.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return iv, nil
	case err != nil:
		return nil, fmt.Errorf("schedule: reading %s: %w", iv.path, err)
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schedule: %s is not readable as an interval override: %w", iv.path, err)
	}
	d, err := time.ParseDuration(s.Interval)
	if err != nil {
		return nil, fmt.Errorf("schedule: %s holds %q, which is not a duration: %w", iv.path, s.Interval, err)
	}
	if d < 0 {
		return nil, fmt.Errorf("schedule: %s holds the negative interval %q", iv.path, s.Interval)
	}
	iv.override = &d
	return iv, nil
}

// Effective is the interval the loop actually runs at. Zero means the
// periodic reconciliation is off; FR-014's manual trigger is unaffected.
func (i *Interval) Effective() time.Duration {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.override != nil {
		return *i.override
	}
	return i.configured
}

// Configured is the value from the configuration layers (FR-003), kept
// separate so the surfaces can show both: an operator looking at a
// running instance must be able to tell "this is what the file says" from
// "this is what it is doing".
func (i *Interval) Configured() time.Duration { return i.configured }

// Overridden reports whether an operator override is in force.
func (i *Interval) Overridden() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.override != nil
}

// Persistent reports whether this instance can persist an override at
// all — false without a state directory.
func (i *Interval) Persistent() bool { return i.path != "" }

// ErrNoStateDir reports an override attempted on an instance with no
// state directory: there is nowhere for it to survive a restart, and an
// interval that silently reverts on the next start is worse than one that
// refuses to change.
var ErrNoStateDir = errors.New("no state directory is configured: an interval override could not survive a restart")

// ErrTooShort reports an override below MinOverride.
var ErrTooShort = fmt.Errorf("the interval must be 0 (periodic reconciliation off) or at least %s", MinOverride)

// Set records an operator override and wakes the scheduler. actor is
// recorded in the state file for forensics; the audit record itself is
// the caller's (FR-094).
func (i *Interval) Set(d time.Duration, actor string, now time.Time) error {
	if i.path == "" {
		return ErrNoStateDir
	}
	if d != 0 && d < MinOverride {
		return ErrTooShort
	}
	raw, err := json.MarshalIndent(state{Interval: d.String(), SetAt: now.UTC(), Actor: actor}, "", "  ")
	if err != nil {
		return fmt.Errorf("schedule: encoding the interval override: %w", err)
	}
	if err := writeAtomic(i.path, raw); err != nil {
		return err
	}
	i.mu.Lock()
	i.override = &d
	i.wake()
	i.mu.Unlock()
	return nil
}

// Clear removes the override, returning the instance to its configured
// interval. Clearing an absent override succeeds: the caller asked for a
// state, not for a transition.
func (i *Interval) Clear() error {
	if i.path == "" {
		return ErrNoStateDir
	}
	if err := os.Remove(i.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("schedule: removing %s: %w", i.path, err)
	}
	i.mu.Lock()
	i.override = nil
	i.wake()
	i.mu.Unlock()
	return nil
}

// Changed returns a channel closed on the next effective-value change.
// Callers re-read it after every wake-up: the channel is one-shot.
func (i *Interval) Changed() <-chan struct{} {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.changed
}

// wake closes the current change channel and installs a fresh one.
// Callers hold i.mu for writing.
func (i *Interval) wake() {
	close(i.changed)
	i.changed = make(chan struct{})
}

// writeAtomic replaces path's content without ever leaving a truncated
// file behind: a half-written override is a duration that parses to
// something nobody chose.
func writeAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("schedule: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("schedule: replacing %s: %w", path, err)
	}
	return nil
}
