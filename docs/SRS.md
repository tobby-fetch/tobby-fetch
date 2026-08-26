# Tobby — Software Requirements Specification (SRS)

| Field | Value |
|---|---|
| Document | Software Requirements Specification (SRS) |
| Product | Tobby — OCI asset transfer for segmented networks |
| Repository | `github.com/tobby-fetch/tobby-fetch` (this document: `docs/SRS.md`) |
| Related specification | `github.com/tobby-fetch/recipe-spec` |
| Status | Draft |
| License of the application | GPL-3.0 (the Recipe format specification and its Go SDK are Apache-2.0, see ADR-0003) |

---

## 1. Introduction

### 1.1 Purpose

This document specifies the functional and non-functional requirements of **Tobby**,
a Go application that transfers OCI assets (container images, Helm charts, arbitrary
OCI artifacts, packaged file sets) between network zones with increasing isolation
levels, up to fully air-gapped environments.

It is the reference for design, implementation, testing, and acceptance of Tobby
v1.0.0. Every requirement is numbered (`FR-xxx` for functional, `NFR-xxx` for
non-functional), written as a testable statement, and paired with a short acceptance
criterion. Architecture decisions are recorded separately as ADRs and referenced by
number where relevant.

### 1.2 Scope

Tobby consumes a declarative description of the assets a zone needs — a **Retriever**
listing **Recipes**, each Recipe listing **Ingredients** — and makes those assets
available in the target zone. It operates in exactly two modes:

- **Passthrough mode** — Tobby runs as a long-lived containerized service between two
  connected zones. It periodically refreshes its Retriever, compares the destination
  registry with the desired state, and pushes only the missing artifacts (differential
  promotion).
- **Mirror mode** — Tobby runs on a workstation or transportable host. It synchronizes
  the selected assets into its embedded local registry, whose storage is then physically
  transported (removable media) into an air-gapped zone, where the same application
  pushes the content to the destination zone registry.

In both modes Tobby enforces supply-chain policy: registry allowlists, signature
verification, and vulnerability scanning.

Out of scope for v1.0 (see section 5): purging destination registries, serving
arbitrary static files outside the `FileSet` ingredient kind, and orchestrating
qualification pipelines.

### 1.3 Definitions

| Term | Definition |
|---|---|
| **Recipe** | A declarative YAML manifest (`apiVersion: recipe.tobby.dev/v1alpha1`, `kind: Recipe`) describing a coherent application ecosystem as a list of Ingredients. Normatively defined in `recipe-spec`. |
| **Ingredient** | One asset within a Recipe. Kinds: `ContainerImage`, `HelmChart`, `OCIArtifact`, `FileSet`. Common fields: `name`, `kind`, `ref`, `version`, `digest`, `platforms`. |
| **Cookbook** | An OCI repository holding published (“cooked”) Recipes as OCI artifacts. A cooked Recipe has all Ingredient digests pinned and is signed. |
| **Retriever** | A manifest (`kind: Retriever`) declaring the set of Recipes a given zone wants. One Retriever per zone. |
| **Passthrough mode** | Continuous, service-based promotion of assets between connected zones. |
| **Mirror mode** | On-demand synchronization to a local store for physical transport into an air-gapped zone. |
| **Connected zone** | A zone with (possibly proxied and filtered) network reachability to the upstream registry. |
| **Restricted zone** | A zone reachable only through controlled network paths (e.g., via a DMZ), served by passthrough mode. |
| **Air-gapped zone** | A zone with no network path to upstream zones; served by mirror mode and physical transport. |
| **Production registry** | The upstream registry hosting qualified assets and the Cookbook, fed by the organization's qualification pipeline. |
| **Zone registry** | The destination registry inside a given zone, consumed by that zone's workloads. |
| **Qualification pipeline** | The organization's upstream CI process that qualifies assets and publishes cooked Recipes to the Cookbook. Not part of Tobby. |
| **Trust root** | A public key configured in Tobby and used to verify cosign signatures of Recipes and Ingredients. |
| **OCI image layout** | The standard on-disk/tar layout defined by the OCI Image Format Specification, used for offline interchange. |
| **Zone identity** | The name of the zone an instance serves — the `metadata.name` of its Retriever; configured on destination-side instances and recorded in the media manifest (FR-054). |
| **Base prefix** | Optional repository path prefix applied uniformly to all relocated ingredients of an instance (FR-035, RECIPE-SPEC §11.5). |
| **Effective reference** | The reference actually contacted after source substitution (FR-036); the registry allowlist and credential lookup apply to it, while trust-root scopes match the nominal `ref`. |

### 1.4 References

| Reference | Description |
|---|---|
| Recipe specification | `github.com/tobby-fetch/recipe-spec` — normative format for `Recipe` and `Retriever` (`recipe.tobby.dev/v1alpha1`), JSON Schemas, Go SDK |
| OCI Distribution Specification | `github.com/opencontainers/distribution-spec` — registry HTTP API (`/v2/`) |
| OCI Image Format Specification | `github.com/opencontainers/image-spec` — manifests, image indexes, image layout |
| SLSA v1.0 | `slsa.dev` — supply-chain levels for software artifacts (Build track) |
| CycloneDX | `cyclonedx.org` — SBOM standard |
| Sigstore / cosign | `sigstore.dev` — artifact signing and verification |
| Trivy | `github.com/aquasecurity/trivy` — vulnerability scanner |
| OpenMetrics | `openmetrics.io` — metrics exposition format |
| Kubernetes `dockerconfigjson` secrets | `kubernetes.io/docs/concepts/configuration/secret/` — registry credential format reused by Tobby |
| RFC 2119 / RFC 8174 | Requirement level keywords |

The ADRs referenced throughout this document are listed in Appendix A.

### 1.5 Conventions

- The key words **SHALL**, **SHALL NOT**, **SHOULD**, and **MAY** are to be
  interpreted as described in RFC 2119 / RFC 8174.
- Requirement identifiers are stable: `FR-xxx` (functional), `NFR-xxx`
  (non-functional). Numbering is grouped in blocks of ten per domain; gaps are
  intentional and reserved for future insertions.
- Unless stated otherwise, every requirement applies to v1.0.0.
- YAML examples in this document are illustrative; the normative format is defined
  in `recipe-spec`.

---

## 2. Overall description

### 2.1 Operational context

Tobby targets industrial environments (energy, defense, naval, manufacturing) where
networks are segmented into zones of increasing isolation for safety and security
reasons, and where software supply chains are subject to regulatory requirements
(e.g., NIS2) and maturity frameworks (SLSA, OWASP SCVS, NIST SSDF).

In such environments, assets are qualified upstream by a qualification pipeline and
published to a production registry as signed, digest-pinned Recipes in a Cookbook.
Workloads in downstream zones must consume **exactly** those qualified assets — no
direct internet access, no unverified content. The remaining problem is *transport*:
moving OCI assets across zone boundaries reliably, verifiably, and — for the last
hop — without any network at all. Tobby is that transport tool, and only that.

```text
production        connected /            air-gapped
registry          restricted zones       zone
(Cookbook)   ──▶  Tobby (passthrough)  ─▶  ...
             ──▶  Tobby (mirror) ──[removable media]──▶ Tobby ──▶ zone registry
```

### 2.2 Product overview

| Aspect | Passthrough mode | Mirror mode |
|---|---|---|
| Deployment | Containerized service (Kubernetes or container runtime) | Single binary on a workstation / transportable host |
| Trigger | Periodic Retriever refresh (configurable interval) | Manual trigger (UI button / API call) |
| Transport | Network push to destination zone registry | Physical transport of the storage on removable media |
| Destination push | Differential (missing artifacts only) | Performed by the same application on the destination side |
| Authentication | Enabled by default; explicit acknowledged opt-out (FR-075) | Enabled by default; explicit acknowledged opt-out (FR-075) |
| Logs | Structured JSON on stdout | Structured JSON to a file on the transport media |

A Recipe, as consumed by Tobby (illustrative):

