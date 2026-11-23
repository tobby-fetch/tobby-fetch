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
// The entries are named here and only here: every package that writes one
// takes its path from Path and the constants below, and a test fails when
// a writer joins a literal that is a documented name instead. Prepare is
// the startup check (layout version, permissions of secret entries —
// TBY-STA-001..003), Inspect the inventory behind `tobby state inspect`,
// the API mirror and the diagnostic bundle.
package statedir

import (
	"errors"
	"fmt"
	"path/filepath"
)

// The documented names, relative to state.root.
const (
	// Accounts is the local account database.
	Accounts = "accounts.yaml"
	// TLS is the directory of the generated or adopted server certificate.
	TLS = "tls"
	// Schedule is the runtime override of the refresh interval.
	Schedule = "schedule.json"
	// MediaImports is the per-zone import record.
	MediaImports = "media-imports.json"
	// TrustCache holds trust roots fetched from HTTPS URLs.
	TrustCache = "trust-cache"
	// Partials is the spool of resumable partial downloads.
	Partials = "partials"
	// Scanner is the vulnerability scanner's private work directory.
	Scanner = "scanner"
	// Serving is the runtime marker of the instance serving the store.
	Serving = "serving.json"
)

// Path returns the absolute location of a documented entry (or of a file
// inside a documented directory) under root.
func Path(root string, elem ...string) string {
	return filepath.Join(append([]string{root}, elem...)...)
}

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
	// private key: created mode 0600, owner-only ACL on Windows (NFR-020);
	// access for every user of the host refuses the start (TBY-STA-002),
	// group access granted by the volume (fsGroup) is tolerated.
	Secret bool `json:"secret"`
	// Purpose says what it holds, and the requirement it serves.
	Purpose string `json:"purpose"`
}

// entries is the layout of version LayoutVersion, in documentation order.
var entries = []Entry{
	{Name: LayoutFile, Kind: KindFile, Class: ClassIdentity, Purpose: "layout version of this state directory (FR-097)"},
	{Name: Accounts, Kind: KindFile, Class: ClassIdentity, Secret: true, Purpose: "local accounts with their password hashes and static tokens as hashes (FR-072, FR-073)"},
	{Name: TLS, Kind: KindDir, Class: ClassIdentity, Secret: true, Purpose: "the generated or adopted server certificate and its private key (FR-082)"},
	{Name: Schedule, Kind: KindFile, Class: ClassIdentity, Purpose: "the passthrough refresh interval set at runtime by an operator (FR-013)"},
	{Name: MediaImports, Kind: KindFile, Class: ClassIdentity, Purpose: "per zone, the resolution time and identifier of the last medium imported (FR-052, R-28)"},
	{Name: TrustCache, Kind: KindDir, Class: ClassDiscardable, Purpose: "trust roots fetched from HTTPS URLs at configuration time (FR-033); fetched again when missing"},
	{Name: Partials, Kind: KindDir, Class: ClassDiscardable, Purpose: "resumable partial downloads (FR-029); a missing spool restarts the blob at zero"},
	{Name: Scanner, Kind: KindDir, Class: ClassDiscardable, Purpose: "the scanner's private work directory: unpacked binary, database, scratch (FR-031, FR-032)"},
	{Name: Serving, Kind: KindFile, Class: ClassDiscardable, Purpose: "runtime marker of the instance serving the store, for the local CLI (FR-066)"},
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
