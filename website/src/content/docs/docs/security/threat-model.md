---
title: Threat model and network flows
description: Assets, adversaries, trust boundaries per mode, the complete network-flow matrix, and every control with its requirement reference and status.
sidebar:
  order: 2
---

This page states what Tobby defends, against whom, and with which
mechanism. Every control carries its requirement number and its delivery
status; the milestone 6 scanning and enterprise-identity controls are
marked as upcoming, not presented as delivered.

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

**Tampered media** (air-gap path). An attacker with physical access to the
transport media can alter or replace anything on it — including the
manifest and any trust-root file placed there. Controls: destination-side
verification of manifest completeness and checksums, then each recipe's
files against their pinned digests and its cosign signature against
*this* instance's trust roots, before any push, any serving and any local
write; every covered file is checked against its own content address as
well as against the inventory, so rewriting the unsigned inventory to
agree with a corrupted blob changes nothing; content not reachable from a
verified recipe is never pushed and is reported; an integrity or
signature failure blocks the affected recipe whole, with no override for
anyone; trust roots on the media are ignored (FR-054, R-19 — delivered
v0.5.0). The verification order and its rationale:
[media security](../../air-gap/media-security/).

**A medium from the wrong place or the wrong month.** A medium addressed
to another zone, or older than the last one this zone imported, is refused
before anything is read out of it (R-28). Both refusals are
**anti-accident guards, not security controls** — the manifest is unsigned,
so a hostile party can forge either field — which is exactly why they are
the only two an administrator may waive, audited (FR-094), and why the
recorded high-water mark never moves backwards when one is waived.

**A client asking an unverified instance for content.** A destination
instance holding a transported medium withholds `/v2/` and `/files/`
until a verification has cleared the medium whole, answering `403` with
`TBY-MED-030`. No role bypasses it, no setting reopens it, and no verdict
survives a restart (FR-054 — delivered v0.5.0).

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

<svg viewBox="0 0 640 206" role="img" aria-label="Trust boundaries: on the connected side, content from source registries is untrusted until Tobby verifies it; across the air gap, media is re-verified from scratch by the destination Tobby before clients receive verified content only — the destination's configured trust roots are the only authority" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="tm-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- sides -->
  <rect x="8" y="58" width="240" height="112" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="296" y="58" width="336" height="112" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="128" y="48" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Connected side</text>
  <text x="464" y="48" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Isolated side</text>
  <!-- trust boundaries -->
  <line x1="138" y1="82" x2="138" y2="158" stroke="var(--sl-color-gray-3)" stroke-dasharray="3 3" />
  <line x1="392" y1="82" x2="392" y2="158" stroke="var(--sl-color-gray-3)" stroke-dasharray="3 3" />
  <line x1="518" y1="82" x2="518" y2="158" stroke="var(--sl-color-gray-3)" stroke-dasharray="3 3" />
  <text x="138" y="74" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-2)">untrusted until verified</text>
  <text x="392" y="74" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-2)">re-verified from scratch</text>
  <text x="518" y="74" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-2)">verified content only</text>
  <!-- boxes -->
  <rect x="18" y="88" width="108" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="72" y="107" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-1)">Source</text>
  <text x="72" y="121" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-1)">registries</text>
  <rect x="152" y="88" width="84" height="46" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="194" y="107" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="194" y="121" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">source side</text>
  <rect x="308" y="88" width="72" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="344" y="115" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Media</text>
  <rect x="406" y="88" width="100" height="46" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="456" y="107" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="456" y="121" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">destination</text>
  <rect x="532" y="88" width="88" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="576" y="115" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Clients</text>
  <!-- flows -->
  <line x1="126" y1="111" x2="148" y2="111" stroke="var(--sl-color-gray-3)" marker-end="url(#tm-arrow)" />
  <line x1="236" y1="111" x2="304" y2="111" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 4" marker-end="url(#tm-arrow)" />
  <text x="272" y="102" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">air gap</text>
  <line x1="380" y1="111" x2="402" y2="111" stroke="var(--sl-color-gray-3)" marker-end="url(#tm-arrow)" />
  <line x1="506" y1="111" x2="528" y2="111" stroke="var(--sl-color-gray-3)" marker-end="url(#tm-arrow)" />
  <!-- caption -->
  <text x="320" y="196" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-2)">One rule at every boundary: the destination's configured trust roots are the only authority</text>
</svg>

**Passthrough**: the instance sits between a more-exposed source zone and
a more-protected destination zone. Boundary 1: everything fetched from
upstream is untrusted until signature and digest verification. Boundary
2: the destination registry only receives verified content (FR-028,
FR-033).

**Mirror**: three boundaries. The connected source
instance fetches and verifies as above; the media is untrusted the moment
it leaves the source's custody; the destination instance re-verifies
everything from its own trust roots (FR-052, FR-054) as if the media were
hostile — because it may have been.

## Network flows

| Flow | Direction | Passthrough | Mirror |
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
| Media verification before any push, serving or local write | FR-054, ADR-0006, ADR-0016 | delivered v0.5.0 |
| Per-recipe blocking, no override on integrity or signature | R-19 | delivered v0.5.0 |
| Secrets never on media, enforced at startup | R-16, NFR-020 | delivered v0.5.0 |
| Trivy scanning with policy, offline DB | FR-031, FR-032, ADR-0008 | upcoming, milestone 6 |
| OIDC then SAML | FR-070, FR-071 | upcoming, milestone 6 |
