// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import "strings"

// Access says who, besides the owning account, can read a file (FR-097).
type Access string

const (
	// AccessOwner is a file only its owner can read.
	AccessOwner Access = "owner"
	// AccessGroup is a file the owning group can also read: what a
	// volume's group ownership (Kubernetes fsGroup) grants. Tolerated.
	AccessGroup Access = "group"
	// AccessBroad is a file every user of the host can read — any
	// permission for "other" on Unix, an ACL entry for Everyone, Users or
	// Authenticated Users on Windows. Refused for a secret entry
	// (TBY-STA-002).
	AccessBroad Access = "broad"
)

// secretFileInDir reports whether a file inside a secret directory holds
// secret material. The certificate beside the key is public by
// definition; only key files are secret.
func secretFileInDir(name string) bool {
	return strings.HasSuffix(name, ".key")
}
