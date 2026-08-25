---
title: Threat model and network flows
description: Assets, adversaries, trust boundaries per mode, the complete network-flow matrix, and every control with its requirement reference and status.
sidebar:
  order: 2
---

This page states what Tobby defends, against whom, and with which
mechanism. Every control carries its requirement number and its delivery
status; the milestone 5 media controls and milestone 6 scanning controls
are marked as upcoming, not presented as delivered.

## Assets

- **The transported content** — container images, Helm charts, OCI
  artifacts, FileSets, and the signed recipes that describe them. The
  asset is bit-exact integrity and provenance, end to end.
- **The destination zone registry** — what workloads in the protected
  zone will actually run. Nothing may reach it unverified.
- **The trust roots** — the configured public keys that decide what
  "verified" means for an instance.
- **The instance state** — accounts, token hashes, TLS key: the identity
  of the instance ([secrets](../../security/secrets/)).
- **The audit trail** — the record of who did what
  ([audit log](../../security/audit-log/)).

## Adversaries and what stops them

**Tampered media** (air-gap path, milestone 5). An attacker with physical
access to the transport media can alter or replace anything on it —
including the manifest and any trust-root file placed there. Controls:
destination-side verification of manifest completeness and checksums,
then recipe signatures and pinned digests, before any push, serving, or
local write; content not reachable from a verified recipe is never pushed
and is reported; integrity failure blocks with no override; trust roots
on the media are ignored (FR-054 — upcoming, milestone 5). The
verification order and its rationale:
[media security](../../air-gap/media-security/).

:::note[Upcoming — milestone 5]
The removable-media controls above (FR-054, FR-055, R-16, R-19, R-28)
ship with milestone 5. Track them on the
[project status](../../discover/status/) page.
:::

**Compromised upstream registry.** A registry that serves altered content
cannot make Tobby accept it: every ingredient is pinned by digest in a
cooked recipe, every recipe and ingredient signature is verified against
the configured trust roots at import *and* again before push (FR-033 —
delivered v0.3.0), and registries outside the allowlist are refused
before any transfer (FR-030 — delivered v0.4.0). A compromised registry
can at worst deny service, not inject content.

**Malicious or negligent operator.** Three fixed roles gate every surface
(FR-074 — delivered v0.4.0); content-affecting actions and sensitive
configuration changes are audit-logged with actor and origin (FR-094 —
delivered v0.1.x, catalogue growing with the features). Security
relaxations are per-scope, declared in configuration, and visible on
every surface — there is no quiet global bypass (FR-033, FR-075). The
last administrator can be neither deleted nor demoted
([authentication and RBAC](../../security/auth-rbac/)).

**Compromised signing key.** Tobby verifies against a *set* of trust
roots, so a compromised organization key is rotated by overlap: add the
new root, re-sign, remove the old (ADR-0007). Tobby itself holds no
signing key, so compromising a Tobby instance yields no signing
capability. Key custody (HSM/KMS, revocation) is deliberately the
operating organization's responsibility and out of Tobby's scope
(ADR-0007).

:::note[Upcoming — milestone 6]
Vulnerability scanning (FR-031, FR-032, ADR-0008) addresses a different
adversary — *honestly signed content with known CVEs* — and arrives at
milestone 6, together with authentication hardening (R-14) and the
reduced-trust content marker (R-22).
:::

## Trust boundaries per mode

**Passthrough**: the instance sits between a more-exposed source zone and
a more-protected destination zone. Boundary 1: everything fetched from
upstream is untrusted until signature and digest verification. Boundary
2: the destination registry only receives verified content (FR-028,
FR-033).

**Mirror** (milestone 5): three boundaries. The connected source
instance fetches and verifies as above; the media is untrusted the moment
it leaves the source's custody; the destination instance re-verifies
everything from its own trust roots (FR-052, FR-054) as if the media were
hostile — because it may have been.

## Network flows

| Flow | Direction | Passthrough | Mirror (J5) |
| --- | --- | --- | --- |
| UI, `/api/v1`, `/v2/` registry, `/files/` | inbound | one listener, authenticated (FR-075), TLS-capable (FR-082) | same |
| `/healthz`, `/readyz`, `/metrics` | inbound | unauthenticated, no instance content | same |
| Source registries and Helm repositories | outbound | allowlist-gated (FR-030), proxy-aware (FR-080), private CAs without disabling TLS (FR-081) | source side only; destination side has none |
| Retriever source (file, HTTPS, OCI) | outbound | configured endpoint (FR-010) | source side only |
| Destination zone registry | outbound | differential push of verified content (FR-028) | destination side, from the transported store |
| Trust-root URLs | outbound | fetched at configuration time only, never at verification time (FR-033) | inline or file forms instead |
| Identity provider | outbound | OIDC/SAML at milestone 6 (FR-070, FR-071) | local accounts |
| Telemetry, update checks, crash reporting | outbound | **none, ever** (NFR-019) | none |

**No other destination, ever.** NFR-019 requires that no connection
exists which the operator did not configure. The proof is structural:
every crucible acceptance run begins with an egress canary — a probe from
the isolated zone that must fail, proving the air gap is real before any
scenario is credited — and e2e suites run behind an egress-capturing
proxy that records every connection attempt (ADR-0014). See
[tests and proofs](../../project/tests-and-proofs/).

## Control summary

| Control | Reference | Status |
| --- | --- | --- |
| Signature verification, import and pre-push | FR-033, ADR-0007 | delivered v0.3.0 |
| Registry allowlist on the effective host | FR-030, FR-036 | delivered v0.4.0 |
| Authentication on by default, RBAC | FR-073–FR-076, ADR-0009 | delivered v0.4.0 |
| Security audit log, six-field schema | FR-094 | delivered v0.1.x, growing |
| No unconfigured egress | NFR-019, ADR-0012 | delivered v0.1.x, canary-proven |
| Secret hygiene, CSRF, path traversal, escaping | NFR-011–NFR-015 | delivered |
| Least privilege reference deployment | NFR-014 | delivered v0.4.0 |
| Media verification before any push | FR-054, ADR-0006 | upcoming, milestone 5 |
| Secrets never on media | R-16 | upcoming, milestone 5 |
| Trivy scanning with policy, offline DB | FR-031, FR-032, ADR-0008 | upcoming, milestone 6 |
| OIDC then SAML | FR-070, FR-071 | upcoming, milestone 6 |
