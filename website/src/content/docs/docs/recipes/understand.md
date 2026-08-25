---
title: Recipes, cookbooks and retrievers
description: The vocabulary, the draft-to-cooked lifecycle, how to choose an ingredient kind, and how Tobby resolves versions and transfers only what a zone is missing.
sidebar:
  order: 1
---

A recipe describes everything one application needs to run in a zone —
images, charts, models, files — pinned to exact digests, so one signature
attests the whole delivery. This page explains the concepts as you meet
them in Tobby. The format itself is an open specification, published
separately under Apache-2.0 at
[tobby-fetch.github.io/recipe-spec](https://tobby-fetch.github.io/recipe-spec/):
everything normative — grammar, JSON Schemas, validation rules — lives
there, and this page links to it rather than restating it.

## The vocabulary

The metaphor is a kitchen, and it is load-bearing: each word names one
precise object.

| Term | What it is |
| --- | --- |
| **Recipe** | A YAML document listing the *ingredients* of one application, each pinned by version and digest. |
| **Ingredient** | One OCI artifact a recipe references: a container image, a Helm chart, a generic OCI artifact, or a file set. |
| **Cookbook** | An OCI repository namespace where recipes are published as ordinary OCI artifacts. The cookbook of a zone is the authoritative catalog of software approved for that zone. |
| **Retriever** | A YAML document listing the recipes (and version constraints) one zone wants. It is the desired state of that zone. |

There is no dedicated server behind any of this. A cookbook is a plain OCI
registry; a recipe is an ordinary OCI artifact any tool can push, pull, and
sign. That choice is what lets recipes cross zones with the same tools as
the content they describe — signatures included. The normative definitions
are in the specification's
[terminology](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#3-terminology)
and [cookbook](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#11-the-cookbook)
sections.

Recipes are Kubernetes-style manifests (`apiVersion: recipe.tobby.dev/v1alpha1`,
`kind: Recipe`), deliberately familiar to anyone who operates a cluster —
and deliberately strict: a misspelled field is rejected, never silently
ignored, because a typo in `digest:` must not weaken pinning
([strict validation](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#43-strict-validation)).

## Draft and cooked

A recipe exists in exactly one of two states, and the state is not a field —
it is determined by where the document lives and what it contains:

- A **draft** is a recipe under construction: it lives in your workspace or
  git, version constraints are allowed (`~0.16.1`, `26.x`), digests are
  optional. It is freely editable and diff-reviewed like any other file.
- A **cooked** recipe is published in a cookbook: every ingredient carries
  an exact tag **and** a digest, and the artifact is immutable and signed.
  Fetching its ingredients at their recorded digests yields bit-identical
  content anywhere, forever — or fails verifiably.

**Cooking** is the act in between: resolving every constraint to an exact
tag, recording every digest, and publishing the result. A qualification
pipeline typically cooks a recipe after its checks pass. A cooked recipe is
never edited: any change — even one digest — requires a new
`metadata.version`. The full rules are in the specification's
[draft and cooked](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#8-draft-and-cooked-recipes)
section.

## The lifecycle in a Tobby context

End to end, a recipe passes through five hands:

1. **An author writes a draft** — usually starting from an existing recipe
   or from the [annotated examples](../write-and-publish/#learn-from-the-examples),
   and validating it with the spec's `recipe lint` CLI.
2. **Someone cooks and publishes it** to the production cookbook — with
   [`tobby recipe push`](../write-and-publish/#publish-with-tobby-recipe-push),
   from the [interface](../write-and-publish/#publish-from-the-interface),
   or with generic OCI tooling — then signs the published digest with
   cosign. Tobby never holds a private key and signs nothing.
3. **A retriever declares what a zone wants.** It lists recipe names with
   exact versions or constraints, resolved against the cookbook. A Tobby
   instance reads its retriever from a file, an `https://` URL, or an OCI
   artifact — see [Zone retriever and cascade](../../passthrough/retriever-cascade/).
4. **Tobby synchronizes.** On each cycle it re-resolves every retriever
   entry, verifies each recipe's signature against the destination's
   configured [trust roots](../../security/content-trust/), and transfers
   the recipe **with** all its ingredients — bit-exact, digests and
   upstream signatures intact. The recipe artifact itself lands in the
   zone's cookbook, so the zone stays self-describing and auditable
   offline.
5. **Clients consume the content** from the zone —
   [Connect your clients](../../passthrough/connect-clients/).

For a one-shot need — a single image, once — a recipe is overkill; see
[Import one-off content](../../passthrough/one-off-import/) and what that
choice means for provenance.

## Choosing an ingredient kind

Four kinds cover everything a zone consumes. The choice is usually
obvious; the pitfalls are not.

### ContainerImage

A runnable image, possibly multi-platform. The digest pins the **index**
(the multi-platform manifest list), not one platform's manifest. The
optional `platforms:` list names what the destination must have — naming
only what your zone runs keeps transfers and media small, while the pinned
index stays intact and verifiable. Platform labels are matched exactly:
`linux/arm64` and `linux/arm64/v8` are different strings, and Tobby fails
the ingredient rather than guessing. Spec:
[§7.1](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#71-containerimage).

### HelmChart

A chart stored as an OCI artifact (Helm ≥ 3.8 native format). Two things
trip up every new author:

- **A chart never implies its images.** The format refuses to evaluate
  templates and values: every image the chart will need must be listed
  explicitly as a `ContainerImage` ingredient, so the transfer stays
  reviewable. Finding that list is the genuinely hard part of authoring —
  the [examples walkthrough](../write-and-publish/#learn-from-the-examples)
  is dedicated to the four ways an image escapes `helm template`.
- **Legacy `index.yaml` repositories are out of scope.** A chart published
  only to a classic HTTP repository must be republished into an OCI
  registry you control before it can be an ingredient at all. The archive
  is not rewritten — the bytes, and any upstream signature, survive the
  move.

Leave `vendorDependencies` at its default (`false`) unless you have read
[§7.2](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#72-helmchart):
vendoring rewrites the chart archive, so the recorded digest is no longer
the upstream one and upstream chart signatures no longer apply.

### OCIArtifact

Everything else that lives in an OCI registry: AI models and weights,
vulnerability database bundles, SBOM collections, WASM modules, policy
bundles. The optional `artifactType` field is a guard: if set, the fetched
artifact's type must match, which catches tag-reuse mistakes and repository
confusion. Spec:
[§7.3](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#73-ociartifact).

### FileSet

Arbitrary files — configuration bundles, offline documentation, scripts,
certificates — packaged **as a standard OCI image**. That packaging gives
two consumption modes for free: mount it read-only in Kubernetes via image
volumes, pinned by digest with no drift, or extract it to disk under the
spec's safety rules. The spec site has a hands-on guide,
[Packaging a FileSet](https://tobby-fetch.github.io/recipe-spec/guides/packaging-a-fileset/),
for building one with a deterministic digest. A FileSet is also how a zone
serves an OS package repository — see
[Connect your clients](../../passthrough/connect-clients/).

One more habit worth forming: fast-moving data does not belong in a slow
recipe. A vulnerability database re-published several times a week should
not be pinned beside a registry you upgrade twice a year — give it its own
recipe, on its own cadence.

## Version constraints, and what actually moves

An ingredient's `version` — and a retriever entry's — is either an exact
tag or a constraint: `~0.16.1` (patch-level), `^0.158.0` (compatible),
`26.x` (series), comparisons and conjunctions. The complete grammar,
including semantics that differ from common libraries, is normative in
[§9](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#9-version-constraint-syntax).
Three behaviors matter operationally:

- Constraints are re-resolved against the cookbook at **each**
  synchronization: publishing a patch release is how it reaches every
  zone — no retriever file to edit.
- The **highest** matching version wins; if nothing matches, that entry
  fails and says so. There is no silent fallback, and one unresolvable
  recipe never blocks the others.
- In a cooked recipe, constraints are gone: exact tags and digests only.
  Resolution is an authoring-time and retriever-time concept.

Transfers are then differential, decided digest by digest. For each item
Tobby compares what the recipe pins against what the destination holds and
classifies it **new** (absent, will be transferred), **outdated** (the tag
exists but points at another digest, will be updated), or **up-to-date**
(already this exact digest, zero bytes moved). Re-running a
synchronization that has nothing to do moves nothing — a property the
acceptance scenarios check on real registries.

## Recipes in the interface

Recipes are first-class in the Tobby interface, with the same data
available through the [API](../../reference/api/):

- **The recipe list** (*Recipes*) shows every recipe the instance has
  resolved, its version, when it was resolved, and its verification state —
  a recipe admitted unsigned under a declared
  [trust scope](../../security/content-trust/) says so explicitly, never
  silently.
- **The per-recipe mapping** lists every ingredient of each recorded
  version: its kind, the local relocated repository it landed in (linked,
  so you can walk to the content this instance actually holds), its tag,
  and its pinned digest — each copiable with one click.
- **The recipe document itself** is shown on the content page of the
  published artifact: the exact YAML this instance holds and verified on
  entry — not a fresh read of the upstream cookbook — together with its
  digest, so the distinction stays checkable. You can copy it or download
  it as a file. A cooked recipe being immutable, this is deliberately a
  download and not an editor: it is the natural starting point for
  authoring the next version.

<!-- TODO: screenshot: /recipes list with verification badges -->
<!-- TODO: screenshot: recipe document section on a content page (YAML, digest, copy/download) -->

## Where to go next

- [Write, publish and sign a recipe](../write-and-publish/) — the
  authoring walkthrough, from a minimal document to a signed publication.
- [Zone retriever and cascade](../../passthrough/retriever-cascade/) — how
  an instance consumes recipes, and how zones chain without modifying them.
- [Signatures, trust roots and allowlist](../../security/content-trust/) —
  how verification is configured on the consuming side.
