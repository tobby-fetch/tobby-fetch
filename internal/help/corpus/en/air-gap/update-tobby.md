---
title: Updating Tobby in an isolated zone
description: Tobby crosses the air gap the way the software it carries does — a recipe of its image, chart and binaries, signed by your site, carried by the ordinary mirror flow, deployed by a person. No self-update.
sidebar:
  order: 8
---

Tobby updates through its own channel (R-25), exactly like the
[vulnerability database](../vulnerability-database/): a new version is a
**recipe** your site cooks and signs, carried by the standard mirror flow,
and deployed by a person. Nothing in Tobby looks for a newer version, and
nothing installs one by itself (NFR-019).

## What a release publishes

From v0.6.0, every release publishes three recipe-ready artifacts,
signed keyless by the release workflow and covered by SLSA provenance:

| Artifact | Reference | Kind in the recipe |
|---|---|---|
| container image (amd64, arm64) | `ghcr.io/tobby-fetch/tobby-fetch:vX.Y.Z` | `ContainerImage` |
| reference Helm chart | `ghcr.io/tobby-fetch/charts/tobby:X.Y.Z` | `HelmChart` |
| static binaries (linux and windows, amd64 and arm64) | `ghcr.io/tobby-fetch/binaries/tobby:vX.Y.Z` | `FileSet` — one index, one file per platform, byte-for-byte the release asset |

[`examples/tobby.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/tobby.yaml)
is the recipe that carries them, with placeholder digests.

## Cook it, sign it with your chain

1. In the connected zone, verify the release as
   [Verify a release](../../project/verify-a-release/) shows: `cosign
   verify` on the image, the chart and the binaries index, and
   `slsa-verifier` on the provenance.
2. Copy `examples/tobby.yaml`, set `metadata.version` and the three
   versions, and replace each placeholder digest with the one the release
   notes state (or `crane digest <ref>:<version>`).
3. **Sign the cooked recipe with your qualification key** and publish it
   to your cookbook. The destination trusts its own trust roots: a recipe
   signed by the project key alone is refused there — Tobby signs nothing,
   and your site admits a Tobby release like any other content
   (invariant 1).
4. Add it to the zone's Retriever, next to `tobby-system`.

## Carry it, deploy it

5. Synchronize the mirror instance (`tobby sync --wait`): the new version
   is written on the medium with everything else.
6. On the isolated side, `tobby media verify` shows it pushable — or
   blocked on its signature if it was not signed by a key the zone trusts —
   and `tobby media import` pushes it to the zone registry.
7. **Deploy it yourself**: `helm upgrade` with the chart and image now in
   the zone registry, or extract the binary of your platform from the
   FileSet and replace the one on the host. Read the release's changelog
   first; a minor version may ask for a configuration change.

## Keeping only some platforms

`platforms` may keep only the binaries your zone runs. The index then
crosses *sparse* (FR-022): it is the original index, at its pinned digest,
listing children the medium does not carry. Tobby's own registry accepts
it and serves the platforms that crossed (FR-042). A third-party zone
registry that validates the members of an index refuses a sparse one,
and an isolated zone cannot widen it by fetching the rest: if yours does,
keep the four platforms, as the example does. The extra children cost a
few tens of megabytes.
