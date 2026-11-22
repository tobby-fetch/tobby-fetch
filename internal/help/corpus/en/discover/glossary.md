---
title: Glossary
description: Plain definitions of the words this documentation uses for zones, recipes, trust, the store, removable media and project vocabulary, each linked to the page that covers it.
sidebar:
  order: 1.5
---

If you know `docker pull` but not cosign, and you have not met this product
before, this page is the place to look a word up. Each entry is short and
points to the page that covers the concept in full. Where a concept goes by
two names in the documentation, one is retained here and the other is
mentioned once as "also written".

## Zones and modes

| Term | Meaning |
|---|---|
| **OCI artifact** | Anything stored in a registry that speaks the OCI standard: a container image, a Helm chart, or any other file packaged and addressed by digest. Tobby moves these and nothing else. See [Why Tobby](../why-tobby/). |
| **Zone** | A network area with its own boundary rules. A **connected zone** can reach the upstream registry, possibly through a proxy and filters. A **restricted zone** is reachable only through controlled paths, such as a DMZ. An **air-gapped zone** has no network path to upstream at all. See [Why Tobby](../why-tobby/). |
| **Zone registry** | The registry inside a zone, from which that zone's workloads pull. It is where Tobby promotes content to. See [Connect your clients](../../passthrough/connect-clients/). |
| **Production registry** and **qualification pipeline** | The production registry is the upstream registry that hosts qualified content and the cookbook. The qualification pipeline is the organization's own process that qualifies content and publishes signed recipes there; it is not part of Tobby. See [Why Tobby](../why-tobby/). |
| **Zone identity** | The name of the zone an instance serves: the `metadata.name` of its Retriever. A destination-side instance must configure it (`zone`), because its content arrives on a medium and it cannot otherwise tell whether a medium is addressed to it. See [Authentication and roles](../../security/auth-rbac/). |
| **Passthrough mode** | The mode for zones that have a network path: a running service promotes content continuously into the zone registry. See [Connected zones overview](../../passthrough/overview/). |
| **Mirror mode** | The mode for air-gapped zones. On the connected side, an instance synchronizes content, on demand, into a store that is carried across the gap; on the isolated side, a second instance in the same mode verifies that store before serving or pushing it. Mirror is the *mode*, air-gap is the *zone* it serves, and a medium is what is carried. See [The media journey end to end](../../air-gap/media-workflow/). |
| **Promotion** (to promote) | The central verb: moving content that was already qualified upstream into a zone, unchanged, after verification. Tobby does not build, modify or re-sign what it promotes. See [Your first promotion](../../try/first-promotion/). |
| **Cascade** | Chained zones in which each zone's instance feeds the next: connected, then restricted, then more restricted. The same signed recipes flow along the chain and each instance verifies on entry. See [Zone Retriever and cascade](../../passthrough/retriever-cascade/). |

## Recipes

| Term | Meaning |
|---|---|
| **Recipe** | A YAML document listing the ingredients of one application, each pinned by version and digest. It is the unit Tobby promotes. See [Recipes, cookbooks and retrievers](../../recipes/understand/). |
| **Ingredient** | One OCI artifact that a recipe references: a container image, a Helm chart, a generic OCI artifact or a FileSet. See [Recipes, cookbooks and retrievers](../../recipes/understand/). |
| **Cookbook** | An OCI repository namespace where recipes are published as ordinary OCI artifacts. The cookbook of a zone is the authoritative catalog of software approved for it. See [Recipes, cookbooks and retrievers](../../recipes/understand/). |
| **Draft / cooked recipe** | A draft is a recipe under construction: version constraints are allowed and digests are optional. A cooked recipe is published in a cookbook with every ingredient pinned by exact tag and digest, signed and immutable. **Cooking** is the step between them. See [Recipes, cookbooks and retrievers](../../recipes/understand/). |
| **Digest / pin** | A digest is the `sha256:…` content address of an artifact. A recipe pins each ingredient by it, so one signature attests the exact bytes of the whole delivery. See [Recipes, cookbooks and retrievers](../../recipes/understand/). |
| **Retriever** | A YAML document listing the recipes, with version constraints, that one zone wants. It is the desired state of that zone, and there is one Retriever per zone. See [Recipes, cookbooks and retrievers](../../recipes/understand/). |
| **FileSet** | An ingredient kind: arbitrary files (configuration bundles, documentation, scripts, certificates) packaged as a standard OCI image. Also written "file set". See [Recipes, cookbooks and retrievers](../../recipes/understand/). |

