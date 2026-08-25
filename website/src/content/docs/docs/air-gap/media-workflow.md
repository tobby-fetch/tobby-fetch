---
title: The media journey end to end
description: The five stable steps of a removable-media transfer — prepare, pre-flight, export, transport, import — and who acts at each one.
sidebar:
  order: 1
  badge:
    text: Partial
    variant: caution
---

In mirror mode, a transfer is a directory. Tobby synchronizes the content a
zone has asked for into a self-contained store, the store travels on a
removable medium across the air gap, and a Tobby instance on the isolated
side verifies it and pushes it into the zone registry. Moving the directory
*is* the transfer — there is no bespoke packing or unpacking step.

:::caution[Decided design, procedures at milestone 5]
The journey below — its steps, what travels, what is verified and in which
order — is decided and specified
([ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md),
SRS FR-050 to FR-056). The code that runs it ships with milestone 5: screens,
commands and step-by-step procedures are described on
[Heading for milestone 5](../../air-gap/milestone-5/) and tracked on the
[project status](../../discover/status/) page. Use this page today to design
your site procedure and your accreditation file; come back at milestone 5
for the operational detail.
:::

## Two instances, one directory

<!-- TODO: diagram: source instance (connected workstation) and destination instance (isolated zone), the removable medium crossing the gap through the decontamination station, the five steps annotated along the path, return logs travelling back on the same medium -->

The same application runs on both sides.

- The **source instance** runs in mirror mode on a connected workstation. It
  resolves the zone's Retriever, downloads and verifies the content, and
  writes it into a transportable store: artifacts, the signed recipes that
  justify them, the operation logs and a media manifest, together in one
  relocatable directory.
- The **destination instance** runs inside the isolated zone. Pointed at the
  transported directory, it re-verifies everything from scratch and pushes
  the content differentially to the zone registry. Its own operation logs are
  written back onto the medium: the medium is also the audit channel on the
  way out.

Nothing else crosses. Trust material never travels with the content — the
destination's configured trust roots are the only authority (see the
[media security model](../../air-gap/media-security/)).

## The five steps

The step names are stable: procedures, screens and error messages use them
consistently, so a site procedure written against these names will not need
renaming later.

| # | Step | What happens | Who acts |
|---|------|--------------|----------|
| 1 | **Prepare** | The source workstation is configured: mirror mode, the zone's Retriever, trust roots, registry allowlist. | admin |
| 2 | **Pre-flight** | Tobby computes what would travel and refuses impossible transfers before they start. | operator |
| 3 | **Export** | A manually triggered synchronization fills the transportable store; the media manifest is written last. | operator |
| 4 | **Transport** | The medium physically crosses the gap, through the site's media handling controls. | site media procedure (outside Tobby) |
| 5 | **Import** | The destination instance verifies the medium, then pushes verified content to the zone registry. | operator (admin for the single audited override) |

### 1 — Prepare

An instance serves exactly one zone, in exactly one mode, chosen at startup.
Preparing the source workstation means installing Tobby, selecting mirror
mode, and configuring the zone's Retriever, the trust roots and the registry
allowlist. Secrets (registry credentials, TLS keys) live in the state
directory on the workstation — never in the transportable store.

Checklist:

- The workstation is installed per your site's offline-install procedure.
- The instance runs in mirror mode and names the destination zone's Retriever.
- Trust roots and the registry allowlist match the zone's policy.
- No secret file resides under the transportable store path.
- The medium is formatted with a suitable filesystem (not FAT32) and, if
  your site requires it, encrypted at the OS level (LUKS, BitLocker).

:::note[Conceptual today]
Mode selection and instance configuration exist today. The mirror
synchronization itself, and the enforced refusal to start with secrets under
the store, ship with milestone 5.
:::

### 2 — Pre-flight

Before anything is written, Tobby computes the volume to transfer — per
recipe, deduplicated by digest, net of what the target already holds — and
compares it with the medium's free space. It refuses to start when the
projection does not fit (stating the missing bytes) and refuses filesystems
that cannot hold the payload, such as FAT32 with its 4 GiB file limit.

Checklist:

- The projected volume, per recipe and total, has been reviewed.
- Free space on the medium exceeds the projection plus the safety margin.
- The medium's filesystem was accepted by the pre-flight check.
- Refusals, if any, were resolved by pruning or a larger medium — not by
  skipping the check (there is no skip).

:::note[Upcoming — milestone 5]
The pre-flight computation and its explicit refusals are milestone 5
behaviour (SRS FR-055), including a scriptable dry-run. Track it on the
[project status](../../discover/status/) page.
:::

### 3 — Export

The operator triggers the synchronization manually — from the UI or the API,
never on a schedule: in mirror mode a media preparation is always a
supervised human act. Tobby downloads what is missing, verifies signatures
and digests on the way in, writes everything into the store on the medium,
and finishes by writing the media manifest: the inventory, the recipes
fulfilled, the zone identity, the run ID and the store format version.

Checklist:

- The synchronization completed without blocked recipes, or every blocked
  recipe is understood and accepted as absent.
- The media manifest was written (it is always the last write).
- The run ID of the synchronization is recorded in your transfer paperwork.
- The medium was cleanly unmounted.

:::note[Upcoming — milestone 5]
Manual mirror synchronization and the media manifest ship with milestone 5
(SRS FR-014, FR-054).
:::

### 4 — Transport

Tobby is deliberately absent from this step. The medium follows your site's
media handling procedure: chain of custody, decontamination or inspection
station, registration. The payload is designed to survive inspection — plain
files, verifiable checksums, no exotic filesystem features — and to be fully
re-verified after it, so the station does not need to be trusted for
integrity.

Checklist:

- Chain of custody is documented from export to import.
- The medium passed the site's decontamination or inspection station.
- The medium is handed to the isolated-zone operator with its transfer
  paperwork (zone, date, run ID).

This step is purely organizational: you can write and rehearse it today.

### 5 — Import

The destination instance treats the medium as untrusted until proven
otherwise. Verification runs before any push, any serving, any local write:
manifest completeness and checksums first, then recipe signatures against
the destination's own trust roots, then every ingredient digest. Recipes
that verify are pushed differentially to the zone registry; everything else
is blocked and named. The destination's logs are written back onto the
medium for the return trip.

Checklist:

- The medium's zone identity matches this instance's zone.
- Verification verdicts were reviewed per step and per recipe.
- Blocked recipes, if any, are listed in the report and handled per your
  site procedure — the only override is the audited admin override for a
  zone-identity mismatch; integrity failures have no override.
- The push completed; the returning medium carries the destination logs.

:::note[Upcoming — milestone 5]
The destination-side Media screen, its per-recipe verdicts and the guided
Verify → Report → Push flow ship with milestone 5 (SRS FR-052, FR-054).
The verification order itself is already normative — see the
[media security model](../../air-gap/media-security/).
:::

## After the import

The zone registry now serves the content. Connecting the zone's clusters and
hosts to it works exactly as in passthrough mode — see
[Connect your clients](../../passthrough/connect-clients/).

<!-- TODO: printable per-step checklists generated at build time from this page -->
