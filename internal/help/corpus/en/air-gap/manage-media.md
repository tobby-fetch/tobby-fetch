---
title: Managing media over time
description: Media identity and freshness, retriever-aligned pruning and the occupancy threshold, sizing a cycling medium, the OCI image layout exit ramp, and the audited store reset.
sidebar:
  order: 4
---

A medium that crosses the gap once is a transfer. A medium that crosses it
every month is an operating procedure, and it raises questions the first
trip does not: is this the right disk, is it this month's, why has it grown,
and how do I start it over.

## Identity and freshness

Every store gets a **media identifier** when it is created. It stays stable
across re-synchronizations onto the same store and is different on a fresh
one, so a medium is traceable as a physical object: the identifier appears
in the manifest, in the logs on both sides, and in every refusal that
concerns the medium.

The destination remembers, **in its state directory and never in the
store**, the identifier and the resolution timestamp of the last medium it
imported for each zone. A medium older than that record is refused by
default ([`TBY-MED-007`](../../reference/errors/#tby-med-007)) — the classic
accident of re-importing last month's disk and rolling a zone backwards.

Three properties of that guard are worth stating:

- The record lives in the state directory because a register carried on the
  medium would be rewritten by whoever holds the medium.
- It advances on **completed imports only**. A verification that ran and a
  push that did not happen move nothing.
- The high-water mark **never moves backwards**, including when an
  administrator waives the refusal to restore an older delivery on purpose.

It is not a security control. The manifest is unsigned, so the timestamp can
be forged; the guard prevents an accident, and that is why it is waivable at
all. Same for the zone identity: **one medium, one zone**. A store carries
the identity of the zone it was prepared for, the destination enforces it
([`TBY-MED-006`](../../reference/errors/#tby-med-006)), and the honest
operating rule is to size one medium per zone per cycle rather than share a
physical disk between zones.

## Keeping the medium a sane size

### Pruning to the Retriever

Content the zone's Retriever no longer asks for is removed from the store at
synchronization time. In **mirror mode this is on by default** and confirmed
at trigger time: the list and the total size of what would go are shown
before it happens, from the projection the instance recomputes on every
display rather than caching. `sync.prune` is a passthrough-only setting and
is refused in a mirror configuration — a prune that could run unattended is
not a prune a person confirmed.

Only content whose recorded provenance is `recipe` is ever eligible. That is
a positive test, not an exclusion list, which is why three kinds of content
are protected by construction rather than by remembering to list them:

- **unit imports** — brought in outside any recipe;
- **the offline vulnerability database** — it arrives the same way;
- **anything pushed through `/v2/`** by a standard client, the seeding case.

Two safeguards apply on top. A cycle in which any recipe failed to resolve
prunes nothing and says so: content of a recipe that did not resolve is
indistinguishable from content the Retriever dropped, and deleting on the
strength of a network failure is not a trade this product makes. And every
removed item is named — repository, tag, digest, and the recipe that brought
it — in the run log, not merely counted.

The result is that a cycling medium stays proportional to the zone's
*current* needs rather than to its history.

### Watching the volume

Set `storage.occupancyThreshold` and the instance says when the store is
over it: a persistent warning on every UI page, the same fact on the API,
and the `tobby_store_occupancy_exceeded` metric. Crossing back under
retracts all three — a warning that appears and never clears is a warning
operators learn to ignore.

It **warns and never refuses**. Refusing a synchronization because a store
grew would strand a zone, and the thing that legitimately refuses on space
is the [pre-flight check](../../air-gap/prepare-source/), which compares a
specific projection with a specific volume.

Unset means **unmonitored**, which is reported as such and never as "within
limits": Tobby cannot guess the size of the volume it was given.

### Sizing a cycle

Deduplication by digest makes the second cycle differential — a medium sized
for the first full transfer is comfortable afterwards. Do not guess: run
[`tobby sync --dry-run`](../../reference/cli/#tobby-sync) before every cycle
and read the projection. Sizing is measured.

## The exit ramp: OCI image layout

The store — or a selection of it — can be written as a standard **OCI image
layout**, readable by `skopeo`, `oras` and `crane`, and imported back at
identical digests. This is deliberate: the content belongs to whoever stored
it, and it must be recoverable without Tobby.

```sh
tobby export --storage-root /var/lib/tobby/storage /media/usb/payload.tar
tobby export --storage-root /var/lib/tobby/storage /media/usb/payload.tar --dry-run --output json
tobby import --storage-root /var/lib/tobby/storage /media/usb/payload.tar
```

A single uncompressed tar by default — one file crosses a physical gap more
reliably than a tree — or a directory with `--directory`. `--recipe` and
`--repository` narrow the selection and are repeatable; a recipe selection
carries its ingredients, the recipe artifact, and the cosign signature
artifacts of both, in either of the layouts cosign publishes. Signatures
travel with the content they attest.

Addressing an entry afterwards depends on the tool, because the tools
disagree. Each index entry is annotated with its full repository and tag,
which is what `skopeo` matches; `oras` splits a layout reference on its last
colon and therefore addresses entries by digest:

```sh
skopeo copy oci:/media/usb/payload:registry.example.com/apps/harbor:2.15.2 …
oras manifest fetch --oci-layout /media/usb/payload@sha256:…
```

On the way back in, the layout is treated as **untrusted data**: every
manifest is accepted only if its bytes hash to the digest addressing it,
every blob is committed against the digest its manifest pins, and an archive
carrying anything other than `oci-layout`, `index.json` and
`blobs/<algorithm>/<digest>` entries is refused before it is read
([`TBY-LAY-002`](../../reference/errors/#tby-lay-002)). Compressed archives
are refused rather than inflated: decompress first. Entries are independent
— one image that did not survive the medium fails on its own line and the
rest still lands.

A layout produced by `skopeo copy` names the tag alone, which is not a
location; give `--repository` to say where the whole archive belongs.

**This is the interoperability format, not the primary one, and the reason
is what it does not carry**: the layout holds artifacts only. Logs, sync
state, the recipe graph and the media manifest are store concepts and stay
behind. An OCI layout is how content leaves this product; a store is how a
delivery travels.

Run either command against a **stopped** instance, or use
`POST /api/v1/oci-layout/export` and `.../import` on a running one: two
processes writing one storage directory is one process too many. The same
operations are on the `/admin/oci-layout` screen, with the side-effect-free
estimate first.

<!-- TODO: screenshot: the OCI image layout screen — a selection estimated before export, with the projected total and largest single file -->

## Starting a medium over

`/admin/store` shows what the store holds and offers the reset that empties
it (FR-046). It is restricted to administrators, requires the exact typed
confirmation `RESET`, and is audit-logged — including the refused attempt,
because somebody typing the wrong word into that field is the trail's early
warning. On an instance running with the authentication override, the typed
confirmation stays and the audit entry records the unauthenticated context.

The confirmation phrase is deliberately frozen and untranslated: it is
quoted in the audit trail and in site procedures, and a confirmation that
reads differently per language is a confirmation two people cannot talk
about. A mismatch is
[`TBY-STO-006`](../../reference/errors/#tby-sto-006), which is a distinct
refusal from a malformed request — nothing was wrong with the request; the
operator was asked to type a word and did not.

**What goes:** the content tree and the two content ledgers. **What stays:**
the operation history, the task logs and the store format marker — a trail
that a destructive action erases is not a trail.

The reset is also available as `POST /api/v1/store/reset`. There is no
`tobby store reset` on the command line.

<!-- TODO: screenshot: the store administration screen — what the store holds, and the typed confirmation the reset requires -->

## Two things that are not here yet

**Clock coherence.** Freshness comparisons assume plausible clocks. Detecting
an implausible clock at startup and at medium opening — warn and audit, never
silently correct — is planned for milestone 7 (R-32). Until then, a
destination whose clock is wrong can refuse a fresh medium or accept a stale
one, and the audit trail will faithfully record the wrong time.

**Updating Tobby itself inside the zone.** At milestone 6, each release is
published as recipe-ready OCI artifacts, so Tobby updates travel through the
standard mirror flow like any other content, signed by your site's own
qualification chain. There is no auto-update, ever (R-25). Today, updating
an isolated instance means carrying the new binary the way you carry
anything else.

Both are tracked on the [project status](../../discover/status/) page.