## Trust and policy

| Term | Meaning |
|---|---|
| **cosign** | A third-party tool, from the Sigstore project, that signs OCI artifacts and verifies those signatures. Tobby verifies cosign signatures; it never signs anything itself. See [Signatures, trust roots and allowlist](../../security/content-trust/). |
| **Trust root** | A public key configured in Tobby (`trust.roots`) and used to verify the cosign signatures of recipes. See [Signatures, trust roots and allowlist](../../security/content-trust/). |
| **Allowlist** | The list of registry host patterns (`registries.allowlist`) that Tobby may contact as a source or promote into. When the key is absent, nothing is restricted. Also written "allow-list". See [Signatures, trust roots and allowlist](../../security/content-trust/). |
| **Relaxation scope** | A declared, named exception (`trust.scopes`) that admits recipes from given repository patterns under relaxed rules, for example unsigned. There is no global bypass: outside a scope, verification is strict. Also written "trust scope". See [Signatures, trust roots and allowlist](../../security/content-trust/#relaxation-scopes--and-their-visibility). |
| **Reduced assurance** | The marker carried by a recipe admitted unsigned under a relaxation scope, and by everything it reaches. It names the scope and the reason, and shows in reports, the API, the interface, logs and metrics. See [The reduced-assurance marker](../../security/reduced-assurance/). |
| **Roles** | An account is a `viewer`, an `operator` or an `admin`, each including the previous one. Triggering a synchronization or verifying a medium needs `operator`; waivers and the administration screens need `admin`. See [Authentication and roles](../../security/auth-rbac/). |
| **Waiver** | A deliberate override of one of the two medium-wide import refusals that guard against accidents: a medium addressed to another zone, or older than the last one imported. The web interface and the API require the `admin` role; on the command line, access to the host is the control. A waiver applied at import is audited. No waiver exists for an integrity or signature failure. See [Import on the isolated side](../../air-gap/import-destination/). |

## Store and content

| Term | Meaning |
|---|---|
| **Store** | The directory (`storage.root`) where Tobby keeps the artifacts, recipes and operation logs it holds. It is large, self-contained and refetchable. See [Deploy: Kubernetes and VM](../../passthrough/deploy/). |
| **State directory** | The directory (`state.root`) holding the instance's identity: accounts, tokens, certificates. It is small, nothing recreates it, and it is the backup target. It must never be nested with the store. See [Deploy: Kubernetes and VM](../../passthrough/deploy/). |
| **Relocation** (relocated path) | The rule that places every ingredient in the zone registry under its original source host: `<zone-registry>[/<base-prefix>]/<source-host>/<repository-path>`. `docker.io/bitnami/wordpress` becomes `registry.zone.example/docker.io/bitnami/wordpress`, with the digest unchanged. See [Connect your clients](../../passthrough/connect-clients/). |
| **Base prefix** | An optional repository path prefix applied uniformly to every relocated ingredient of an instance (`storage.basePrefix`, the instance's own layout). `destination.basePath` is a distinct key: the prefix the next zone sees on the destination registry. See [Connect your clients](../../passthrough/connect-clients/). |
| **Embedded registry** | The standard OCI registry (`/v2/`) that every instance serves from its own store, next to the web interface and the API. A zone with no registry of its own can pull from it. See [Connect your clients](../../passthrough/connect-clients/). |
| **Synchronization** (sync) | One run that resolves the Retriever, fetches the recipes and their ingredients, verifies them, and, in passthrough with a destination registry, pushes what the zone lacks. Passthrough runs it on a schedule (`sync.interval`); mirror only on request. It is a task with a run ID. See [Zone Retriever and cascade](../../passthrough/retriever-cascade/). |
| **Destination registry** | The zone registry an instance promotes into, set by `destination.registry`. When it is empty, nothing is promoted. See [Configuration reference](../../reference/configuration/). |
| **Source substitution** | A fetch-time setting (`registries.substitutions`) that makes a downstream zone fetch `docker.io/…` from its upstream zone registry without editing any recipe. It changes only the endpoint contacted, never the relocated path. See [Zone Retriever and cascade](../../passthrough/retriever-cascade/). |
| **Effective reference** | The reference actually contacted once source substitution has applied. The allowlist and credential lookup apply to it, while trust scopes match the nominal reference. See [Signatures, trust roots and allowlist](../../security/content-trust/). |
| **Provenance class** | The origin recorded for each repository in the store: `recipe` (managed by a verified recipe), `unit-import` (a one-off import, or a FileSet packed on the instance) or `seed` (pushed through `/v2/` by a standard client). See [Operate over time](../../passthrough/operate/). |
| **One-off import** | The import of a single OCI artifact by reference, outside any recipe. Also written "unit import"; its provenance class is `unit-import`. See [One-off imports](../../passthrough/one-off-import/). |
| **Prune** | Removal of recipe-managed content that the resolved Retriever no longer references. In passthrough it is the `sync.prune` setting, off by default. In mirror mode it is on by default and confirmed when a synchronization is triggered. One-off imports, the vulnerability database and seeded content are never eligible. See [Managing media over time](../../air-gap/manage-media/). |

## Removable media

| Term | Meaning |
|---|---|
| **Medium** (media) | The removable storage, such as a disk, that carries a store across an air gap. Moving the directory is the transfer. See [The media journey end to end](../../air-gap/media-workflow/). |
| **Media manifest** | The inventory of what a medium covers, stored as `meta/media.json`. It is deliberately unsigned, so the destination treats it as a claim and re-verifies the content. See [Media security model](../../air-gap/media-security/). |
| **Verdict** | The result of verifying a medium: `pushable` (every delivery cleared), `partial` (some deliveries cleared, others blocked) or `blocked`. See [Import on the isolated side](../../air-gap/import-destination/). |
| **Media gate** | The lock of a destination-side instance: until the instance has verified the medium itself, in this process, it refuses to serve its content (`TBY-MED-030`). It closes again at every restart. See [Media security model](../../air-gap/media-security/). |
| **Pre-flight** | The check run before an export that computes what would travel and refuses impossible transfers, such as content that does not fit with its safety margin. See [Prepare the source workstation](../../air-gap/prepare-source/). |
| **Plan mode** (dry run) | `tobby sync --dry-run`: reports everything a synchronization would do without doing it. See [Prepare the source workstation](../../air-gap/prepare-source/). |
| **Transfer slip** | A printable page, in English or French, summarizing one physical transfer: medium, freshness, inventory, verification, scan and assurance markers. It is an unsigned aid, never a proof. See [The transfer slip](../../air-gap/transfer-slip/). |

## Project vocabulary

| Term | Meaning |
|---|---|
| **Run ID** | The identifier that one synchronization, import or media verification carries through the logs, the task and its report. Filter the logs on it. See [Troubleshooting](../../reference/troubleshooting/). |
| **Error code `TBY-…`** | A stable identifier of the form `TBY-<area>-<nnn>` attached to every user-visible error. The code never changes meaning. See [Errors and troubleshooting](../../reference/errors/). |
| **Crucible** | The test tier that replays a milestone's scenarios on real disposable nodes and gates the milestone's acceptance rather than merges. See [Tests and quality proofs](../../project/tests-and-proofs/). |
| **Milestone / train** | The project ships in numbered milestones. Milestone N ships as the release line (train) v0.N.x, except milestone 7, which ships as the release candidates of 1.0.0. See [Project status and release notes](../status/). |
| **`FR-nnn` / `NFR-nnn`** | Functional and non-functional requirements of the specification (SRS), each with a stable number. See [SRS and ADRs](../../reference/srs-adr/). |
| **`UC1` to `UC3`** | The three use cases of the SRS: passthrough promotion between connected zones, mirror synchronization into an air-gapped zone, and a substitution or seeding registry. See [SRS and ADRs](../../reference/srs-adr/). |
| **`R-nn`** | A roadmap item added after the initial specification, listed on the [roadmap](https://tobby-fetch.github.io/tobby-fetch/roadmap.html) and cited by the SRS amendment that implements it. See [SRS and ADRs](../../reference/srs-adr/). |
| **`B-nnn`** | A defect number, cited in the [changelog](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md) entries of the fixes that closed it. |