```yaml
# Illustrative Recipe — normative schema in tobby-fetch/recipe-spec
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
      version: "18.x"               # wildcard, resolved at fetch time
    - name: wordpress
      kind: ContainerImage
      ref: registry.example.com/library/wordpress
      version: "6.5.2"
      # digest is pinned once the recipe is cooked
      digest: "sha256:6d1f9c52e3b1a4d87f0e2ab5c6d97e348b21f4a0c3d5e6f7089a1b2c3d4e5f60"
      platforms: ["linux/amd64", "linux/arm64"]
```

### 2.3 Use cases

#### UC1 — Passthrough promotion between connected zones

Tobby runs as a containerized service at the boundary between a connected zone and a
restricted zone. At each refresh interval it fetches its zone Retriever, resolves the
listed Recipes from the Cookbook, verifies signatures and policy, compares every
Ingredient digest with the destination zone registry, and pushes **only the missing
or outdated artifacts** together with the Recipes themselves. Administrators monitor
and configure it through the web UI or the REST API.

#### UC2 — Mirror synchronization into an air-gapped zone

An operator runs Tobby on a workstation in a connected zone, triggers a
synchronization against the zone's Retriever, and Tobby populates its embedded local
registry storage (artifacts, Recipes, and operation logs). The storage — the
directory itself or an OCI image layout export — is carried on removable media
through the physical transfer procedure into the air-gapped zone. There, the same
application, pointed at the transported storage, pushes the content to the
destination zone registry and serves it in the meantime if needed.

#### UC3 — Substitution / seeding registry

Tobby's embedded registry is a standards-compliant OCI registry. It can therefore act
as a **temporary registry**: seeding a brand-new environment before its permanent
registry exists (bootstrap), or standing in for a zone registry that is unavailable
(disaster recovery). Standard clients (`docker`, `podman`, `helm`, `oras`, `skopeo`)
pull directly from Tobby's `/v2/` endpoint until the permanent registry is restored
and repopulated.

### 2.4 Actors

| Actor | Interaction |
|---|---|
| Platform administrator | Deploys and configures Tobby (passthrough), manages registries, certificates, secrets, and policy. |
| Transfer operator | Runs mirror synchronizations, transports media, performs destination-side pushes. |
| Auditor / security officer | Reviews logs, scan reports, signature verification results. |
| OCI clients | `docker`, `podman`, `helm`, `oras`, `skopeo` and any OCI-conformant tool pulling from the embedded registry. |
| Downstream ecosystem | GitOps controllers and housekeeping jobs (e.g., registry purge) consuming standard registry listings. |

### 2.5 Assumptions and constraints

- Recipes and Retrievers conform to `recipe.tobby.dev/v1alpha1` as defined in
  `recipe-spec`; Tobby uses the `recipe-spec` Go SDK for parsing and validation.
- Cooked Recipes published to the Cookbook are digest-pinned and cosign-signed by the
  upstream qualification pipeline; Tobby verifies, it does not qualify.
- Air-gapped zones have no access to online services: signature verification is
  key-based (no Fulcio/Rekor dependency, ADR-0007) and the Trivy vulnerability
  database is distributed offline as an OCI artifact (ADR-0008).
- Destination zone registries are OCI Distribution-conformant; any conforming
  product is in scope (CNCF distribution, Harbor, Artifactory, cloud SaaS
  registries…) and no vendor is a prerequisite — the common standard is the OCI
  registry protocol. Recipe propagation (FR-034) additionally relies on the
  registry accepting OCI artifacts (non-image `artifactType`); signatures
  travel via cosign's tag-based convention (ADR-0002, ADR-0007,
  RECIPE-SPEC §12.2), which requires no OCI 1.1 referrers API support.
  Destination compatibility with relocated repository paths is checked before
  pushing (FR-035).
- Implementation language is Go; target platforms are Linux and Windows.

---

## 3. Functional requirements

### 3.1 Modes and configuration

**FR-001 — Operating mode selection.**
Tobby SHALL run in exactly one operating mode, `passthrough` or `mirror`, selected by
configuration at startup.
*Acceptance:* the same binary started with each mode value exhibits the documented
behavior of that mode; any other value causes startup failure with a non-zero exit
code and an explicit error message.

**FR-002 — Configurable registries.**
Source registries (production registry, Cookbook location) and the destination zone
registry SHALL be configurable, per instance, without rebuilding the application.
*Acceptance:* changing a registry endpoint in configuration and restarting redirects
all subsequent pulls/pushes accordingly; endpoints are visible in the UI and API.

**FR-003 — Layered configuration.**
Tobby SHALL accept configuration from a YAML file, environment variables, and
command-line flags, with precedence `flags > environment > file`.
*Acceptance:* for any given setting defined at all three levels, the flag value wins;
with no flag, the environment value wins; effective configuration (secrets redacted)
is dumpable via a CLI/API call.

**FR-004 — Registry credentials format.**
Registry credentials SHALL be provided in the Kubernetes
`kubernetes.io/dockerconfigjson` format, in both modes.
*Acceptance:* a standard `.dockerconfigjson` payload grants Tobby authenticated
access to the matching registries without transformation. *(ADR-0001)*

**FR-005 — Guided first start. *(amendment 2026-08-12)***
Tobby SHALL provide an interactive first-start command (`tobby quickstart`) that
fills the missing configuration step by step — storage and state directories
(proposed defaults), operating mode, first administrator account with the hash
computed by the tool (FR-066) — writes the resulting configuration file, and
offers to start serving. The interactive path SHALL never be mandatory:
automation and containers remain fully driven by flags and environment (FR-003),
and the command SHALL refuse non-interactive use with an actionable message
naming the flag equivalents. Configuration validation SHALL be scoped per
command: a command SHALL NOT require settings it does not use.
*Acceptance:* a fresh host reaches a serving, authenticated instance through the
guided dialogue alone; the same result is scriptable by flags without any
prompt; `tobby user` operates without an operating mode.

### 3.2 Retriever and Recipes

**FR-010 — Retriever acquisition.**
Tobby SHALL retrieve its Retriever from any of: an HTTP(S) URL, an OCI reference, or
a local file path.
*Acceptance:* each of the three source types is covered by an automated test; the
configured source is reported in the UI/API.

**FR-011 — Parsing and validation via the recipe-spec SDK.**
Tobby SHALL parse and validate all `Retriever` and `Recipe` documents
(`recipe.tobby.dev/v1alpha1`) using the `recipe-spec` Go SDK, rejecting invalid
documents with actionable errors.
*Acceptance:* documents violating the published JSON Schemas are rejected; the error
identifies the file, path, and violated constraint. *(ADR-0001)*

**FR-012 — Recipe resolution from the Cookbook.**
Tobby SHALL resolve every Recipe listed in the Retriever from the configured Cookbook
(OCI repository), by name and version.
*Acceptance:* given a Retriever listing N recipes present in the Cookbook, all N are
fetched, validated, and displayed with their versions. *(ADR-0002)*

**FR-013 — Periodic refresh (passthrough).**
In passthrough mode, Tobby SHALL re-fetch the Retriever and reconcile the destination
at a configurable interval.
*Acceptance:* with the interval set to T, two consecutive reconciliations start
within T ± scheduling tolerance; the interval is changeable without redeployment.

**FR-014 — Manual trigger (mirror).**
In mirror mode, synchronization SHALL be triggered manually, via a UI action
(“Synchronize” button) and an equivalent API call, and SHALL NOT run unattended.
*Acceptance:* no synchronization occurs without an explicit trigger; a trigger starts
a tracked task visible in the task list (FR-062).

### 3.3 Fetch engine

**FR-020 — Ingredient kinds.**
Tobby SHALL download all four ingredient kinds — `ContainerImage`, `HelmChart`,
`OCIArtifact`, `FileSet` — as OCI content.
*Acceptance:* a Recipe containing one Ingredient of each kind synchronizes fully;
each kind has dedicated integration tests.

