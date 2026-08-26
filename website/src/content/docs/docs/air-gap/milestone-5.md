---
title: Heading for milestone 5
description: Everything the mirror and air-gap mode delivers with milestone 5, consolidated on one page until it ships.
sidebar:
  order: 4
  badge:
    text: J5
    variant: note
---

Milestone 5 (release train v0.5.x) delivers the second complete use case:
prepare a physical medium, transport it, push its content into an isolated
zone — with guardrails at every step for an operator who is not a Tobby
expert.

Everything on this page is **upcoming**. It is one consolidated page, kept
deliberately instead of four empty stubs; when milestone 5 ships, it splits
into four real pages — *Prepare the source workstation*, *Pre-flight*,
*Import on the isolated side*, *Managing media over time* — written against
the shipped behaviour. The design decisions behind it are already published:
the [media journey](../../air-gap/media-workflow/) and the
[media security model](../../air-gap/media-security/) will not change shape.
Delivery is tracked on the [project status](../../discover/status/) page.

## Preparing the source workstation

The source instance is a single binary on a workstation, Linux or Windows —
the Windows mirror journey is validated end to end in CI, and workstation
distribution adds a winget manifest and a Scoop bucket alongside the
existing release binaries and offline-installable deb/rpm/apk packages.

- **Offline installation**: the packages install with no remote repository;
  release provenance is verifiable beforehand — see
  [Verify a release](../../project/verify-a-release/).
- **Mirror-mode quickstart**: the guided `tobby quickstart` gains the
  mirror-specific questions (transportable store location, state location,
  zone Retriever), and remains scriptable for non-interactive setup.
- **Manual trigger only — an assumed difference from passthrough.** A mirror
  instance never synchronizes on a schedule. Preparation of a medium is
  always a supervised human act, triggered from the UI or the API (SRS
  FR-014). This is a design position, not a missing feature: an unattended
  process must not decide what crosses an air gap.

## Pre-flight: verify before exporting

Before a synchronization or an export starts, the pre-flight check answers
the operator's two questions — *does it fit, and will it pass?*

- **Volume vs space**: the bytes to transfer are computed per recipe,
  deduplicated by digest and net of what the target already holds, then
  compared with the medium's free space. The pre-flight check will refuse to
  start when the projection exceeds free space minus a configurable safety
  margin (default 10 %), stating the missing byte count.
- **Explicit refusals**: filesystems that cannot hold the payload are
  refused by name — FAT32 and its 4 GiB file limit is the canonical case —
  and an unidentifiable filesystem produces a warning, never silence. A
  file-too-large error mid-write fails cleanly, store intact.
- **Scriptable dry-run**: a plan mode (CLI `--dry-run`, API, UI) produces
  the full report of a synchronization to come — version resolution,
  per-digest statuses, volumes, projected pruning, policy verdicts — with no
  side effect, and with distinct exit codes so CI pipelines can gate on it.
  This rides on the CLI contract also landing at milestone 5: documented
  `--output json` on every command and a published exit-code table under
  semantic versioning.

### The store on the medium

The transportable store is a plain directory, self-contained and
self-describing:

```text
<store>/
├── registry/   # OCI content-addressed store: images, charts,
│               # artifacts, filesets — and the recipes themselves
├── logs/       # structured JSON operation logs (both directions)
└── meta/       # store format version, sync state, media manifest
```

For interoperability, the store (or a selection of recipes) can also be
exported to — and imported from — the standard **OCI image layout**, as a
directory or a single tar, readable by `skopeo`, `oras` and `crane`:

```bash
tobby export --format oci-layout /media/usb/payload.tar   # outbound
tobby import --format oci-layout /media/usb/payload.tar   # inbound
```

The layout export carries artifacts only — logs and sync state are
store concepts — which is exactly why it is the interop format, not the
primary one. A store reset (start a medium over, clean) is an admin action
with a typed confirmation, audit-logged.

## Import on the isolated side

The destination instance gets a dedicated **Media screen**, the guided
counterpart of the verification pipeline:

- an inventory summary — zone, timestamp, recipes, volumes;
- **verdicts per step** (integrity → signatures → digests) and **per
  recipe**, with a guided Verify → Report → Push flow;
- **fine-grained blocking**: recipes whose signature and every digest verify
  are pushable; everything else is blocked, with no exception, and listed by
  name in the report. A corrupted inventory or a wrong zone identity remain
  globally blocking;
- **one single override**, and only one: an admin may override a
  zone-identity mismatch, and the override is written to the audit log.
  Integrity and completeness failures have no override path at all;
- every refusal states what to do next, in the error taxonomy style used
  everywhere else — media error codes join the
  [errors reference](../../reference/errors/) when they ship.

## Managing media over time

- **Identity and freshness**: every medium gets a unique identifier,
  recorded in its inventory and logs. The destination remembers the
  timestamp of the zone's last import and will refuse, by default, a medium
  older than it — the classic swapped-media accident — with an audited admin
  unblock for the legitimate cases.
- **Pruning aligned with the Retriever**: content the zone's Retriever no
  longer asks for is pruned from the store at synchronization time, with the
  list and total size shown before it happens. Unit imports, the
  vulnerability database and bootstrap seeding are protected roots and are
  never pruned. This keeps a cycling medium's size proportional to the
  zone's *current* needs, not its history.
- **One medium = one zone**: a store carries the identity of the zone it was
  prepared for, and the destination enforces it. Do not share a physical
  medium across zones; size one medium per zone per cycle.
- **Cycle sizing**: deduplication by digest means the second cycle is
  differential — a medium sized for the first full transfer is comfortable
  afterwards. The pre-flight report gives the projection before every run,
  so sizing is measured, not guessed.
- **Clock coherence**: freshness comparisons assume plausible clocks.
  Detection of an implausible clock at startup and at medium opening — warn
  and audit, never silently correct — is planned for milestone 7 (R-32).
- **Updating Tobby itself in the isolated zone**: at milestone 6, each
  release is published as recipe-ready OCI artifacts, so Tobby updates
  travel through the standard mirror flow like any other content, signed by
  your site's own qualification chain — no auto-update, ever (R-25).

## End of the journey

After a successful import, the zone registry serves the transferred content.
Connecting your cluster and hosts to it —
[Connect your clients](../../passthrough/connect-clients/) — works identically
in both modes: that page is the destination of the air-gap journey, not just
the passthrough one.
