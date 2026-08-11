# Spike — Embedded registry library footprint (milestone 1)

> Decision record for the measurement spike scheduled by ADR-0004 and the
> v0.1.x plan: quantify the cost of embedding CNCF
> `distribution/distribution` v3 as a library, and take a go/no-go against
> the fallback options (zot, minimal in-house implementation).

**Date:** 2026-08-12 · **Toolchain:** go1.25.6 · **Library:** `distribution/distribution` v3.1.1

## Measurements

Static builds, `-trimpath -ldflags "-s -w"`, darwin/arm64 (linux sizes are
within a few percent):

| Build | Size |
|---|---|
| Application socle alone (config, logging, probes, metrics, CLI) | 7.9 MB |
| Socle + embedded registry (handlers + filesystem driver) | 22.6 MB |
| **Attributable to the registry capability** | **≈ 14.7 MB** |

| Metric | Value |
|---|---|
| Go modules actually linked into the binary | 64 |
| `distribution/*` packages linked | 37 |
| Resident set at idle, registry mounted (darwin/arm64) | ≈ 20 MB |
| Known CVEs in the dependency set (Trivy, 2026-08-12) | 0 after one transitive bump¹ |

¹ The scan surfaced one HIGH in `google.golang.org/grpc` v1.80.0 (pulled
transitively through the library's OpenTelemetry plumbing — a code path
Tobby never exercises). Bumping to v1.82.1 cleared it the same day. This is
exactly the class of maintenance ADR-0004 accepted: the CVE surface is
inflated by unused subtrees, but it is *governable* with Renovate and the CI
scan gate.

## Functional verification

The interop rationale of ADR-0004 held in practice with zero
workarounds: a third-party OCI client (go-containerregistry) pushes and
pulls a multi-arch image index against the embedded endpoint with the index
digest preserved, per-platform manifests retrievable, nested relocated
repository names (`docker.io/bitnami/wordpress`,
`registry.example.com_5000/team/app`) first-class, and `_catalog` +
`tags/list` correct — all covered by integration tests in
`internal/store` that speak the real wire protocol.

## Decision: **GO** — keep `distribution` v3

- The dominant cost — ≈ 14.7 MB of binary and a wide module graph — buys the
  reference implementation of the protocol whose conformance matters most in
  the environments where debugging is hardest. That trade was anticipated by
  ADR-0004 and the measured numbers do not change its balance: a 22.6 MB
  static binary and a 20 MB idle footprint are unremarkable for the
  transportable-workstation profile.
- The CVE surface is real but demonstrated manageable (one finding, fixed by
  a routine bump, gated in CI).
- The fallbacks remain what ADR-0004 stated: **zot** stays the documented
  alternative should the footprint trend become problematic, and the narrow
  wrapper interface in `internal/store` — the only place the library is
  touched — is the seam that keeps that swap possible without reshaping the
  engine.

Re-measure at the v0.3.x train (after the engine lands on the storage APIs)
to confirm the trend before the surface hardens further.
