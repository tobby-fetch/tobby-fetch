---
title: Why Tobby
description: The problem Tobby solves, what it deliberately is not, and how to choose between its two operating modes.
sidebar:
  order: 1
---

Your organization qualifies software in a connected zone: it builds or selects
container images, Helm charts, AI models and files, scans them, pins them by
digest, and signs the result. The zones that need this content sit behind
network boundaries — restricted networks, and at the far end, zones with no
network path at all. Getting qualified content there usually means re-tagging
images, rewriting chart values, or repackaging into an opaque archive. Every
one of those steps breaks the digest or the signature that made the content
trustworthy in the first place. Tobby moves the content across those
boundaries **without changing a byte of it**, and re-verifies everything on
arrival.

<svg viewBox="0 0 640 336" role="img" aria-label="Three independent deployment shapes: passthrough between two connected zones, a cascade of passthrough instances, and mirror instances exchanging a store on removable media — each carrying content from source registries to a restricted zone registry" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="wt-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- row 1: passthrough, one hop -->
  <text x="8" y="14" font-size="11" font-weight="600" fill="var(--sl-color-gray-2)">Passthrough — between two connected zones</text>
  <rect x="8" y="24" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="68" y="41" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Source registries</text>
  <text x="68" y="55" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker.io, ghcr, …</text>
  <rect x="245" y="24" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="305" y="41" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="305" y="55" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">passthrough</text>
  <rect x="482" y="24" width="150" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="557" y="41" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Restricted zone registry</text>
  <text x="557" y="55" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker · helm · oras</text>
  <line x1="128" y1="44" x2="241" y2="44" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <line x1="365" y1="44" x2="478" y2="44" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <!-- row 2: cascade -->
  <text x="8" y="119" font-size="11" font-weight="600" fill="var(--sl-color-gray-2)">Cascade — each zone feeds the next, recipes unchanged</text>
  <rect x="8" y="129" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="68" y="146" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Source registries</text>
  <text x="68" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker.io, ghcr, …</text>
  <rect x="166" y="129" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="226" y="146" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="226" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">passthrough</text>
  <rect x="324" y="129" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="384" y="146" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="384" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">passthrough</text>
  <rect x="482" y="129" width="150" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="557" y="146" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Restricted zone registry</text>
  <text x="557" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker · helm · oras</text>
  <line x1="128" y1="149" x2="162" y2="149" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <line x1="286" y1="149" x2="320" y2="149" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <line x1="444" y1="149" x2="478" y2="149" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <!-- row 3: mirror / air-gap -->
  <text x="8" y="224" font-size="11" font-weight="600" fill="var(--sl-color-gray-2)">Air-gap (mirror) — the store travels on removable media</text>
  <rect x="8" y="234" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="68" y="251" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Source registries</text>
  <text x="68" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker.io, ghcr, …</text>
  <rect x="166" y="234" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="226" y="251" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="226" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">mirror — source side</text>
  <rect x="324" y="234" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="384" y="251" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="384" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">mirror — isolated side</text>
  <rect x="482" y="234" width="150" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="557" y="251" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Isolated zone registry</text>
  <text x="557" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker · helm · oras</text>
  <line x1="128" y1="254" x2="162" y2="254" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <line x1="286" y1="254" x2="320" y2="254" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 4" marker-end="url(#wt-arrow)" />
  <text x="303" y="246" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">media</text>
  <line x1="444" y1="254" x2="478" y2="254" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow)" />
  <!-- bottom note -->
  <text x="320" y="308" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-2)">One tool, three deployment shapes — combine them as your zones require</text>
  <text x="320" y="326" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">Same digests, same signatures — re-verified at every boundary</text>
</svg>

## The pallet truck that signs nothing

Tobby is a transport tool, and deliberately nothing more. A useful mental
model: a pallet truck in a warehouse. It carries sealed pallets, checks the
seals at every door, and refuses a pallet whose seal is broken. It never
opens a pallet, never repacks one, and never applies a seal of its own.

