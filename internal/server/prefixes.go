// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package server

import "strings"

// ReservedPrefixes are the path prefixes of the single listener that do not
// belong to the web UI (ADR-0015). The UI owns the root; everything mounted
// here is a machine surface. "/auth/" is reserved ahead of the milestone-6
// OIDC/SAML callbacks; "/static/" serves the embedded UI assets.
//
// The list is part of the product contract: it is documented on /about and
// a collision test keeps UI routes out of it. Entries without a trailing
// slash reserve the exact path; entries with one reserve the whole subtree.
var ReservedPrefixes = []string{
	"/v2/",
	"/api/",
	"/metrics",
	"/healthz",
	"/readyz",
	"/auth/",
	"/static/",
}

// Reserved reports whether path collides with a reserved prefix.
func Reserved(path string) bool {
	for _, p := range ReservedPrefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) || path == strings.TrimSuffix(p, "/") {
				return true
			}
			continue
		}
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
