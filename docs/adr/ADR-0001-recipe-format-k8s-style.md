# ADR-0001 — Recipe format: Kubernetes-style YAML manifests, versioned API group

## Status

Accepted — 2026-07-11

## Context

Tobby moves sets of OCI-distributable artifacts (container images, Helm charts,
arbitrary OCI artifacts, packaged file sets) between network zones, from fully
connected environments down to air-gapped ones. The unit of intent is the
**Recipe**: a declarative description of everything an application ecosystem
needs, written once by the team that qualifies the software and consumed by
every downstream zone. A companion document, the **Retriever**, lists which
recipes a given zone wants.

These documents have demanding, partly conflicting requirements:

- **Human-authored and human-reviewed.** Recipes are written and diff-reviewed
  by engineers as part of a qualification pipeline; the format must be readable
  in a merge request without tooling.
- **Machine-validated.** Malformed or ambiguous recipes must be rejected early,
  with published schemas that third-party tools can validate against.
- **Evolvable over years without breaking consumers.** Recipes are long-lived
  artifacts: a recipe published today must still be interpretable by a Tobby
  released two years from now, and the format must be able to grow (new
  ingredient kinds, new fields) without silently changing the meaning of
  existing documents.
- **Usable everywhere.** Recipes are parsed on developer laptops, in CI
  pipelines, on transportable workstations, and inside air-gapped zones — with
  no Kubernetes cluster, and often no network, available.
- **Familiar.** The target audience already operates cloud-native platforms;
  a format that reuses their existing mental model lowers the adoption cost.

## Decision

Recipes and Retrievers are **Kubernetes-style YAML manifests**: typed documents
with `apiVersion`, `kind`, `metadata`, and `spec` top-level fields, under a
dedicated, versioned API group.

- **API group:** `recipe.tobby.dev` (the `tobby.dev` domain is designated for
  the project — registration is tracked for milestone 0.1;
  `tobby-fetch.github.io` is the documented fallback group if the domain
  cannot be secured).
- **Version lifecycle:** `v1alpha1` → `v1beta1` → `v1`, with `v1` frozen at
  Tobby 1.0.0. Alpha and beta versions may change between minor releases; `v1`
  is stable for the life of the major version.
- **Kinds:** `Recipe` and `Retriever`. (The *Cookbook* is deliberately **not**
  a kind — it is an OCI repository holding published recipes; see ADR-0002.)
- **Ingredient kinds** (entries inside a Recipe's `spec`): `ContainerImage`,
  `HelmChart`, `OCIArtifact`, `FileSet`.
- **Kubernetes conventions are reused where they fit.** In particular,
  registry credentials use the standard `kubernetes.io/dockerconfigjson`
  secret payload format verbatim, so existing credential tooling and operator
  knowledge apply unchanged.
- **JSON Schemas** for every kind and version are published in the
  [`tobby-fetch/recipe-spec`](https://github.com/tobby-fetch/recipe-spec)
  repository, alongside a Go SDK for parsing and validation.

Illustrative example (the normative definition lives in the recipe
specification, not in this ADR):

```yaml
# A Recipe describes one application ecosystem to be transported as a unit.
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: wordpress
  version: "6.5.2"
spec:
  ingredients:
    - name: wordpress-chart
      kind: HelmChart
      ref: registry.example.com/charts/wordpress
      version: "24.x"            # semver constraint, resolved at fetch time
    - name: wordpress
      kind: ContainerImage
      ref: docker.io/library/wordpress
      version: "6.5.2"
      platforms: [linux/amd64, linux/arm64]
      # digest is optional while authoring, mandatory once the recipe
      # is published ("cooked") to a cookbook
```

## Consequences

- Operators who know Kubernetes read and write recipes with zero training;
  generic YAML tooling (linters, editors, schema-aware IDEs) works out of the
  box against the published JSON Schemas.
- The `apiVersion` field gives us an explicit, per-document compatibility
  contract: Tobby can support several versions simultaneously and convert
  between them, following a graduation model operators already understand.
- The envelope is more verbose than a flat format; for the small documents
  recipes are, this is an accepted cost of self-description.
- The project must actually secure the `tobby.dev` domain early (action
  tracked for milestone 0.1); the API group string is hard to change once
  recipes exist in the wild.
- The Go SDK in `recipe-spec` becomes the single source of truth for parsing
  and validation, used by Tobby itself and offered to third-party tools
  (see ADR-0003).

## Alternatives considered

### Flat ad-hoc format (the proof-of-concept's `package.yml`)

The proof of concept used a minimal flat file (`images:` and `charts:` lists).
It is pleasant to write and served the POC well, but it has no versioning
envelope, no typed extensibility, and no self-description: adding a third
artifact category or changing field semantics is a silent breaking change.
For documents that must remain interpretable for years across security zones,
"no declared version" is disqualifying. Rejected.

### CUE

CUE offers types, constraints, and validation in one language, and would make
schemas and documents the same artifact. However, it remains a niche skill;
recipes must be reviewable by qualification and security staff who are not
language enthusiasts, and every third-party integration would need a CUE
evaluator rather than a YAML parser plus JSON Schema. The validation benefits
are real but achievable with JSON Schema at a fraction of the adoption cost.
Rejected — CUE may still be used *internally* to generate the JSON Schemas.

### TOML

TOML is excellent for configuration files but poor for the shapes recipes
need: deeply nested lists of heterogeneous objects (ingredients of different
kinds) are awkward to express and worse to review. There is also no ecosystem
convention for versioned, typed TOML documents. Rejected.

### Real Kubernetes CRDs

Defining `Recipe` as a CustomResourceDefinition would give free validation,
`kubectl` UX, and RBAC — but only *inside a cluster*. The format's primary
habitats are CI pipelines, transportable workstations, and air-gapped zones
where no cluster exists, and requiring one would invert the dependency: the
tool that provisions clusters' content would itself need a cluster. It would
also pull the Kubernetes API machinery into every consumer. Rejected — while
deliberately keeping the manifest shape CRD-compatible, so a future optional
in-cluster controller could adopt the same documents without changes.
