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

<!-- TODO: diagram: three zones (connected → restricted → air-gapped) with
     Tobby carrying recipes and content across each boundary -->

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
| Status | **Available** (v0.4.x) | Upcoming — milestone 5 |

:::note[Upcoming — milestone 5]
Mirror mode ships with milestone 5. The media journey and its security model
are already documented conceptually — see
[the media workflow](../../air-gap/media-workflow/) and
[the media security model](../../air-gap/media-security/) — and tracked on the
[project status](../../discover/status/) page.
:::

## Three ways it earns its keep

**Continuous promotion between connected zones.** A restricted zone declares
what it wants in a single manifest, its *Retriever*. A passthrough instance
re-reads it on a schedule, resolves the recipes from the production cookbook,
verifies signatures and policy, and pushes only what the zone registry is
missing. A second run with nothing new transfers zero bytes. The signed
recipes travel with their content, so the zone can prove what it holds.
Start with [the passthrough overview](../../passthrough/overview/), or
[try a first promotion](../../try/first-promotion/) in ten minutes.

<!-- TODO: screenshot: recipes screen with per-ingredient sync status -->

**Crossing the air gap.** The same declaration drives a mirror workstation:
it synchronizes the selected recipes onto a self-contained store, an operator
carries the media across the gap, and the same application on the other side
re-verifies everything — completeness, digests, signatures against the
destination's trust roots — before a single byte reaches the zone registry.
This is the milestone 5 use case; the journey is described end to end in
[the air-gap section](../../air-gap/media-workflow/).

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