**FR-021 — Version resolution.**
Tobby SHALL resolve Ingredient versions expressed as exact tags, semver constraints
(`^`, `~`, `>=`), or wildcards (e.g., `12.x`) to a concrete tag and digest at fetch
time.
*Acceptance:* documented resolution examples for each notation resolve to the
expected tag against a fixture registry; a resolution report (requested → resolved →
digest) is attached to the task.

**FR-022 — Platform selection.**
For multi-arch `ContainerImage` Ingredients, Tobby SHALL fetch only the platforms
declared in the Ingredient's `platforms` field; if the `platforms` field is absent,
all platforms of the source index SHALL be transferred. When a partial platform
set is transferred, the original image index — and therefore the pinned digest —
SHALL be preserved at the destination (sparse index); if the destination registry
rejects sparse indexes, all platforms SHALL be transferred instead
(RECIPE-SPEC §7.1).
*Acceptance:* a multi-arch image with `platforms: [linux/amd64]` results in exactly
that platform's content in the destination while the original index remains
pullable by its pinned digest; an Ingredient without `platforms` lands with all
source platforms present; pulls by digest of the selected platform succeed.

**FR-023 — Unit OCI artifact import.**
Tobby SHALL support importing a single OCI artifact (any media type) by reference,
outside of a Recipe run, without requiring a container runtime client.
*Acceptance:* a one-off import via API/UI of an arbitrary OCI artifact lands it in
the local store, pullable by digest and tag.

**FR-024 — Helm chart import with dependency verification.**
Tobby SHALL import Helm charts (from OCI registries and from HTTPS chart
repositories, converted to OCI) and SHALL verify that all declared chart
dependencies are resolvable, reporting any missing dependency.
*Acceptance:* importing a chart with dependencies yields a verification report;
a chart with an unresolvable dependency is flagged as failed with the dependency
named.

**FR-025 — Optional chart dependency vendoring.**
Tobby MAY, when explicitly enabled per operation, vendor a chart's dependencies into
a self-contained archive; because this rewrites the archive and breaks the upstream
digest and signature, the operation SHALL be disabled by default and traced (original
digest, new digest, vendored dependency list) in logs and metadata.
*Acceptance:* with vendoring off, the pushed chart digest equals the upstream digest;
with vendoring on, the trace record links both digests and lists vendored
dependencies.

**FR-026 — Per-digest status.**
Tobby SHALL compute, for every Ingredient, a status relative to the destination —
`new` (absent), `outdated` (different digest), or `up-to-date` (same digest) — before
any transfer.
*Acceptance:* the status report matches an independent digest comparison performed
with `skopeo inspect`; `up-to-date` Ingredients trigger no transfer.

**FR-027 — Direct-to-storage import.**
Tobby SHALL write fetched content directly into its storage backend, without looping
back through its own network registry endpoint.
*Acceptance:* imports succeed with the HTTP listener disabled; no self-directed HTTP
traffic is observed during import. *(ADR-0005)*

**FR-028 — Differential push.**
When pushing to a destination registry, Tobby SHALL transfer only blobs and manifests
absent from the destination, in both modes.
*Acceptance:* re-pushing an already-synchronized Recipe transfers zero blobs
(verified via registry logs/metrics); a partially present image transfers only the
missing blobs.

**FR-029 — Retry and resume.**
Tobby SHALL retry failed transfers with bounded backoff and SHALL resume an
interrupted synchronization from persisted state rather than restarting from scratch.
*Acceptance:* killing the process mid-synchronization and restarting completes the
run without re-downloading blobs already stored; transient registry errors (HTTP
5xx) are retried up to the configured limit.

### 3.4 Policy and supply chain

**FR-030 — Registry allowlist.**
Tobby SHALL enforce a configurable allowlist of authorized registries for both
sources and destinations, and SHALL refuse any pull from or push to a registry not on
the list.
*Acceptance:* an Ingredient referencing a non-allowlisted registry fails policy
before any network transfer, with a distinct error class; the event is logged and
counted in metrics; when source substitution applies (FR-036), the allowlist is
evaluated against the effective reference.

**FR-031 — Vulnerability scanning with configurable policy.**
Tobby SHALL scan fetched content with Trivy and evaluate results against a
configurable policy (severity thresholds; blocking or advisory), blocking the push of
non-compliant artifacts when the policy is blocking.
*Acceptance:* with a blocking policy at `CRITICAL`, an image with a known critical
CVE is fetched, reported, and not pushed; with an advisory policy, it is pushed and
the finding appears in the task report. *(ADR-0008)*

**FR-032 — Offline vulnerability database.**
Tobby SHALL support consuming the Trivy vulnerability database as an OCI artifact
transferable by Tobby itself, so scanning works in restricted and air-gapped zones.
*Acceptance:* with all external Trivy endpoints unreachable, scanning succeeds using
a database previously imported as an OCI artifact. *(ADR-0008)*

**FR-033 — Signature verification.**
Tobby SHALL verify cosign (key-based) signatures of Recipes and of Ingredients
against configured trust roots, at import and before any push to a destination;
verification failures SHALL block the affected item. Enforcement SHALL be on by
default and MAY be relaxed only per explicitly declared trust-root scope (no
global bypass), per RECIPE-SPEC §12.3.
*Acceptance:* an unsigned or wrongly-signed Recipe/Ingredient is rejected with the
key fingerprint(s) tried; a correctly signed one passes; trust roots are configurable
without rebuild, provided inline, as files, or as HTTPS URLs fetched at configuration
time (RECIPE-SPEC §12.3); relaxation applies only to the declared scope. *(ADR-0007)*

**FR-034 — Recipe propagation.**
Tobby SHALL push the Recipes themselves (as OCI artifacts, signatures attached) to
the destination registry together with their Ingredients, so each zone's Cookbook
reflects what the zone actually holds.
*Acceptance:* after synchronization, the destination registry contains the cooked
Recipe artifact at `<registry>/<cookbook>/<name>:<version>` with its signature,
verifiable with cosign. *(ADR-0002)*

**FR-035 — Destination repository naming.**
Tobby SHALL derive every ingredient's repository path — in its embedded store and
in any destination registry — per the relocation convention of RECIPE-SPEC §11.5,
with an optional configurable base prefix (default: none) applied identically to
all ingredients of an instance. The source→destination mapping SHALL be exposed
per recipe through the API and UI (FR-065). Before pushing, Tobby SHALL verify
that the destination accepts the relocated names (nested paths, name length) and
SHALL fail with an error naming the destination's limit otherwise.
*Acceptance:* `docker.io/bitnami/wordpress` pushed to `reg.zone.example` is
pullable at `reg.zone.example/docker.io/bitnami/wordpress` with an unchanged
digest; a two-hop cascade yields the identical relocated path in both zones;
`registry-1.docker.io/...` relocates under `docker.io/`; a destination refusing
nested repositories yields an explicit pre-push error. *(ADR-0013)*

**FR-036 — Source substitution.**
Tobby SHALL support a configured source-substitution map (nominal registry host →
substitute registry base, per RECIPE-SPEC §11.5) applied at fetch time.
Substitution changes only the network endpoint contacted, never the relocated
path (FR-035). The registry allowlist (FR-030) and the credential lookup
(RECIPE-SPEC §13.2) SHALL apply to the effective host actually contacted;
trust-root scopes (FR-033) SHALL match the nominal `ref`. Logs SHALL record the
nominal→effective mapping.
*Acceptance:* in a downstream zone, a recipe referencing `docker.io/...` is
fetched from the zone registry without any modification of the recipe; a
substitute host absent from the allowlist fails policy before transfer; logs show
both references. *(ADR-0013)*

### 3.5 Embedded registry

**FR-040 — OCI Distribution endpoint.**
Tobby SHALL expose an embedded OCI registry on `/v2/` conforming to the OCI
Distribution Specification (pull and push).
*Acceptance:* the OCI distribution-spec conformance suite passes against the
endpoint. *(ADR-0004)*

