# ADR-0002 — Recipes as OCI artifacts; cookbook = OCI repository

## Status

Accepted — 2026-07-11

## Context

Once a recipe is written and its ingredients qualified, it must be
**published**: made discoverable, versioned, signed, and transportable to
every zone that consumes it — including air-gapped zones reached only by
removable media. The set of published recipes for an environment is called
the **cookbook**; it defines the exact software perimeter authorized in that
environment, and downstream processes (including an external mark-and-sweep
purge of zone registries) enumerate it to decide what may exist in a zone.

The publication mechanism therefore needs: strong content identity (digests),
versioning, authenticated access, attached signatures, replication across
zones, and an enumeration API — and it must not introduce a *second*
distribution channel next to the one already carrying the artifacts
themselves.

## Decision

Recipes are stored and distributed as **OCI artifacts**, and a **cookbook is
an OCI repository** containing published recipes.

- **Artifact type:** `application/vnd.tobby.recipe.v1+yaml`. The recipe YAML
  document is the artifact's single layer; the manifest's `artifactType`
  identifies it.
- **Reference layout:** `<registry>/<cookbook>/<name>:<version>`, e.g.
  `registry.example.com/cookbook/wordpress:6.5.2`.
- **Signatures:** a Sigstore/cosign signature is attached to the recipe
  artifact (as an OCI referrer), signed at publication time by the
  qualification pipeline.
- **Publication invariant:** a recipe pushed to a cookbook ("cooked") must
  pin every ingredient by digest; floating tags and semver constraints are an
  authoring-time convenience only.
- **Enumeration:** the cookbook is listed through the standard OCI tag/listing
  API — no Tobby-specific index is required, which is what keeps the external
  purge process possible. An optional cookbook index artifact may be specified
  later for richer discovery, without changing this decision.

```text
registry.example.com/cookbook/          ← the cookbook (one OCI repository)
├── wordpress:6.5.2                     ← recipe artifact
│     artifactType: application/vnd.tobby.recipe.v1+yaml
│     └── cosign signature (attached referrer)
├── wordpress:6.5.1
└── ai-model-serving:1.4.0
```

## Consequences

- Tobby reuses the entire OCI ecosystem instead of rebuilding it:
  authentication (`dockerconfigjson` credentials, ADR-0001), transport,
  digest-based identity, replication/mirroring, and cosign signing all work
  on recipes exactly as they do on images.
- Recipes travel **in-band**: the same registry sync or removable-media
  export that carries the ingredients carries the recipes describing them
  (see ADR-0006). A zone is never left with artifacts it cannot account for.
- Generic tools (`oras`, `crane`, `skopeo`) can inspect, copy, and back up
  cookbooks without Tobby.
- Registries hosting cookbooks should support the OCI 1.1 referrers API for
  attached signatures; for older registries, cosign's tag-based fallback
  scheme applies. This constrains registry choice mildly and is documented.
- Recipe identity is dual — a human version tag and a content digest — and
  Tobby always records and verifies the digest, matching how it treats every
  other ingredient.

## Alternatives considered

### Git repository as the cookbook

Git is the natural home of recipes *while they are authored*, and nothing in
this decision prevents that. As the *publication* channel, however, git adds a
second protocol, a second credential system, and a second replication story
next to the registries that must exist anyway for the artifacts — a real cost
in restricted and air-gapped zones where every channel is negotiated. Git
also has no native per-document content identity (a file's meaning depends on
a ref) and no standard signature-attachment model comparable to OCI
referrers. Rejected as the publication channel; expected as the authoring
workflow in front of it.

### Database (relational or document store)

A database gives rich querying but nothing else the requirements need:
credentials, backup, replication, and air-gap transport would all be bespoke,
and third-party tools could not interoperate. It also makes the cookbook
state diverge from what the registry actually serves — the exact failure mode
the digest-pinning invariant exists to prevent. Rejected.

### Flat files on a share

Publishing recipes as plain YAML files (network share, object bucket) is
simple but provides no versioning contract, no authenticated enumeration
API, no content-addressed identity, and no signature attachment convention.
Every property would need to be reinvented with directory conventions and
sidecar files — a fragile, tool-hostile reimplementation of what OCI
registries already standardize. Rejected.
