---
title: Media security model
description: Why the media manifest is deliberately unsigned, what the destination verifies and in which order, and what never travels on the medium.
sidebar:
  order: 5
---

This page is the security model of the removable medium: what travels, what
is verified, by whom, and against what authority. It is written for the
security reviewer who must approve a media transfer procedure.

The model is decided and specified
([ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md),
[ADR-0007](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0007-signing-cosign-key-based.md),
[ADR-0016](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0016-media-manifest.md),
SRS FR-054), and implemented as described. Accreditation work can rely on
this page.

## What travels on the medium

The transportable store carries, in one self-contained directory:

- the **artifacts** — images, charts, OCI artifacts, filesets — in an OCI
  content-addressed store;
- the **signed recipes** that justify every artifact, with their cosign
  signatures attached;
- the **operation logs** of the synchronization that produced the store,
  under `_tobby/`;
- the **media manifest** (`meta/media.json`): an inventory of every covered
  file with its size and SHA-256, the recipes fulfilled with their pinned
  digests, the zone identity, the medium's identifier, the resolution
  timestamp, the producing version and run, and the store format version.

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

A second mechanism is what makes the first one harmless. Every covered file
is checked **against its own content address** as well as against its
inventory entry. An attacker who corrupts a blob and rewrites the inventory
to match defeats the inventory and is still caught by the digest the content
is stored under
([`TBY-MED-015`](../../reference/errors/#tby-med-015)). Dropping that check
is what would make the unsigned manifest load-bearing, which is exactly what
this section refuses.

What the unsigned manifest actually buys you:

- **fast, precisely localized failures** — which file, which recipe — instead
  of a generic verification error at push time;
- an **inventory to read** before the medium is plugged into an isolated
  zone, and a completeness answer the content alone cannot give: a blob that
  never made it across is otherwise indistinguishable from a blob that was
  never meant to be there;
- two **anti-accident guards**: the zone identity and the resolution
  timestamp let the destination refuse a medium prepared for another zone,
  or an older medium than the one it last imported, before anything else
  happens.

### Coverage stops where the medium is written after the fact

The inventory covers every regular file under the registry tree and under
`meta/`, excluding `meta/media.json` itself — a file cannot inventory
itself. Everything under `_tobby/` is **outside coverage** by construction:
the task area, the operation logs, and the destination's return logs. Files
that keep being written after the inventory is taken cannot be inventoried
without invalidating it on the next line they receive.

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

1. **The manifest** — it parses, its own format and the store format version
   are ones this build reads, its paths are well formed, and the zone
   identity and freshness guards are evaluated.
2. **Every recipe** — each file it reaches is checked against its inventory
   entry *and* against its own content address, then the recipe's cosign
   signature is verified against the destination's trust roots.
3. **A final sweep** — for content that is present and unaccounted for, or
   inventoried and reachable from no verified recipe.

The order is deliberate: integrity failures are cheap to detect and name the
exact file, so they surface first; signature verification then runs on a
store known to be bit-intact.

Blocking is equally fixed:

- Verification and blocking are decided **per recipe** (R-19): a recipe
  whose signature verifies and whose every reachable file matches its pinned
  digest is pushable; a recipe failing either is blocked **whole**, with no
  override, and named in the report with the file that decided it. A
  delivery that verified in part is not a delivery — but a medium carrying
  several deliveries still delivers the intact ones.
- An **integrity or signature failure has no override**. There is no flag,
  no confirmation dialog, no admin path around a corrupted medium, for any
  role.
- **Four refusals stay medium-wide**, because per-recipe salvage is
  meaningless for them: an absent, unreadable or unsupported manifest, and
  an altered recipe graph, block everything with no override; a medium
  addressed to another zone, and a medium older than the last one imported
  here, block everything and are the **only two** an administrator may
  waive, each waiver written to the
  [audit log](../../security/audit-log/) with the actor and the origin.

The two waivable refusals are anti-accident guards over an *unsigned* claim,
not security controls — which is precisely why they are the two that can be
waived at all, and why nothing that rests on cryptography can be. The full
code-by-code table is on
[import on the isolated side](../../air-gap/import-destination/).

### Serving is part of the order

"Precedes any push, any **serving**, and any local write" has three verbs. A
destination instance holding a transported medium withholds `/v2/` and
`/files/` — in whole, for every role, administrators included — until a
verification *it performed* has cleared the medium, and answers `403` with
[`TBY-MED-030`](../../reference/errors/#tby-med-030) and the way out. The
gate opens only on a medium that came out whole, holds no persistent record,
re-closes on every restart, and has no opt-out setting.

## Secrets never travel

Secret files — registry credentials, TLS private keys, proxy passwords —
belong to an instance, not to a transfer. They live in the instance's state
directory and are never written under the transportable store. The
transported directory contains content, signatures, logs and the manifest;
nothing in it authenticates anyone.

The rule is an enforced check, not an instruction (NFR-020, R-16): an
instance **refuses to start** ([`TBY-CFG-002`](../../reference/errors/#tby-cfg-002))
when `state.root`, `registries.credentialsFile` or `server.tls.keyFile`
resolves inside the store. The comparison goes through the real filesystem —
relative paths, `..` and symbolic links included — so a path that reads as
"outside" and lands inside is caught, and the refusal names both the setting
and the path it resolved to. Files holding secret material are created
owner-only. Details in [Secrets](../../security/secrets/).

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
- Exactly two refusals can be waived, both by an administrator, both
  audited, both over unsigned claims; integrity and signature failures have
  no override for anyone.
- Nothing was served off the medium before it was verified — the registry
  and the file surfaces stay closed until then, and no setting reopens
  them.
- The full control-by-control view, with delivery status, is on the
  [security one-pager](../../security/one-pager/).