**FR-041 — Standard client compatibility.**
The embedded registry SHALL be usable with `docker`, `podman`, `helm`, `oras`, and
`skopeo` without client-side workarounds.
*Acceptance:* e2e tests cover push and pull with each of the five clients.

**FR-042 — Multi-arch support.**
The embedded registry SHALL store and serve multi-arch content (OCI image indexes /
manifest lists), including partial platform sets selected per FR-022.
*Acceptance:* a multi-arch image pushed to Tobby is pullable per-platform by standard
clients; the original index — and therefore the pinned digest — is preserved,
including when only a partial platform set is stored (sparse index,
RECIPE-SPEC §7.1).

**FR-043 — Standard listing APIs.**
The embedded registry SHALL expose the standard catalog and tag listing endpoints so
that downstream tooling (including out-of-scope purge processes, section 5.1) can
enumerate content.
*Acceptance:* `GET /v2/_catalog` and `GET /v2/<name>/tags/list` return complete,
paginated listings consistent with stored content.

**FR-044 — Recipe-scoped removal and garbage collection.**
Tobby SHALL allow removing a recipe from its local store; content referenced only
by the removed recipe becomes eligible for garbage collection. Content referenced
by any other locally stored recipe, and the attached signature/attestation
artifacts (cosign tag convention and referrers) of every retained manifest, SHALL
be preserved. Garbage collection SHALL hold an exclusive lock against store
mutations, SHALL apply a minimum-age grace period to unlinked blobs, and SHALL be
crash-safe (NFR-010). Garbage collection runs as part of the removal operations,
under the exclusive lock — it needs no separate schedule.
Content whose recorded provenance is a unit import (FR-023) SHALL additionally be
removable, repository by repository, by an administrator from its repository page
and through the API (FR-061), audit-logged (FR-094); the removal runs the same
garbage collection. Recipe-managed content SHALL NOT be individually removable:
the UI presents the action disabled, naming the managing recipe. No bulk removal
SHALL exist in the content browsing surfaces — the only multi-select removal in
the product is the prune confirmation of FR-045. *(amendment 2026-08-12)*
*Acceptance:* removing one of two recipes sharing a base layer keeps the shared
blobs; after GC, `cosign verify` passes on every remaining recipe and ingredient;
a GC run concurrent with a simulated push loses no blob of a transfer that
completes; kill -9 during GC leaves a store passing integrity verification;
removing a unit-imported repository as admin deletes its manifests and its
unshared blobs, emits the FR-094 record, and leaves recipe-managed content
untouched; on a recipe-managed repository the removal action is disabled and
names the recipe.

**FR-045 — Prune to Retriever.**
In mirror mode, synchronization SHALL offer a prune option — enabled by default,
visible at trigger time with the list and total size of items to be removed —
restricted to recipe-managed content: relocated ingredient trees (FR-035) and
cookbook entries brought by previous synchronizations and no longer referenced by
the currently resolved Retriever. Content imported outside a Recipe run — unit
imports (FR-023), the offline vulnerability database (FR-032), and content pushed
through `/v2/` outside managed namespaces (UC3 seeding) — SHALL NOT be
prune-eligible unless explicitly selected. Pruned items SHALL be listed in the
run logs (FR-053).
*Acceptance:* after removing a recipe from the Retriever and re-synchronizing
with prune enabled, its exclusive content is gone and listed as pruned in the
media log; seeded content and the vulnerability database survive a default prune;
with prune disabled, the store is unchanged.

*Amendment 2026-08-11 (R-33).* The same prune SHALL be available in passthrough
mode, applied at each reconciliation cycle, with the same protected roots and
the same non-eligible content as above; unlike mirror mode it is **opt-in**,
because a passthrough transit store is not a delivery unit and shrinking it is
never implied by a refresh. Independently of prune, Tobby SHALL expose a
configurable store-occupancy threshold, raising a persistent UI warning and a
metric when the store exceeds it, so an unattended service does not fill its
volume silently.
*Acceptance:* with passthrough prune enabled, content dropped from the Retriever
is removed at the next reconciliation and listed in the run logs, while unit
imports and the vulnerability database survive; with it disabled (the default),
the transit store is unchanged by reconciliation; crossing the configured
occupancy threshold raises the warning and moves the metric, and clearing it
retracts both.

**FR-046 — Store reset.**
Tobby SHALL provide a full store reset requiring an explicit typed confirmation,
audit-logged (FR-094). On authenticated instances it SHALL be restricted to the
admin role; on instances running with the FR-075 authentication override, the
typed confirmation is maintained and the audit entry records the unauthenticated
context.
*Acceptance:* the operator role cannot reset an authenticated instance; the reset
event and its actor (or the unauthenticated context) appear in the audit log
(FR-094); the instance is immediately usable on an empty store.

**FR-047 — FileSet HTTP serving.**
Tobby SHALL serve, over read-only HTTP(S) GET under `/files/<fileset>/…`, the
merged root filesystem content of explicitly enabled `FileSet` ingredients
present in its store, applying the extraction semantics and safety rules of
RECIPE-SPEC §7.4 and §14.5. Serving SHALL be disabled by default and enabled
per FileSet by configuration; no upload or write surface SHALL exist. Byte-range
requests SHALL be supported. Access control follows FR-075/FR-076; anonymous
read MAY be enabled per FileSet for bare-host bootstrap scenarios, reported like
the FR-075 override. This surface makes Tobby usable as an OS package
repository (apt/rpm) fed exclusively by verified, signed FileSets.
*Acceptance:* a FileSet packaging an apt repository and an rpm repository is
served such that `apt` and `dnf` clients on a bare host install packages from
Tobby with no other infrastructure; a path-traversal and link-escape corpus
returns errors; a non-enabled FileSet under `/files/` returns 404; range
requests return 206.

**FR-048 — Operator FileSet packing. *(amendment 2026-08-26)***
Tobby SHALL provide, via CLI (`tobby fileset pack`) and API/UI, a packing
operation that takes a local file tree, packages it as a single-manifest
`FileSet` OCI image conforming to RECIPE-SPEC §7.4, and imports it into the
local store through the unit import path (FR-023), pinned by digest. The
resulting FileSet MAY then be enabled for FR-047 serving like any other.
Packed FileSets SHALL be recorded as manual imports — unsigned, of local
origin — and SHALL be distinguishable from Recipe-delivered FileSets in
listings and reports. This operation SHALL NOT introduce any HTTP upload
surface: writing goes through OCI import only, and serving remains FR-047,
read-only (section 5.2).
*Acceptance:* packing a directory yields a FileSet whose extraction reproduces
the tree (modes and symlinks per RECIPE-SPEC §7.4); once enabled, its content
is served under `/files/<fileset>/…` (FR-047); it appears marked as a manual
import in listings; `/files/` still accepts no write method.

### 3.6 Removable-media transport

**FR-050 — Self-contained transportable store.**
Tobby's storage directory SHALL be self-contained and relocatable: it SHALL hold the
artifacts, the Recipes, and the operation logs of the synchronization, with no state
required outside the directory.
*Acceptance:* copying the storage directory to another host and starting Tobby on it
serves the full content and shows the original synchronization history. *(ADR-0006)*

**FR-051 — OCI image layout export/import.**
Tobby SHALL optionally export its store (or a selection of Recipes) to, and import
from, the standard OCI image layout (directory or tar), for interoperability with
third-party tools.
*Acceptance:* an export is readable by `skopeo` and `oras`; importing the same
archive on another instance restores the content with identical digests. *(ADR-0006)*

**FR-052 — Destination-side operation.**
The same application SHALL, on the destination side of a physical transfer, open the
transported store (directory or layout archive) and push its content to the
destination zone registry, applying the same policy checks (FR-030, FR-033) after
the media verification of FR-054.
*Acceptance:* the UC2 end-to-end scenario — synchronize, transport (simulated),
destination push — completes with digests identical end to end.

