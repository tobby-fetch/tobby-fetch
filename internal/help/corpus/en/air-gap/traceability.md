---
title: Tracing and proving a transfer
description: How one run ID follows a transfer through the air gap, and how the medium itself carries the evidence in both directions.
sidebar:
  order: 6
---

An air-gap transfer must be provable after the fact: what crossed, when,
prepared by which run, verified with which verdicts. Tobby's answer is that
**the evidence travels with the content** — structured logs and the manifest
live on the medium itself, correlated by a single run identifier from the
source workstation to the destination registry.

## One run ID, end to end

Every synchronization run is assigned a unique **run ID** at start. It is
carried by every log record of the run, alongside the other correlation
fields: task ID, recipe, ingredient, digest (SRS FR-090). Filtering the logs
on one run ID reconstructs that run completely.

The same run ID then crosses the gap:

1. **At the source** — every log line of the mirror synchronization carries
   it.
2. **On the medium** — the media manifest records it, next to the zone
   identity and the resolution timestamp.
3. **At the destination** — the instance that opens the transported store
   reuses the run ID in its own logs while it verifies and pushes.

One identifier therefore ties together the preparation, the payload and the
import — across two machines that never shared a network.

## Durable JSON logs on the medium

In mirror mode, operation logs are not written to stdout: they are written
**to a file inside the transported store** — `_tobby/logs/operations.log` by
default, `logging.media.file` to change it — so the destination side can
audit what the medium contains and how it was produced (SRS FR-053).

The path must lie **outside the media manifest's coverage**, under
`_tobby/`, and the instance refuses to start otherwise: a log written inside
coverage would invalidate, line by line, the very inventory the destination
verifies.

Because a removable medium can be yanked, the log file is held to a
durability contract (SRS FR-056):

- an explicit **fsync at every task boundary** — a yanked or failing medium
  loses at most the entries of the task in progress;
- **size-based rotation** — `logging.media.maxSize` (10 MiB by default) and
  `logging.media.keep` (3) bound the log at `maxSize × (keep + 1)` on a
  medium whose whole point is to carry gigabytes of content.

The fsync contract is tested by execution, not asserted: a process killed
outright immediately after a task leaves that task's records readable on the
medium.

`logging.media.disabled: true` turns the log off. It is explicit and never a
default — a medium arriving without one cannot be audited by whoever
receives it.

Logs are JSON Lines with stable keys: parseable by your SIEM or by `jq`,
with no format guesswork.

## Security events on the same channel

Security audit events — authentication, account and token lifecycle,
sensitive configuration changes, and the audited media overrides — use a
dedicated six-field schema (actor, action, target, outcome, timestamp,
origin) and travel on the same channel as the operation logs, separable by a
stable marker field. On a mirror instance that channel is the file on the
medium: **the audit trail crosses the air gap with the content it accounts
for.** The schema and its guarantees are described in
[Audit log](../../security/audit-log/).

## Exploitation on return

The medium is a two-way audit channel:

- **Outbound**, it carries the source-side logs of the run that produced the
  payload.
- **On the isolated side**, the destination instance writes its own logs —
  verification verdicts, pushes, any override — onto the medium, in a
  dedicated path *outside* manifest coverage, so the outbound inventory
  remains checkable on the way back.
- **On return**, the connected side reads back the destination logs: filter
  on the run ID and you hold the complete story of the transfer, both sides
  included, without any network link having existed.

Practically: archive the medium's `_tobby/logs/` content (both directions)
with the transfer record, keyed by run ID. That archive is your replayable evidence
for a security review.

One honest limit, stated plainly: like the manifest, the logs are **not
signed**. They are operational evidence, not a trust anchor — the
authenticity of the content itself never rests on them (see the
[media security model](../../air-gap/media-security/)).

## Printable transfer slip

Sites that escort media with paper get a first-class document: a printable,
bilingual transfer slip derived from the Media screen — medium summary,
verification report, scan results — exportable as HTML or text, and clearly
marked as an **unsigned aid**.

:::note[Upcoming — milestone 6]
The transfer slip (R-07) ships with milestone 6. The
[Media screen](../../air-gap/import-destination/) it derives from is
delivered; until the slip itself ships, the run ID, the verification report
(downloadable as JSON) and the media logs above are the traceability
backbone to build a paper procedure on.
:::
