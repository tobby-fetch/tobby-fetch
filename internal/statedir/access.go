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
	// permission for "other" on Unix, an ACL entry granting a broad
	// principal (Everyone, Users, Authenticated Users…) read or write on
	// Windows. Refused for a secret entry (TBY-STA-002).
	AccessBroad Access = "broad"
	// AccessUnknown is a file whose access list could not be read.
	// Refused for a secret entry like a broad one: the check fails closed.
	AccessUnknown Access = "unknown"
)

// Refused reports whether a secret entry with this access refuses the
// start (TBY-STA-002).
func (a Access) Refused() bool { return a == AccessBroad || a == AccessUnknown }

// SecretFileInDir reports whether a file inside a secret directory holds
// secret material: a private key, whatever the extension it was adopted
// under — .key, .pem (which may hold a key as well as a certificate, and
// is judged as one), PKCS#8 and PKCS#12 containers, or any name that says
// "key". The certificate beside the key (.crt, .cer) is public by
// definition.
func SecretFileInDir(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".key", ".pem", ".p8", ".pk8", ".p12", ".pfx"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return strings.Contains(n, "key")
}