*Amendment 2026-08-11 (R-28).* The destination instance SHALL persist, per zone,
the resolution timestamp and media identifier (FR-054) of the last media it
imported, and SHALL refuse by default a media whose resolution timestamp is
older than that record, naming both timestamps. The refusal MAY be overridden by
an admin, audit-logged (FR-094), on the pattern of the zone-identity override.
This is an **anti-accident** guard, not a security control: the media manifest is
unsigned, so a hostile party can forge the timestamp — what it prevents is an
operator re-importing last month's medium and silently rolling a zone backwards.
*Acceptance:* importing a media older than the last recorded import for that zone
is refused by default naming both timestamps; the admin override succeeds and
writes an audit entry; the record advances only on a completed import.

**FR-053 — Logs on the transport media.**
In mirror mode, operation logs SHALL be written to a file within the transported
store (path configurable), so the destination side can audit what the media contains
and how it was produced.
*Acceptance:* after a mirror synchronization, the store contains a structured log
file covering the run; its location matches the configured path.

**FR-054 — Media manifest.**
At the end of every mirror synchronization producing the transportable store
(FR-050) — after any prune (FR-045) — Tobby SHALL write into the store a media
manifest: the store inventory (paths, sizes, digests), the recipes fulfilled with
their digests, the zone identity, the resolution timestamp, and the store format
version. The manifest is an integrity and completeness aid, not a trust anchor:
Tobby signs nothing. Authenticity is established on the destination side
exclusively by verifying the transported recipes' signatures against the
destination instance's configured trust roots and every ingredient against its
pinned digest (FR-033, RECIPE-SPEC §12.3). Destination-side verification —
manifest completeness and checksums, then recipe signatures and ingredient
digests — SHALL precede any push, any serving, and any local write;
destination-side writes (return logs) SHALL go to a dedicated path outside
manifest coverage. Content present on the media but not reachable from a verified
recipe SHALL NOT be pushed and SHALL be reported. Integrity or completeness
failure SHALL block with no override; a zone-identity mismatch MAY be overridden
by an admin, audit-logged (FR-094). Trust roots present on the media SHALL be
ignored.
*Acceptance:* truncating or corrupting any covered file is detected and blocks
the push, naming the file; a tampered recipe fails signature verification and is
blocked; extraneous content not referenced by any verified recipe is reported and
not pushed; a media produced for zone A is refused by an instance configured for
zone B; a trust-root file on the media has no effect on verification; verification
progress is displayed. *(ADR-0006, ADR-0007)*

*Amendment 2026-08-26 (R-19) — granularity of the block.* The sentence
"integrity or completeness failure SHALL block with no override" above was
written as a global verdict, while RECIPE-SPEC §12.3 point 4 requires failing
closed **per item**. The two are reconciled as follows, and this paragraph
governs:

- **Per recipe.** Verification and the push decision are taken recipe by recipe.
  A recipe whose signature verifies against the destination's trust roots and
  whose every reachable ingredient matches its pinned digest and its manifest
  entry is pushable. Any recipe failing either check is blocked with no
  override, and SHALL be named in the report with the reason and the offending
  file. A partially damaged medium therefore still delivers its intact recipes,
  which is the whole point of carrying several deliveries on one medium.
- **Globally blocking, no per-recipe salvage.** A media manifest that is absent,
  unparseable, or internally inconsistent, and a zone-identity mismatch, block
  the medium as a whole — the first because there is then no inventory to reason
  about, the second because the medium is addressed to someone else. The
  zone-identity mismatch remains admin-overridable and audit-logged; a corrupt
  manifest is not.
- Files covered by the manifest but reachable from no verified recipe are
  reported as extraneous and never pushed; they do not block anything.

*Acceptance:* on a medium carrying two recipes where one ingredient blob of the
first is truncated, the first recipe is blocked and named with its offending
file while the second is pushed; a corrupted manifest blocks both with no
override offered; a zone mismatch blocks both and offers the audited admin
override.

*Amendment 2026-08-11 (R-28) — media identity.* The manifest SHALL carry a
media identifier generated when the transportable store is created, stable
across subsequent synchronizations onto the same store, and repeated in the
operation logs (FR-053) on both sides, so an incident can be traced to a
physical medium.
*Acceptance:* the identifier appears in the manifest and in the source-side and
destination-side logs of the same medium; re-synchronizing an existing store
keeps it; a freshly created store gets a different one.

**FR-055 — Pre-flight checks.**
Before starting a mirror synchronization or an export, Tobby SHALL compute and
display the per-recipe and total bytes to transfer — from source manifests,
deduplicated by digest, net of blobs already present in the applicable target
(local store for synchronization, destination for push) — the projected store or
archive size, and the target's free space; computation MAY be asynchronous and
cached (pinned digests make sizes stable). Tobby SHALL refuse to start when the
projection exceeds free space minus a configurable safety margin (default 10 %),
stating the shortfall; SHALL refuse targets whose filesystem is positively
identified as unable to hold the largest file to be written (e.g. FAT32's 4 GiB
limit — including single-tar exports) and SHALL warn when the filesystem cannot
be identified; and SHALL fail cleanly, store intact, on file-too-large errors
during writes.
*Acceptance:* an oversized synchronization is refused before any transfer with
the missing byte count stated; per-recipe sizes are displayed at trigger time; a
FAT32 target with a > 4 GiB blob or export archive is refused naming the limit; a
simulated file-too-large error mid-write leaves the store consistent.

*Amendment 2026-08-11 (R-04) — plan mode.* This computation already produces
most of a dry run; Tobby SHALL expose it as a **side-effect-free operation** in
both modes, from the CLI (`--dry-run`), the API, and the UI, over either the
configured Retriever or a candidate Retriever file, reporting: version
resolution (FR-021), per-digest statuses (FR-026), projected volumes (this
requirement), projected prune (FR-045), and the policy verdicts evaluable
without transfer — registry allow-list (FR-030) and the signature verdicts of
the recipes reachable without fetching content. A plan run SHALL write nothing
to the store, push nothing, and SHALL NOT reset or gate the passthrough refresh
schedule (FR-013). Exit codes SHALL distinguish "nothing to do", "changes
planned", and "refused by policy" so the operation is usable as a CI gate
(FR-066).
*Acceptance:* a plan run over a Retriever with pending changes reports them and
leaves the store byte-identical; a plan run whose recipes violate the allow-list
exits with the policy code without contacting the destination; a plan run in
passthrough mode does not advance the refresh schedule.

**FR-056 — Transport log durability.**
The log file on the transport media (FR-053) SHALL use size-based rotation and
SHALL be flushed with an explicit fsync at task boundaries, so that yanked or
failing media lose at most the entries of the task in progress.
*Acceptance:* killing the process (or detaching the media image) immediately
after a task completes leaves that task's entries readable on the media; rotation
keeps the log within the configured size budget. *(ADR-0012)*

### 3.7 API and UI

**FR-060 — Versioned REST API.**
Tobby SHALL expose a versioned REST API under `/api/v1`, documented with an OpenAPI
specification shipped with the application.
*Acceptance:* the OpenAPI document is served by the running instance, validates
against the OpenAPI schema, and describes every implemented endpoint.

**FR-061 — UI/API parity.**
Every action available in the web UI SHALL be achievable through the REST API, and
vice versa (excluding purely presentational features).
*Acceptance:* the e2e suite executes each UC1/UC2/UC3 scenario twice — once via UI,
once via API only — with identical results.

**FR-062 — Web UI screens.**
Tobby SHALL provide a server-rendered web UI comprising at least: a dashboard
(instance status, mode, last runs), Recipe/Cookbook browsing, synchronization status
(per-Recipe, per-Ingredient, per-digest), registry configuration, Retriever settings
(Retriever source; refresh interval in passthrough mode; manual synchronization
trigger in mirror mode), certificates and secrets management, and task management
(running/queued/completed, with logs). The operating mode is displayed in the UI
but is selected at startup only (FR-001): changing it requires a configuration
change and a restart. This is a deliberate deviation — switching modes re-purposes
the instance and its deployment shape, which is not a runtime operation.
*Acceptance:* each listed screen exists, is reachable from the main navigation, and
is covered by at least one e2e test; the UI offers no mode-switch control.
*(ADR-0010)*

