# ADR-0004 — Embedded OCI registry: CNCF distribution v3 as a library

## Status

Accepted — 2026-07-11

## Context

Tobby stores every artifact it handles — images, charts, OCI artifacts,
file sets, and the recipes themselves — in a local OCI store, and exposes
that store over the standard OCI Distribution API (`/v2/`). This is central
to both modes:

- in **passthrough** mode, the local store is the staging area between the
  production registry and a zone registry;
- in **mirror** mode, the local store *is* the payload carried on removable
  media into the air-gapped zone (ADR-0006), and the destination-side Tobby
  instance serves and pushes from it.

Tobby ships as a **single portable binary** that must run on a transportable
workstation with no other services installed. The registry capability
therefore has to live inside the binary. Wire-protocol conformance matters
more than usual: arbitrary third-party clients (`skopeo`, `oras`, `crane`,
`helm`, container runtimes) must be able to talk to Tobby's endpoint, and
subtle deviations from the Distribution specification surface as
hard-to-diagnose interop failures precisely in the environments where
debugging is hardest.

## Decision

Tobby embeds the **CNCF `distribution/distribution` v3** codebase as a Go
library, using its registry handlers for the `/v2/` API and its filesystem
storage driver for the local store.

Rationale:

- It is the **reference implementation** of the OCI Distribution
  specification — the codebase the spec's conformance expectations grew out
  of — which retires the interop risk almost entirely.
- It is **battle-tested** at enormous scale (it powered Docker Hub and
  countless self-hosted registries for a decade).
- The approach is **proven by our proof of concept**, which embedded the same
  library and successfully served standard clients, including multi-platform
  image imports and OCI chart pushes.
- Its storage driver abstraction gives Tobby direct, in-process access to the
  blob and manifest stores — the foundation of the direct-to-storage import
  path (ADR-0005).

The library is wrapped behind a narrow internal interface (serve API, read/
write manifests and blobs, enumerate, delete, garbage-collect) so that the
implementation can be swapped without touching the engine.

## Consequences

- **Accepted cost: a large transitive dependency tree.** `distribution` v3
  pulls in modules Tobby never exercises (Redis client, OpenTelemetry, gRPC,
  cloud storage drivers). This inflates the binary, the SBOM, and — most
  importantly — the CVE surface to be governed. Mitigations, tracked as
  standing practice: dependency pruning where upstream allows, Renovate,
  scheduled rebuilds, and vulnerability scanning of Tobby's own artifacts as
  part of the supply-chain gates (ADR-0011).
- **A measurement spike is scheduled at milestone 0.1**: binary size, memory
  footprint, and dependency/CVE counts are recorded as a baseline. If the
  footprint proves problematic for the transportable-workstation profile,
  the zot alternative below is re-evaluated behind the existing interface.
- Tobby inherits upstream's maintenance cadence and must track v3 releases;
  the internal wrapper keeps that churn out of the engine.
- Registry conformance is essentially free, and the OCI conformance test
  suite runs against Tobby's endpoint in CI to keep it that way.

## Alternatives considered

### zot

[zot](https://zotregistry.dev) is a modern, artifact-first, OCI-native
registry with a smaller footprint and first-class OCI 1.1 referrers support —
on paper the most attractive candidate. However, zot is designed and
maintained as a *standalone server*, not as an embeddable library: its
internal packages are not a supported API surface, and embedding would mean
tracking internals across releases with no compatibility promise. Rejected
for now on embeddability grounds, **explicitly kept as the fallback**: the
milestone 0.1 footprint spike exists to decide whether the `distribution`
dependency cost justifies revisiting this choice.

### Minimal in-house implementation

The subset of the Distribution API Tobby strictly needs (push, pull, tags,
referrers, delete) looks small enough to hand-write, and would yield the
leanest possible binary. Rejected on risk: the specification's hard parts are
in the corners — chunked and cross-repository blob upload, content
negotiation across manifest media types, referrers fallback behavior, digest
validation — and every gap becomes an interop bug against some client we do
not control. Owning a registry implementation is a permanent maintenance tax
unrelated to Tobby's actual value.

### External registry process

Shipping alongside, or depending on, a separate registry (a `registry`
container, or one provisioned by the platform) is operationally standard in
connected datacenters. Rejected because it contradicts the product's defining
constraint: a single self-contained binary that runs on a bare transportable
workstation and whose storage directory is the removable-media payload.
Requiring a second service to install, configure, secure, and version-match
would make the air-gap workflow strictly worse.