**What Tobby is:**

- A carrier for OCI content: container images, Helm charts, arbitrary OCI
  artifacts, and file sets — driven by signed, digest-pinned
  [Recipes](../../recipes/understand/).
- A verifier at every boundary: cosign signatures checked against the
  destination's own trust roots, digests re-checked before every push, a
  registry allowlist enforced before any transfer.
- A portable OCI registry: every instance serves a standards-compliant
  `/v2/` endpoint that `docker`, `podman`, `helm`, `oras` and `skopeo`
  consume directly, so it can seed a new environment or stand in for a zone
  registry.

**What Tobby is not:**

- It does not build, test, scan-for-qualification, or select software. It
  consumes the output of your qualification pipeline.
- It **signs nothing** and holds no private key. Authenticity comes
  exclusively from your organization's cosign signatures, verified against
  the trust roots configured on the receiving instance. If Tobby signed
  content, it would become a trust anchor — and a target.
- It does not rewrite references. `docker.io/library/postgres` stays
  recognizable as `<zone-registry>/docker.io/library/postgres`; digests and
  signatures survive the trip unchanged.
- It does not purge destination registries or orchestrate deployments.
  See [limits and out-of-scope](../../discover/limits/) for the full list,
  each with its consequence and its justification.

## Choose your mode

An instance runs in exactly one mode, chosen at startup.

| | Passthrough | Mirror |
|---|---|---|
| Between | Two connected zones | A connected zone and an air-gapped one |
| Runs as | Long-lived containerized service (Kubernetes or another runtime) | Single binary on a workstation or transportable host |
| Trigger | Periodic, configurable interval | Manual only — never unattended |
| Transport | Differential network push to the zone registry | Physical carry of a self-contained store on removable media |
| Status | **Available** (v0.4.x) | **Available** (v0.5.x) |

Both modes are delivered. The media journey is described end to end in
[the media workflow](../../air-gap/media-workflow/), with its security model
on [the media security model](../../air-gap/media-security/).

## Three ways it earns its keep

**Continuous promotion between connected zones.** A restricted zone declares
what it wants in a single manifest, its *Retriever*. A passthrough instance
re-reads it on a schedule, resolves the recipes from the production cookbook,
verifies signatures and policy, and pushes only what the zone registry is
missing. A second run with nothing new transfers zero bytes. The signed
recipes travel with their content, so the zone can prove what it holds.
Start with [the passthrough overview](../../passthrough/overview/), or
[try a first promotion](../../try/first-promotion/) in ten minutes.

![The per-recipe mapping screen: each ingredient with its relocated path, tag, digest and local presence](../../../../assets/docs/recipe-mapping.png)

**Crossing the air gap.** The same declaration drives a mirror workstation:
it synchronizes the selected recipes onto a self-contained store, an operator
carries the media across the gap, and the same application on the other side
re-verifies everything — completeness, digests, signatures against the
destination's trust roots — before a single byte reaches the zone registry.
The journey is described end to end in
[the air-gap section](../../air-gap/media-workflow/), from
[preparing the source workstation](../../air-gap/prepare-source/) to
[importing on the isolated side](../../air-gap/import-destination/).

**Seeding and standing in.** Because every instance is a conformant OCI
registry, Tobby can bootstrap a brand-new environment — including serving OS
packages from verified FileSets over plain HTTP — or act as a temporary
substitute while a zone registry is down.

## Where to go next

- [Try it in 10 minutes](../../try/install-and-start/) — one binary, one
  guided quickstart.
- [Honest comparison](../../discover/comparison/) — Zarf, Hauler, Harbor
  replication, skopeo scripts, including where they win.
- [The security model on one page](../../security/one-pager/) — the document
  to hand to your CISO.
- [Project status](../../discover/status/) — every feature, its status, its
  milestone.
