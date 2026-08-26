---
title: Media security model
description: Why the media manifest is deliberately unsigned, what the destination verifies and in which order, and what never travels on the medium.
sidebar:
  order: 2
---

This page is the security model of the removable medium: what travels, what
is verified, by whom, and against what authority. It is written for the
security reviewer who must approve a media transfer procedure.

The model is decided and specified
([ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md),
[ADR-0007](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0007-signing-cosign-key-based.md),
SRS FR-054). Milestone 5 implements it; the model itself is a design
commitment and will not change. Accreditation work can rely on this page
before the code ships.

## What travels on the medium

The transportable store carries, in one self-contained directory:

- the **artifacts** — images, charts, OCI artifacts, filesets — in an OCI
  content-addressed store;
- the **signed recipes** that justify every artifact, with their cosign
  signatures attached;
- the **operation logs** of the synchronization that produced the store;
- the **media manifest**: an inventory of every file with its checksum, the
  recipes fulfilled with their digests, the zone identity, the resolution
  timestamp, the run ID and the store format version.

Two things never travel: **secrets** and **trust material** (see below).

## The manifest is an unsigned inventory — and that is safe

The media manifest is deliberately **not signed**. This is the point most
security reviews stop at, so here is the reasoning in full.

The manifest is an *integrity and completeness aid*, not a trust anchor.
Tobby transports content; it is not a provenance authority. A signing key
held by the transport tool would add no security: an attacker able to alter
the store could re-sign the altered manifest with that same key. Signing it
would manufacture a false trust anchor, not a real one.

Authenticity comes from somewhere else entirely: the cosign signatures of
the recipes themselves, applied by your qualification pipeline before Tobby
ever sees the content, and verified on the destination side against the
destination instance's configured trust roots. Every ingredient is then
checked against the digest pinned inside its signed recipe. The signed
recipes therefore make completeness *independently derivable*: every pinned
digest must be present and correct, so tampering with the manifest cannot
hide missing, altered or extraneous content from verification.

What the unsigned manifest actually buys you:

- **fast, precisely localized failures** — which file, which recipe — instead
  of a generic verification error at push time;
- an **anti-accident guard**: the zone identity field lets the destination
  refuse a medium prepared for another zone before anything else happens.

The limits are stated with the same honesty: the manifest proves nothing by
itself, and Tobby's documentation never claims otherwise. See also
[Limits and out-of-scope](../../discover/limits/).

## The destination's trust roots are the only authority

The destination instance verifies recipe signatures **exclusively** against
its own configured trust roots. Any trust material present on the medium —
key files, alternative trust roots — is ignored. A medium can therefore
never bring its own authority with it: compromising the transport channel
does not compromise the trust decision.

Consequences, all normative (SRS FR-054):

- Content signed for the target environment is accepted; everything else is
  not.
- Content present on the medium but **not reachable from a verified recipe**
  is never pushed, and is reported. There is no side door for loose
  artifacts.
- Two zones with different trust roots can receive the same physical medium
  and legitimately accept different subsets of it.

How trust roots are configured, scoped and rotated is covered in
[Signatures, trust roots and allowlist](../../security/content-trust/).

## Verification order

On arrival, verification runs in a fixed order, and **all of it precedes any
push, any serving, and any local write**:

1. **Completeness and checksums** — every file listed in the manifest is
   present and matches its checksum; the store format version is supported;
   the zone identity matches the instance's zone.
2. **Signatures and digests** — every recipe's cosign signature verifies
   against the destination's trust roots; every ingredient matches the
   digest pinned in its recipe.

The order is deliberate: integrity failures are cheap to detect and name the
exact file, so they surface first; signature verification then runs on a
store known to be bit-intact.

Blocking is equally fixed:

- An **integrity or completeness failure blocks, with no override**. There
  is no flag, no confirmation dialog, no admin path around a corrupted
  medium.
- A **zone-identity mismatch** is the single overridable refusal: an admin
  may override it, and the override is written to the
  [audit log](../../security/audit-log/).
- Verification and blocking are decided **per recipe**: recipes whose
  signature and digests all verify are pushable; the rest is blocked and
  listed by name. A corrupted inventory or a wrong zone remain globally
  blocking.

:::note[Upcoming — milestone 5]
The verification pipeline ships with milestone 5. The order, the blocking
rules and the per-recipe granularity above are the specified behaviour it
implements (SRS FR-054, R-19).
:::

## Secrets never travel

Secret files — registry credentials, TLS private keys, proxy passwords —
belong to an instance, not to a transfer. They live in the instance's state
directory and are never written under the transportable store. The
transported directory contains content, signatures, logs and the manifest;
nothing in it authenticates anyone.

:::note[Upcoming — milestone 5]
Milestone 5 turns this rule into an enforced check (R-16): Tobby refuses to
start if secret files reside inside the transportable store, and applies
restrictive permissions by default. The separation itself is a design rule
you can already build your procedure on. Details in
[Secrets](../../security/secrets/).
:::

## Encryption at rest is the operating system's job

Tobby does not encrypt the medium. Encryption at rest is delegated to the
OS layer: LUKS on Linux, BitLocker on Windows, or whatever tooling your site
has approved. The reasoning:

- Volume encryption is a mature, certified OS capability; reimplementing it
  inside an application would duplicate it, worse.
- It would put key management inside an application that crosses trust
  boundaries — exactly where key material should not live.
- Sites that mandate specific approved encryption tooling could not use an
  application-level scheme anyway.

The operational consequence is honest and simple: **confidentiality of the
medium is your site's media policy, applied with your site's tools.** Tobby
guarantees integrity and authenticity of what is read from the medium,
whatever sits underneath.

## Decontamination stations

Industrial sites commonly route incoming media through a decontamination or
inspection station. Tobby assumes this step rather than fighting it:

- The payload is **plain files with verifiable checksums** — no exotic
  filesystem features, no opaque container format a station cannot inspect.
- The destination **re-verifies everything after** the station, in the order
  above. The station does not need to be trusted for integrity: if it
  alters the payload, verification says so, file by file.
- If the station rejects or quarantines files, the per-recipe blocking model
  degrades gracefully: intact recipes remain pushable, affected ones are
  blocked and named.

## What this gives an auditor

- Authenticity is anchored in your organization's keys, verified on the
  isolated side, offline, against configuration that never travels.
- Integrity is checked file by file before anything is pushed or served.
- Every override is a named, audited admin action; integrity failures have
  none.
- The full control-by-control view, with delivery status, is on the
  [security one-pager](../../security/one-pager/).