*Amendment 2026-08-11 (R-02) — media screen.* The UI SHALL provide a dedicated
**Media** screen, present on both the source and the destination side, which
walks a non-expert operator through the physical transfer: the inventory summary
of the store (zone, media identifier, resolution timestamp, recipes, volumes),
the per-stage and per-recipe verdicts of destination-side verification
(completeness and checksums → recipe signatures → ingredient digests), and the
guided sequence Verify → Report → Push, where each step unlocks the next. A zone
refusal or a stale-media refusal SHALL be stated in plain language with the
course of action, not as an error code alone. This screen introduces no engine
behaviour of its own: the blocking order is already normative in FR-054, and the
screen is required to make it legible.
*Acceptance:* on the destination side the Push control is unreachable until
verification has completed; per-recipe verdicts name blocked recipes and their
offending files; a zone mismatch and a stale medium each render their refusal
with the admin override path.

**FR-063 — Internationalization.**
All UI labels SHALL be externalized and provided in English and French, with the
active language selectable; adding a language SHALL NOT require code changes beyond a
translation catalog.
*Acceptance:* switching language translates all UI labels; a missing-translation
check runs in CI; no user-facing string is hard-coded in templates. *(ADR-0010)*

**FR-064 — Themable styling.**
The UI's visual theme SHALL be customizable via configuration (design-token based
stylesheet override) without rebuilding the application.
*Acceptance:* providing a custom stylesheet/token file changes colors, logo area, and
typography on restart; the default theme passes the UI quality bar (NFR-017).
*(ADR-0010)*

**FR-065 — Consumption aids.**
Tobby SHALL generate, per recipe and per destination: the source→destination
reference table (copyable), and a K3s/RKE2 `registries.yaml` mirror snippet
(mirrors + rewrite rules) redirecting nominal image references to the zone
registry for runtime image pulls. The documentation SHALL state the aids' limits:
admission policies and GitOps chart sources must reference relocated paths
explicitly, using the mapping table. Tobby SHALL NOT rewrite chart values.
*Acceptance:* on an RKE2 node configured with the generated snippet, a pod
referencing `docker.io/bitnami/wordpress` pulls from the zone registry; the table
matches pushed content. *(ADR-0013)*

**FR-066 — Command-line interface.**
Tobby SHALL provide a CLI covering the automation-relevant operations: media
export/import (FR-051), media verification (FR-054), triggering a mirror
synchronization (FR-014), and configuration dump (FR-003). The CLI complements
the UI and API; the FR-061 parity requirement applies between UI and API only —
the CLI is not required to mirror every screen.
*Acceptance:* the UC2 flow can be scripted end to end through the CLI, with exit
codes distinguishing success, policy refusal, and verification failure.
*(ADR-0006, ADR-0010)*

*Amendment 2026-08-11 (R-08) — stable command-line contract.* The CLI SHALL
carry a contract an automation can depend on across versions:
`--output json` on every command that reports anything, with schemas documented
alongside the OpenAPI document (FR-060); an exhaustive, published table of exit
codes — the command-line projection of the error taxonomy (FR-065) — covered by
the project's semantic-versioning promise, so that removing or renumbering a code
is a breaking change; a guaranteed non-interactive mode, where no command
prompts and none requires a terminal; and `--wait` on every command that starts
a task, blocking until the task reaches a terminal state and exiting on its
outcome.
*Acceptance:* every reporting command accepts `--output json` and emits a
document validating against its published schema; the exit-code table is
generated from the code and a test fails if a code exists that the table does not
list, or the converse; a scripted UC2 run with no TTY and no prompt completes;
`--wait` on a synchronization trigger returns only once the task is terminal, and
its exit code reflects the task outcome.

### 3.8 Authentication and authorization

**FR-070 — OIDC authentication.**
Tobby SHALL support OpenID Connect (authorization code flow) against a configurable
identity provider.
*Acceptance:* login via a standard OIDC provider (tested against a reference IdP)
establishes an authenticated session; group/role claims map to RBAC roles (FR-074).
*(ADR-0009)*

**FR-071 — SAML authentication.**
Tobby SHALL support SAML 2.0 (SP-initiated) against a configurable identity provider.
*Acceptance:* login via a reference SAML IdP establishes an authenticated session
with attribute-based role mapping. *(ADR-0009)*

**FR-072 — Static token authentication.**
Tobby SHALL support static bearer tokens for API automation, with per-token role
assignment and revocation; stored token material SHALL be hashed, never kept in
plaintext at rest.
*Acceptance:* a valid token authorizes API calls per its role; a revoked token is
rejected immediately; token stores and configuration dumps contain only hashes.
*(ADR-0009)*

**FR-073 — Basic authentication.**
Tobby SHALL support HTTP basic authentication with locally managed accounts.
*Acceptance:* basic-auth credentials grant UI and API access per the assigned role;
passwords are stored only as salted hashes. *(ADR-0009)*

**FR-074 — Minimal RBAC.**
Tobby SHALL enforce three roles: `viewer` (read-only), `operator` (trigger and manage
synchronizations), `admin` (full configuration, certificates, secrets, tokens).
*Acceptance:* a permission matrix (role × endpoint) is documented and enforced;
negative tests confirm each role's denials. *(ADR-0009)*

**FR-075 — Authentication default and opt-out.**
Authentication SHALL be enabled by default in both modes, with locally managed
basic authentication (FR-073) as the out-of-the-box method, replaceable by
configuration with OIDC, SAML, or static tokens (FR-070–072). It MAY be disabled
only through an explicit configuration override; disabling is never silent: the
instance SHALL report the unauthenticated state prominently at startup, in logs
and the audit log (FR-094), and as a persistent UI banner. Security-reducing
settings SHALL always be
explicit opt-in — no configuration path relaxes security implicitly.
*Acceptance:* a default configuration rejects anonymous access and serves a basic
authentication login; with the override set, access is open and a warning appears
in logs and as a persistent UI banner; no default or omitted setting results in
an unauthenticated instance. *(ADR-0009)*

**FR-076 — Registry authentication compatible with `docker login`.**
The embedded registry SHALL authenticate clients using standard registry auth (basic
credentials and/or bearer token flow) such that `docker login`, `podman login`,
`helm registry login`, and `oras login` work unmodified, honoring RBAC (pull:
`viewer`+, push: `operator`+).
*Acceptance:* each client logs in and pulls/pushes according to its role; anonymous
access follows the FR-075 setting. *(ADR-0009)*

**FR-077 — Self-service password change. *(amendment 2026-08-12)***
Any locally authenticated account SHALL be able to change its own password
through the UI and through the API (FR-061 parity), providing the current
password; the change SHALL be audit-logged (FR-094) on success and on failure.
Password storage follows FR-073 and NFR-015 (salted hashes only, no secret in
any log or response).
*Acceptance:* a viewer changes their own password after providing the current
one and signs in with the new password; a wrong current password is refused
with a stable error code and produces an audit record; the API mirror behaves
identically.

### 3.9 Network

**FR-080 — Authenticated proxy support.**
Tobby SHALL support HTTP and HTTPS forward proxies, including proxies requiring
authentication, for all outbound traffic, configurable globally and honored by every
fetch path.
*Acceptance:* with direct egress blocked, all UC1 traffic flows through an
authenticated test proxy; proxy credentials never appear in logs.

**FR-081 — Custom certificate authorities.**
Tobby SHALL trust additional CA certificates supplied by configuration (for
registries and proxies outside any public PKI), without disabling TLS verification.
*Acceptance:* a registry presenting a certificate from a private CA is reachable once
the CA is configured; there is no global “skip TLS verify” switch in production
configuration.

