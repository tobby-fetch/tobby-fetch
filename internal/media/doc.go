// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package media is the removable-media transport of milestone 5: it writes
// the media manifest at the end of a mirror synchronization and verifies a
// transported store on the destination side (FR-050, FR-052, FR-054,
// ADR-0006).
//
// # What the manifest is, and what it is not
//
// The manifest (meta/media.json) is an inventory: every covered file with
// its size and digest, the recipes the medium delivers with their pinned
// ingredients, the zone it is addressed to, the identity of the medium,
// and the two format versions needed to read it. It is written atomically
// and it is deliberately UNSIGNED — Tobby signs nothing (project decision
// no. 10, ADR-0006/ADR-0007). It is an integrity and completeness aid that
// makes a failure fast and precisely localized; it is not a trust anchor.
//
// Authenticity comes from somewhere else entirely: the cosign signatures
// of the recipes, verified against the trust roots of the DESTINATION
// instance. Trust roots present on the medium are ignored (FR-054), which
// is why nothing in this package reads key material from the store it
// verifies.
//
// # Granularity of a refusal (FR-054 amendment R-19)
//
// Four conditions block a medium as a whole, because none of them leaves
// anything to reason about or anyone to reason for:
//
//  1. no manifest, an unreadable manifest, or an unsupported format
//     version — no inventory, no verdict; no override;
//  2. a recipe graph (meta/recipes.json) that does not match its inventory
//     entry — the graph IS the reachability set; no override;
//  3. a zone identity other than this instance's — the medium is addressed
//     to someone else; admin-overridable, audited (FR-094);
//  4. a resolution timestamp older than the last import recorded for the
//     zone (R-28) — an anti-accident guard, admin-overridable, audited.
//
// Everything else is decided recipe by recipe. A recipe is pushable when
// its signature verifies against the destination's trust roots and when
// every file it reaches is present, matches its inventory entry, and — for
// blobs — hashes to the digest its own path claims. Otherwise that recipe
// is blocked whole, with no override, named in the report together with
// the file that failed. Its neighbours are unaffected: carrying several
// deliveries on one medium is the point.
//
// Files under coverage that the inventory does not list, and inventoried
// files no recipe reaches, are reported as findings and never pushed. They
// block nothing.
//
// # Coverage
//
// The manifest covers every regular file under docker/registry/v2/ (the
// content) and under meta/ (the bookkeeping), except meta/media.json
// itself. Everything under _tobby/ — tasks, operation logs — is outside
// coverage on purpose: it is where the destination side writes its return
// logs (FR-053, FR-054), and an inventory that covered it would be stale
// the moment it was written.
//
// # Untrusted input
//
// A manifest read on this side came from a foreign medium. Its inventory
// paths are attacker-controlled data (NFR-011): they are validated against
// the covered roots before use, they may not contain "..", a leading
// separator, a backslash, or a colon, and every read goes through an
// os.Root anchored at the store — so neither a crafted path nor a symlink
// planted on the medium can make Tobby read outside the directory it was
// handed.
package media
