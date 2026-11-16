// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package diag is the vocabulary of the diagnostic bundle (FR-095, roadmap
// R-30, milestone 7): one local archive an operator hands to support.
//
// The bundle is local only: written to disk or downloaded by the
// administrator who asked for it, never sent anywhere (NFR-019).
// Redaction fails closed: before an archive is released, every member is
// searched for the secret values the instance knows, and a match aborts
// it (TBY-DIA-002) rather than ship it. Producing one is an audit event
// (diagnostics.bundle).
//
// This package carries the archive's layout and manifest only, reserved at
// the opening of the milestone. Collecting each member, the in-memory log
// window (diagnostics.logRecords), the redaction check and the surfaces —
// `tobby diagnostics bundle`, the admin screen and its API mirror — are
// track B.
package diag

import (
	"fmt"
	"strings"
)

// The members of a bundle, at fixed names so a support engineer finds the
// same thing in every archive. Paths are slash-separated, relative to the
// archive root.
const (
	MemberManifest  = "MANIFEST.json"        // this list, with digests
	MemberVersion   = "version.json"         // version, commit, build date, platform
	MemberConfig    = "config.yaml"          // effective configuration, redacted (FR-003)
	MemberLogs      = "logs.jsonl"           // the in-memory window of recent log records
	MemberMediaLog  = "media-log.jsonl"      // mirror mode: tail of the medium's log (FR-053)
	MemberTasks     = "tasks/"               // the most recent task reports, one file each
	MemberMetrics   = "metrics.txt"          // an OpenMetrics snapshot (FR-091)
	MemberIntegrity = "integrity.json"       // the last on-demand verification report (FR-057)
	MemberState     = "state-inventory.json" // the state-directory inventory (FR-097) — never contents
)

// Members lists every member name, in archive order. MemberTasks is a
// directory prefix; the others are files.
func Members() []string {
	return []string{
		MemberManifest, MemberVersion, MemberConfig, MemberLogs, MemberMediaLog,
		MemberTasks, MemberMetrics, MemberIntegrity, MemberState,
	}
}

// Known reports whether name is a member of the documented layout — a file
// member, or a file under MemberTasks.
func Known(name string) bool {
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, m := range Members() {
		if name == m || (m == MemberTasks && len(name) > len(m) && strings.HasPrefix(name, m)) {
			return true
		}
	}
	return false
}

// Manifest is MemberManifest: what the archive holds.
type Manifest struct {
	// Format versions the bundle layout.
	Format  int      `json:"format"`
	Members []Member `json:"members"`
}

// Member is one file of the archive.
type Member struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// FormatVersion is the bundle layout version this build writes.
const FormatVersion = 1

// Validate checks that a manifest only lists documented members, each
// once — what a reader of the archive relies on.
func (m Manifest) Validate() error {
	if m.Format != FormatVersion {
		return fmt.Errorf("diag: bundle format %d, this build reads %d", m.Format, FormatVersion)
	}
	seen := map[string]bool{}
	for _, mem := range m.Members {
		if !Known(mem.Name) || mem.Name == MemberTasks {
			return fmt.Errorf("diag: %q is not a documented bundle member", mem.Name)
		}
		if seen[mem.Name] {
			return fmt.Errorf("diag: %q is listed twice", mem.Name)
		}
		seen[mem.Name] = true
	}
	return nil
}