**FR-082 — Server TLS management.**
Tobby SHALL serve its UI, API, and embedded registry over TLS with an
administrator-supplied certificate and key, and SHALL generate a self-signed
certificate as a fallback when none is supplied.
*Acceptance:* supplying a certificate/key pair makes the listener present it;
without one, a self-signed certificate is generated and its fingerprint logged;
certificate replacement is possible via configuration and the admin UI (FR-062).

### 3.10 Observability

**FR-090 — Structured logging.**
Tobby SHALL emit structured JSON logs — to stdout in passthrough mode, to a
configurable file (on the transport store by default) in mirror mode — with level,
timestamp, and correlation fields (run ID, task ID, recipe, ingredient, digest).
Every synchronization run SHALL be assigned a unique run ID at start, carried by
every log record of the run; the run ID is recorded in the media manifest
(FR-054) and reused by the destination-side instance when it processes the
transported store, so one run is traceable end to end across the air gap.
*Acceptance:* log output parses as JSON Lines; a given synchronization is fully
reconstructable by filtering on its task ID, and end to end — including
destination-side operation — by filtering on its run ID. *(ADR-0012)*

**FR-091 — OpenMetrics endpoint.**
Tobby SHALL expose metrics in OpenMetrics format on `/metrics`, covering at minimum:
synchronization counts and durations, per-status ingredient counts, transferred
bytes, policy rejections, and scan results.
*Acceptance:* the endpoint is scrapeable by Prometheus; each listed metric family is
present and documented. *(ADR-0012)*

**FR-092 — Health probes.**
Tobby SHALL expose `/healthz` (liveness) and `/readyz` (readiness, false until
storage and configuration are usable).
*Acceptance:* `/readyz` returns non-200 until the instance can serve; both probes are
usable as Kubernetes probes in the reference deployment. *(ADR-0012)*

**FR-093 — Graceful shutdown.**
On SIGTERM/SIGINT, Tobby SHALL stop accepting new work, finish or checkpoint
in-flight transfers within a configurable grace period, flush logs, and exit 0.
*Acceptance:* terminating during a synchronization leaves the store consistent and
the run resumable (FR-029); no partially written manifest is served afterwards.
*(ADR-0012)*

**FR-094 — Security audit log.**
Tobby SHALL emit a dedicated category of security audit events with a stable
schema: actor (authenticated identity, or the unauthenticated context under the
FR-075 override), action, target, outcome, timestamp, and origin (client address
or local invocation). The category SHALL cover, as the corresponding features
ship: authentication successes and failures, account and token lifecycle
operations, sensitive configuration changes, and audited overrides (FR-046,
FR-054). Audit events are structured log records on the FR-090 channels (stdout
in passthrough mode, the transport-store log file in mirror mode),
distinguishable from operational logs by a stable marker field; the audit log is
operational evidence, not a trust anchor — it is not signed. The event schema is
versioned with the same compatibility discipline as the REST API.
*Acceptance:* each implemented event class produces a record carrying all six
schema fields; audit records are separable from operational logs by a single
filter on the marker field; the documented schema is stable across the 1.x
series. *(ADR-0012)*

---

## 4. Non-functional requirements

### 4.1 Distribution and supply chain

**NFR-001 — Single static binary.**
Tobby SHALL be delivered as a single statically linked binary (CGO disabled) for
Linux and Windows, on amd64 and arm64. macOS binaries (amd64 and arm64) SHALL
additionally be published as a convenience tier through the same reproducible
release chain — SBOM, provenance, and checksums included — and distributed via
a Homebrew tap; they carry Go's deterministic ad-hoc code signature and are
covered by the full unit and integration test suite on macOS runners in CI,
but macOS is outside the validated operating scope (NFR-018): production
deployments remain Linux (server) and Windows (mirror workstation).
*Acceptance:* the four Linux/Windows release binaries run on clean hosts with no
runtime dependencies; `file`/`ldd` confirm static linking on Linux; the two
macOS binaries install through the Homebrew formula and pass `tobby version`
on both architectures. *(ADR-0011; amendment 2026-08-12)*

**NFR-002 — Minimal, zero-CVE container image.**
The container image SHALL be built from a minimal base with a zero-known-CVE
objective at release time.
*Acceptance:* a Trivy scan of each release image reports zero known CVEs at build
date; the image contains no shell or package manager. *(ADR-0011)*

**NFR-003 — Embedded assets.**
All UI assets (templates, stylesheets, translations) SHALL be embedded in the binary
via `go:embed`; the binary SHALL be fully functional with no adjacent files.
*Acceptance:* running the bare binary from an empty directory serves the complete UI.
*(ADR-0010)*

*Amendment 2026-08-11 (R-05) — embedded offline documentation.* The embedded
assets SHALL include operations guides for both modes and a troubleshooting
guide, in English and French, served by the instance itself under `/help` and
reachable with no outbound connection (NFR-019) — the destination zone is
air-gapped by definition, so documentation that lives on a website is
documentation the operator who needs it most cannot read. Screens SHOULD link
into the relevant section contextually, and every error code (FR-065) SHOULD
link to its entry.
*Acceptance:* an instance started with no network serves the complete guides in
both languages; a link check over the embedded corpus finds no dangling target;
error codes rendered in the UI carry a working link to their troubleshooting
entry.

**NFR-004 — Reproducible builds.**
Release builds SHALL be reproducible: two builds of the same tag on independent
machines produce bit-identical binaries.
*Acceptance:* an independent rebuild (documented procedure) yields matching SHA-256
checksums. *(ADR-0011)*

**NFR-005 — SLSA Build L3 provenance.**
Every release SHALL be built by a hardened, isolated build pipeline producing signed
SLSA Build L3 provenance, verifiable by consumers.
*Acceptance:* `slsa-verifier` validates the provenance of each release artifact
against the source repository and tag. *(ADR-0011)*

**NFR-006 — SBOM.**
Every release SHALL ship a signed CycloneDX SBOM for each binary and image.
*Acceptance:* the SBOM validates against the CycloneDX schema, lists all Go module
dependencies, and its signature verifies with the project key. *(ADR-0011)*

### 4.2 Performance

**NFR-007 — Blob streaming.**
Tobby SHALL stream blobs end to end (download, verify digest, store, upload) without
loading entire blobs in memory, supporting images of several gigabytes.
*Acceptance:* transferring a multi-GB image keeps process RSS below a documented
bound (independent of blob size); digests are verified on the fly.

**NFR-008 — Bounded parallelism.**
Tobby SHALL process multiple Ingredients concurrently with a configurable upper bound
on parallel transfers.
*Acceptance:* with bound N, at most N transfers are in flight (observable in
metrics); raising N on a multi-Ingredient Recipe reduces wall-clock time.

### 4.3 Reliability

**NFR-009 — Idempotent synchronization.**
Re-running any synchronization against an unchanged desired state SHALL produce no
content changes and no errors.
*Acceptance:* a second identical run reports all Ingredients `up-to-date`, transfers
zero bytes of blob data, and exits successfully.

**NFR-010 — Crash resilience.**
Interruptions (crash, power loss, media removal) SHALL never corrupt the store;
incomplete uploads SHALL be invisible to registry clients and recoverable.
*Acceptance:* fault-injection tests (kill -9 at random points) leave a store that
passes integrity verification and completes on the next run (FR-029).

### 4.4 Security

**NFR-011 — Path traversal resistance.**
All file-handling paths (storage backend, `FileSet` extraction, layout import) SHALL
reject path traversal and absolute-path escapes.
*Acceptance:* a malicious archive/layout containing `../` or absolute entries is
rejected; dedicated unit tests cover the attack corpus.

**NFR-012 — CSRF protection.**
All state-changing UI endpoints SHALL be protected against cross-site request
forgery.
*Acceptance:* state-changing requests without a valid CSRF token are rejected;
covered by automated tests.

**NFR-013 — No unsafe HTML rendering.**
The UI SHALL only render escaped output; any rendering of remote content (e.g.,
Recipe descriptions, chart READMEs) SHALL be sanitized — no raw/unsafe HTML mode.
*Acceptance:* an XSS corpus injected through Recipe metadata and artifact
documentation renders inert; templates use context-aware auto-escaping.

