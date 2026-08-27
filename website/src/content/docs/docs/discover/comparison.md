---
title: Honest comparison
description: How Tobby compares to Zarf, Hauler, Harbor replication and skopeo scripts — including the cases where the alternative wins.
sidebar:
  order: 2
---

No tool exists in a vacuum. If you are evaluating Tobby, you are probably
also looking at Zarf, Hauler, Harbor's replication, or a set of skopeo
scripts. This page compares them at the capability level, and states plainly
where each alternative is the better choice. Claims about other tools are
based on their public documentation; check the current state of each project
before deciding — they all evolve.

## The comparison, axis by axis

| Axis | Tobby | Zarf | Hauler | Harbor replication | skopeo scripts |
|---|---|---|---|---|---|
| Verification on arrival (signatures + digests re-checked at the destination boundary, blocking by default) | Yes — cosign against the destination's trust roots, before any push | Package-level cosign signature, verified at deploy when you pass the key; not a per-upstream-artifact policy | Optional cosign verification when content is added to the store; no blocking signature gate when a haul is loaded at the destination | Scanning and policy in the registry, after content has arrived | Possible via containers `policy.json`, assembled and maintained by you |
| Promotion between connected zones **and** air-gap transfer in one tool | Yes — same application, same recipes, two modes | Air-gap focus; connected promotion is not the model | Air-gap focus (haul archives) | Connected registry-to-registry only; no removable-media journey | Both, if you script both |
| Zero reference rewriting (upstream digests and signatures survive) | Yes — canonical relocation under the source host, no mutation | No — pod image references are mutated to pull from Zarf's in-cluster registry (Zarf Agent webhook) | Content is repackaged into hauls; references depend on how you serve them | Yes — and associated cosign signatures are replicated with the images | Yes — with `--all`/`--preserve-digests`; a default copy may re-resolve a manifest list |
| No Node toolchain, no telemetry, no undeclared outbound connection | Yes — single Go binary, guarantee tested by an egress canary | Go binary; check current telemetry/update behavior yourself | Go binary; check yourself | Multi-service platform; larger surface to audit | Yes — single binary |
| Serving OS packages (apt/rpm) from verified content | Yes — FileSets served read-only under `/files/` | Not a goal | File server exists; no signature-verified gate in front of it | No | No |
| Deploying workloads into a cluster | No — out of scope | **Yes — its core strength** | No | No | No |

## Where the alternatives win

Honesty is cheaper than a failed evaluation, so here it is plainly:

- **Zarf wins when you want deployment, not just transport.** Zarf packages
  a whole application — images, charts, manifests, even a cluster
  bootstrap — and deploys it on the isolated side. If your goal is "stand up
  this Kubernetes application in the air-gapped zone with one command",
  Zarf does the whole journey and Tobby deliberately stops at the zone
  registry. The price is Zarf's model: workloads are mutated to pull from
  its internal registry, and what the isolated side verifies is Zarf's
  package-level cosign signature, not each upstream artifact's own
  signature.
- **Hauler wins for a quick one-off haul.** If you occasionally need to drag
  a handful of images and charts across a gap and no standing policy is
  required, Hauler's collect-and-serve workflow is lighter than deploying a
  verifying instance on both sides. It can also verify cosign signatures as
  it collects, and by default carries signatures, attestations and SBOMs
  along in the haul — what it lacks is a blocking policy gate on the
  destination side.
- **Harbor replication wins when you already run Harbor everywhere.** For
  connected registry-to-registry replication between Harbor instances —
  with projects, quotas, a proxy cache and a mature UI for many teams —
  Harbor's built-in replication is the natural choice. It has no
  removable-media story, and its verification model lives in the registry
  rather than at the transfer boundary.
- **skopeo wins on ubiquity and zero infrastructure.** It is on every
  operator's machine, needs no service, and copies anything OCI — with the
  right flags, digests included. What you assemble yourself is everything
  around the copy: the desired-state inventory, differential sync, retries
  and resume,
  signature policy distribution, audit logs, and the destination-side
  verification discipline. That assembly is, in essence, what Tobby is.

## Where Tobby wins

One tool covers the declared-state promotion loop **and** the physical
air-gap journey, with the same verification discipline at every boundary:
signatures verified against the destination's own trust roots before
anything is pushed, digests pinned end to end, references never rewritten,
an allowlist in front of every outbound connection, and an audit trail that
crosses the gap with the content. And the supply chain of the tool itself is
part of the offer: reproducible builds, SLSA Build L3 provenance and signed
SBOMs that [you can verify yourself](../../project/verify-a-release/).

## Reversibility: the exit costs nothing

Adopting Tobby does not lock your content into anything. The store is
standard OCI content served by a conformant registry, and
[`tobby export`](../../reference/cli/#tobby-export) writes it — or a
selection of it — as a standard OCI image layout. Worst case, skopeo works
directly on the store: every
artifact can be copied out with standard tooling, signatures included.
The [recipe format](https://tobby-fetch.github.io/recipe-spec/) is an open
Apache-2.0 specification with JSON Schemas and a Go SDK, implementable by
any tool — including one that replaces Tobby.
