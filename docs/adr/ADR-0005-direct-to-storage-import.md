# ADR-0005 — Direct-to-storage import (no self-push loopback)

## Status

Accepted — 2026-07-11

## Context

When Tobby fetches an ingredient from a remote registry, the result must land
in its embedded local store (ADR-0004). There are two ways to get it there:
speak the OCI Distribution protocol to *itself* over the network, or write
through the embedded library's storage APIs in-process.

Our proof of concept used the loopback approach: after pulling from the
source, it pushed the artifact over HTTPS to its **own** listener, deriving
the target address from the incoming request's `Host` header and disabling
TLS verification (`InsecureSkipVerify`) to tolerate its own self-signed
certificate. Experience with the POC turned up concrete problems:

- **Fragile addressing.** The self-URL was reconstructed from `r.Host`, which
  breaks behind reverse proxies, with multiple listeners, or when the import
  is triggered by a background job with no inbound request at all.
- **Weakened TLS posture.** `InsecureSkipVerify` in the import path is
  exactly the kind of flag that gets copy-pasted beyond its original intent;
  a security-focused tool should not need it to talk to itself.
- **Doubled traffic.** Every blob was read from the source and then
  re-serialized through the local HTTP stack — twice the I/O, twice the
  memory pressure, for zero benefit.
- **An observed correctness bug.** The loopback path re-applied the
  source-registry name prefix on import, producing doubly-prefixed
  repository names in the store.
- **Auth entanglement.** Once the API requires authentication, the importer
  would need service credentials to its own endpoint, with token lifecycles
  and failure modes invented purely to talk to itself.

## Decision

The import pipeline writes **directly into the storage backend**, in-process,
using the embedded distribution library's storage APIs (blob store, manifest
service, tag service). The network listener is for *external* clients only;
no Tobby code path ever opens an HTTP connection to Tobby.

Concretely, an ingredient import:

1. pulls blobs and manifests from the source registry (with digest
   verification on every object);
2. writes blobs through the storage driver's content-addressed blob store,
   which deduplicates by digest natively;
3. puts manifests and tags through the manifest and tag services, under the
   canonical local name (single source-registry prefix, computed in one
   place).

## Consequences

- Import I/O is halved and the double-prefixing class of bug is structurally
  eliminated: naming is computed once, in the engine, not round-tripped
  through a wire protocol.
- `InsecureSkipVerify` disappears from the codebase along with the
  self-connection it excused; TLS configuration now only ever concerns
  *remote* registries.
- Imports work identically whether the HTTP listener is up or not — enabling
  headless/batch operation and cleaner unit testing (storage can be exercised
  without a running server).
- The engine is coupled to the distribution library's storage-layer Go APIs,
  which are less stable than the wire protocol. This is contained by the
  narrow internal registry interface established in ADR-0004; a future
  backend swap reimplements that interface, not the engine.
- In-process writes bypass the HTTP layer's serialization of concurrent
  uploads, so the importer owns concurrency control (per-repository locking,
  upload staging) and must stay compatible with the library's garbage
  collection model — linked blobs and manifest references are created the
  same way the API handlers would.

## Alternatives considered

### Keep the network loopback

Its one real virtue is layering purity: the importer is just another registry
client, and the API remains the single write path (useful for audit
uniformity). Every practical property, however, is negative — the fragility,
doubled traffic, TLS exception, auth entanglement, and observed bug listed in
the context. Audit uniformity is preserved differently: the engine emits the
same structured audit events for direct writes as the API layer does for
external pushes. Rejected.

### Pull-through proxy cache

`distribution` ships a pull-through cache mode: configure an upstream, and
pulls populate the local store on demand. Attractive because it is
off-the-shelf, but it answers a different question: it caches what clients
*happen to pull*, one upstream per registry, and does not cover push flows at
all. Tobby's imports are recipe-driven batch operations across many source
registries, with tag/semver resolution, platform selection, verification, and
per-ingredient status — none of which a passive cache provides. Rejected as
the import mechanism (unaffected: it remains a valid deployment pattern for
registries *upstream* of Tobby).
