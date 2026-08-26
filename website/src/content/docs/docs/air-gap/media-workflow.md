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

<svg viewBox="0 0 640 246" role="img" aria-label="A source instance on a connected workstation prepares, pre-flights and exports onto a removable medium; the medium crosses the decontamination station into the isolated zone, where the destination instance imports — verifies, then pushes to the zone registry — and writes its logs back onto the same medium" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="mw-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- sides -->
  <rect x="8" y="32" width="200" height="178" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="432" y="32" width="200" height="178" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="108" y="22" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Connected side</text>
  <text x="532" y="22" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Isolated zone</text>
  <!-- source instance -->
  <rect x="20" y="56" width="176" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="108" y="75" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Source instance</text>
  <text x="108" y="90" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">1 prepare · 2 pre-flight · 3 export</text>
  <!-- station -->
  <rect x="264" y="60" width="112" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="320" y="77" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">Decontamination</text>
  <text x="320" y="91" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">station</text>
  <!-- destination instance -->
  <rect x="444" y="56" width="176" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="532" y="75" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Destination instance</text>
  <text x="532" y="90" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">5 import — verify, then push</text>
  <!-- medium path -->
  <line x1="196" y1="80" x2="260" y2="80" stroke="var(--sl-color-gray-3)" marker-end="url(#mw-arrow)" />
  <line x1="376" y1="80" x2="440" y2="80" stroke="var(--sl-color-gray-3)" marker-end="url(#mw-arrow)" />
  <text x="320" y="116" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-2)">4 transport — removable medium</text>
  <!-- return logs -->
  <text x="320" y="140" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">return — destination logs on the same medium</text>
  <line x1="440" y1="148" x2="200" y2="148" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 4" marker-end="url(#mw-arrow)" />
  <!-- zone registry -->
  <rect x="460" y="166" width="144" height="36" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="532" y="188" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Zone registry</text>
  <line x1="532" y1="104" x2="532" y2="162" stroke="var(--sl-color-gray-3)" marker-end="url(#mw-arrow)" />
  <text x="540" y="140" font-size="9.5" fill="var(--sl-color-gray-3)">verified push</text>
  <!-- caption -->
  <text x="320" y="236" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-2)">Trust material never travels with the content — the destination's trust roots are the only authority</text>
</svg>

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
