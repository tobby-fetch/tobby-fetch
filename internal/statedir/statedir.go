// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package statedir is the normative layout of the instance state directory
// (FR-097, roadmap R-27, milestone 7): every entry Tobby writes under
// state.root, what it is for, whether it is secret, and whether a backup
// must keep it.
//
// The state directory is what makes an instance THIS instance without
// being content: accounts, token hashes, the server certificate, the
// operator's runtime overrides, the per-zone import record. Together with
// the configuration file and the secret files it references, it is the
// single backup target of an instance besides its store (FR-050), and it
// never lives inside the store (NFR-020).
//
// This package carries the layout only, reserved at the opening of
// milestone 7. The entries are still named, today, by the packages that
// write them (auth, netx, schedule, media, blobfetch, scan/trivy, cli);
// track B of the milestone makes those packages take their names from
// here, adds the startup check (layout version, permissions of secret
// entries — TBY-STA-001..003) and `tobby state inspect`. A test keeps the
// table and the writers in step from then on.
package statedir

import (
	"errors"
	"fmt"
)

// LayoutVersion is the version of the layout described by Entries. It is
// written to LayoutFile by the build that first creates or adopts a state
// directory; a version this build does not know refuses the start
// (TBY-STA-001). Bumped only by an incompatible change, which the 1.x
// promise excludes: entries may be added, never renamed or repurposed.
const LayoutVersion = 1

// LayoutFile records the layout version of a state directory.
const LayoutFile = "layout.json"

// Class says what a backup must do with an entry.
type Class string

const (
	// ClassIdentity entries make the instance what it is; a rebuild needs
	// them, and a missing one is reported (TBY-STA-003).
	ClassIdentity Class = "identity"
	// ClassDiscardable entries are caches, spools and runtime markers:
	// rebuilt on demand, never worth backing up, recreated silently.
	ClassDiscardable Class = "discardable"
)

// Kind is the file-system shape of an entry.
type Kind string

const (
	// KindFile is a regular file.
	KindFile Kind = "file"
	// KindDir is a directory.
	KindDir Kind = "dir"
)

// Entry is one documented entry of the state directory.
type Entry struct {
	// Name is the path relative to state.root, slash-separated.
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Class says whether a backup keeps it.
	Class Class `json:"class"`
	// Secret entries hold credential material (hashes included) or a
	// private key: mode 0600, owner-only ACL on Windows (NFR-020), and a
	// broader mode refuses the start (TBY-STA-002).
	Secret bool `json:"secret"`
	// Purpose says what it holds, and the requirement it serves.
	Purpose string `json:"purpose"`
}

// entries is the layout of version LayoutVersion, in documentation order.
var entries = []Entry{
	{Name: LayoutFile, Kind: KindFile, Class: ClassIdentity, Purpose: "layout version of this state directory (FR-097)"},
	{Name: "accounts.yaml", Kind: KindFile, Class: ClassIdentity, Secret: true, Purpose: "local accounts with their password hashes and static tokens as hashes (FR-072, FR-073)"},
	{Name: "tls", Kind: KindDir, Class: ClassIdentity, Secret: true, Purpose: "the generated or adopted server certificate and its private key (FR-082)"},
	{Name: "schedule.json", Kind: KindFile, Class: ClassIdentity, Purpose: "the passthrough refresh interval set at runtime by an operator (FR-013)"},
	{Name: "media-imports.json", Kind: KindFile, Class: ClassIdentity, Purpose: "per zone, the resolution time and identifier of the last medium imported (FR-052, R-28)"},
	{Name: "trust-cache", Kind: KindDir, Class: ClassDiscardable, Purpose: "trust roots fetched from HTTPS URLs at configuration time (FR-033); fetched again when missing"},
	{Name: "partials", Kind: KindDir, Class: ClassDiscardable, Purpose: "resumable partial downloads (FR-029); a missing spool restarts the blob at zero"},
	{Name: "scanner", Kind: KindDir, Class: ClassDiscardable, Purpose: "the scanner's private work directory: unpacked binary, database, scratch (FR-031, FR-032)"},
	{Name: "serving.json", Kind: KindFile, Class: ClassDiscardable, Purpose: "runtime marker of the instance serving the store, for the local CLI (FR-066)"},
}

// Entries returns the documented layout, in documentation order. The
// slice is a copy: the layout is a contract, not a variable.
func Entries() []Entry {
	return append([]Entry(nil), entries...)
}

// Lookup returns the documented entry named name.
func Lookup(name string) (Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Check reports what makes the layout table itself inconsistent: a
// duplicate or empty name, an unknown class or kind, a secret entry that a
// backup would discard (a secret is always identity — there is no secret
// cache), or an entry without a purpose. It returns nil for the table this
// build ships, which a test asserts.
func Check(list []Entry) error {
	seen := map[string]bool{}
	for _, e := range list {
		switch {
		case e.Name == "":
			return errors.New("statedir: an entry has no name")
		case seen[e.Name]:
			return fmt.Errorf("statedir: %s is listed twice", e.Name)
		case e.Class != ClassIdentity && e.Class != ClassDiscardable:
			return fmt.Errorf("statedir: %s has unknown class %q", e.Name, e.Class)
		case e.Kind != KindFile && e.Kind != KindDir:
			return fmt.Errorf("statedir: %s has unknown kind %q", e.Name, e.Kind)
		case e.Secret && e.Class != ClassIdentity:
			return fmt.Errorf("statedir: %s is secret but discardable: a backup must keep every secret entry or the rebuild loses it", e.Name)
		case e.Purpose == "":
			return fmt.Errorf("statedir: %s has no documented purpose", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}