**NFR-014 — Least privilege.**
Tobby SHALL run as an unprivileged user (non-root container, no capabilities) with
write access restricted to its storage and log paths.
*Acceptance:* the reference deployment runs with a read-only root filesystem,
non-root UID, and all capabilities dropped, and passes the e2e suite.

**NFR-015 — Secret hygiene.**
Secrets (registry credentials, tokens, private keys, proxy passwords) SHALL never
appear in logs, error messages, API responses, or configuration dumps. Browser
session cookies SHALL carry the `Secure`, `HttpOnly`, and `SameSite` attributes;
passwords and static tokens SHALL be stored only as salted hashes (FR-072,
FR-073).
*Acceptance:* a log/response scan across the full e2e suite with known planted
secrets finds zero occurrences; redaction is unit-tested; session cookie
attributes are asserted in the e2e suite. *(ADR-0009)*

**NFR-020 — Secrets never travel. *(amendment 2026-08-11, R-16)***
Files holding secrets — registry credentials (`dockerconfigjson`), TLS private
keys, proxy passwords, static tokens, the local user database — SHALL NOT reside
inside the transportable store (FR-050). Tobby SHALL verify this at startup and
SHALL refuse to start when a configured secret path resolves under the store
root, naming the offending path and the reason. Secret files SHALL be created
with restrictive permissions — mode 0600 on Unix; on Windows an ACL granting the
owning account only, documented with the feature matrix (NFR-018). The store is
handed to a courier and plugged into a machine in another zone: anything under it
is assumed to be read by someone else.
*Acceptance:* an instance configured with a credentials file under the store root
refuses to start, naming the path; a planted-secret corpus placed under the store
is detected by the startup check; created secret files carry the documented
permissions; the e2e suite scans a produced medium for the planted secrets and
finds none.

### 4.5 Maintainability

**NFR-016 — Code quality gates.**
The codebase SHALL maintain a test coverage target of at least 70% on core packages
(fetch engine, policy, registry, recipe handling) and a clean lint status
(`golangci-lint` with the project profile) enforced in CI.
*Acceptance:* CI blocks merges below the coverage threshold or with lint findings;
coverage is published per release.

### 4.6 Usability and accessibility

**NFR-017 — UI accessibility and quality.**
The web UI SHALL target WCAG 2.1 level AA (keyboard navigation, contrast, labels,
focus states) and follow a consistent design system across all screens.
*Acceptance:* automated accessibility checks (e.g., axe) report no AA violations on
the FR-062 screens; a manual keyboard-only pass completes UC1 and UC2 flows.
*(ADR-0010)*

### 4.7 Platform scope

**NFR-018 — Windows functional scope.**
On Windows, Tobby SHALL support at least the complete mirror-mode workflow —
manual synchronization (FR-014), the self-contained transportable store on fixed
and removable media (FR-050), file-based logging on the transport store (FR-053),
OCI image layout export/import (FR-051) — and destination-side operation
(FR-052), including the embedded registry and the web UI/API needed for those
flows. Passthrough mode is delivered and validated as a containerized Linux
service; the single Windows binary (NFR-001) can run it, but passthrough on
Windows is outside the v1.0.0 validated scope.
*Acceptance:* the UC2 end-to-end scenario (synchronize → transport → destination
push) passes on a Windows CI runner — beyond binary smoke tests; the
documentation states the supported feature matrix per operating system.
macOS appears in that matrix as a convenience tier (NFR-001): the full test
suite runs on macOS in CI, but no end-to-end operating scenario is validated
on it and no production support is implied.

### 4.8 Network posture

**NFR-019 — No unconfigured outbound connections.**
Tobby SHALL make no network connection that was not explicitly configured by the
operator (source and destination registries, IdP, proxy). There is no usage
telemetry, crash reporting, update checking, or any other implicit outbound
call; any future diagnostics bundle is a local file export the operator chooses
to share.
*Acceptance:* a full e2e run behind an egress-capturing proxy records
connections to configured endpoints only; the crucible air-gap canary
(ADR-0014) proves zero egress in mirror scenarios. *(ADR-0012)*

---

## 5. Out of scope (v1.0)

### 5.1 Destination registry purge

Removing obsolete content from destination registries is **not** performed by Tobby.
It is a downstream ecosystem concern, implemented as a mark-and-sweep over the
environment's Cookbook: enumerate every Ingredient referenced by every Recipe of the
environment, delete everything else. Tobby's obligations are limited to not
obstructing this process — in particular by exposing complete standard listings
(FR-043) and by propagating Recipes alongside artifacts (FR-034) so the reference
set is always available in-zone.

### 5.2 Generic static file server

Serving arbitrary static files over HTTP **with an upload surface** — a generic
upload-and-serve file server — is excluded from v1.0: ad-hoc uploads
would bypass the recipe model and its verification chain entirely. The need is
split and covered on both halves without that bypass: *distributing* files is
the `FileSet` ingredient kind — files packaged as an OCI image, signed and
digest-pinned like any ingredient, mountable as a Kubernetes image volume or
extractable with standard tooling; *serving* them is FR-047, which exposes
verified FileSet contents read-only over HTTP — sufficient for OS package
repositories (apt/rpm) and bare-host bootstrap, with no unverified write path
reopened. For the operator-side need of serving a handful of local files
simply (air-gapped bootstrap, one-off documents), the sanctioned path is
FR-048 *(amendment 2026-08-26)*: pack them into a digest-pinned FileSet
imported through the store and served read-only by FR-047 — the convenience
of an upload, without a mutable HTTP write surface.

### 5.3 Qualification pipeline orchestration

Tobby does not build, test, scan-for-qualification, sign, or otherwise qualify
assets, and does not orchestrate CI/CD pipelines. It consumes the *output* of the
organization's qualification pipeline (signed, digest-pinned Recipes in a Cookbook)
and transfers it. Verification performed by Tobby (FR-031, FR-033) is a transport
safeguard, not a qualification step.

### 5.4 Zone state publication after a destination push

Beyond pushing the transported content and the cooked Recipes themselves (FR-034,
FR-052), Tobby does not maintain or republish a separate "zone state" document —
for example an updated Retriever reflecting what the zone now holds — after a
destination-side push. The destination zone's Cookbook, populated by FR-034, is
the authoritative, queryable record of the zone's content; any bookkeeping built
on top of it (inventory, reconciliation, purge eligibility — section 5.1) is a
downstream ecosystem concern. As with the purge, Tobby's obligation is not to
obstruct: Recipes are propagated with their signatures (FR-034), registry
listings are standard (FR-043), and the transported store carries the complete
operation logs of the run (FR-053).

---

## Appendix A — ADR index

| ADR | Title |
|---|---|
| ADR-0001 | Recipe format: Kubernetes-style YAML manifests, versioned API group |
| ADR-0002 | Recipes as OCI artifacts; cookbook = OCI repository |
| ADR-0003 | Repository & licensing split: recipe-spec (Apache-2.0) / tobby (GPL-3.0) |
| ADR-0004 | Embedded OCI registry: CNCF distribution v3 as a library |
| ADR-0005 | Direct-to-storage import (no self-push loopback) |
| ADR-0006 | Removable-media transport: self-contained store + OCI image layout export |
| ADR-0007 | Signing & verification: Sigstore/cosign, key-based for air-gap |
| ADR-0008 | Vulnerability scanning: Trivy, offline DB shipped as OCI artifact |
| ADR-0009 | Authentication: OIDC, SAML, static tokens, basic auth; minimal RBAC |
| ADR-0010 | Web UI: Go server-rendered + htmx, no Node toolchain, i18n, themable CSS |
| ADR-0011 | Tobby's own supply chain: SLSA Build L3, melange+apko, reproducible builds |
| ADR-0012 | Observability & operations: structured logs, OpenMetrics, health probes |
| ADR-0013 | Ingredient relocation and destination naming |
| ADR-0014 | Crucible test infrastructure: Incus |
