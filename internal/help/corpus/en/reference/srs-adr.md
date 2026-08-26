---
title: SRS and ADRs
description: Index of the requirements specification and the architecture decision records, the source hierarchy between them, and the ADR process.
sidebar:
  order: 6
---

Two documents govern what Tobby is required to do and why it is built the
way it is: the **Software Requirements Specification** (SRS) and the
**Architecture Decision Records** (ADRs). Both live in the repository —
the canonical text is there, not here. This page is a rendered index; a
full in-site HTML rendering of the SRS and the ADRs is a later
documentation enhancement.

## The SRS

**Canonical text:**
[docs/SRS.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/SRS.md)
in the repository.

The SRS is the reference for design, implementation, testing and acceptance
of Tobby v1.0.0. Its structure:

1. **Introduction** — purpose, scope, the two operating modes, definitions.
2. **Overall description** — product perspective, user classes, an
   illustrative Recipe (the normative schema lives in
   [recipe-spec](https://tobby-fetch.github.io/recipe-spec/)).
3. **Functional requirements** — `FR-xxx`, grouped by theme (modes and
   configuration, acquisition, media, promotion, UI/API, authentication,
   operations…).
4. **Non-functional requirements** — `NFR-xxx` (performance, security,
   portability, quality).
5. **Out of scope (v1.0)** — explicit non-goals, each with its rationale.
6. **Appendix A** — the ADR index.

### How requirements are written and traced

- Every requirement has a stable number (`FR-035`, `NFR-015`), is written
  as a **testable statement**, and is paired with a short **acceptance
  criterion**.
- Requirement numbers are cited throughout the code and its tests: the
  packages that implement a requirement name it in their documentation
  comments, notable log lines carry a `requirement` field, and structural
  tests (the RBAC anti-drift test, the OpenAPI cross-check, the egress
  canary) are written against the requirement they prove. `git grep FR-035`
  in the repository is a working traceability query.
- Amendments are dated in place (e.g. *"FR-005 — amendment 2026-08-12"*),
  so a requirement's history is readable in its own text.

How the requirements pyramid maps onto executable proof is covered in
[Tests and quality proofs](../../project/tests-and-proofs/).

## Source hierarchy

The two documents answer different questions and are ordered:

- The **SRS states what Tobby must do** — it is the requirements authority,
  and acceptance is judged against it.
- An **ADR records how and why a decision was made** to satisfy those
  requirements. An ADR never overrides a requirement: when a decision
  requires the requirement itself to change, the SRS is amended (dated, in
  place) and the ADR references the amendment.
- Where the shipped code and either document disagree, that is a defect to
  be resolved — the documentation never papers over it.

## The ADR process

ADRs follow the Nygard format — *Status / Context / Decision /
Consequences / Alternatives considered* — and three rules
([docs/adr/README.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/README.md)):

- **Birth** — ADRs are numbered in the order the decisions were made. A
  direction that is settled and already referenced by other design
  documents while the full record is being written carries the **Drafting**
  status.
- **Arbitration and amendment** — an **Accepted** ADR is in force. It may
  be amended *in place* for additive or corrective changes that do not
  reverse the decision; the Status line then records the amendment date and
  scope.
- **Supersession** — a reversal is never an amendment: it gets a new ADR
  that supersedes the old one. The superseded ADR keeps its file with a
  **Superseded by ADR-XXXX** status. ADRs are never deleted.

## ADR index

All records are **Accepted**. Each link opens the canonical text on GitHub.

| ADR | Title | Date | Summary |
|---|---|---|---|
| [ADR-0001](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0001-recipe-format-k8s-style.md) | Recipe format: Kubernetes-style YAML manifests, versioned API group | 2026-07-11 | Recipes are declarative YAML manifests with a versioned API group, familiar to Kubernetes operators and validatable by schema. |
| [ADR-0002](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0002-recipes-as-oci-artifacts.md) | Recipes as OCI artifacts; cookbook = OCI repository | 2026-07-11, amended 2026-08-04 | A recipe is published as an OCI artifact; a cookbook is an OCI repository — signatures, transport and versioning ride the registry ecosystem. Amended to align signature transport on the cosign 3.x default. |
| [ADR-0003](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0003-repo-and-licensing-split.md) | Repository & licensing split: recipe-spec (Apache-2.0) / tobby (GPL-3.0) | 2026-07-11, amended 2026-08-04 | The recipe format and its SDK are Apache-2.0 so anyone can implement them; the application is GPL-3.0. |
| [ADR-0004](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0004-embedded-registry-cncf-distribution.md) | Embedded OCI registry: CNCF distribution v3 as a library | 2026-07-11 | The instance embeds CNCF distribution as its store and standard `/v2/` surface rather than fronting an external registry. |
| [ADR-0005](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0005-direct-to-storage-import.md) | Direct-to-storage import (no self-push loopback) | 2026-07-11 | Imports write into the store directly instead of pushing to the instance's own HTTP registry endpoint. |
| [ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md) | Removable-media transport: self-contained store + OCI image layout export | 2026-07-11 | The air-gap crossing is the store itself, self-contained and transportable, with an interoperable OCI image-layout export. |
| [ADR-0007](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0007-signing-cosign-key-based.md) | Signing & verification: Sigstore/cosign, key-based for air-gap | 2026-07-11 | Key-based cosign signatures, verifiable without any online service — the form that works in an isolated zone. |
| [ADR-0008](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0008-vulnerability-scanning-trivy.md) | Vulnerability scanning: Trivy, offline DB shipped as OCI artifact | 2026-07-11, amended 2026-08-04 and 2026-08-12 | Scanning with Trivy as a pinned external binary; the CVE database crosses the air gap as an OCI artifact carried by Tobby itself. |
| [ADR-0009](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0009-authentication-oidc-saml-tokens-basic.md) | Authentication: OIDC, SAML, static tokens, basic auth; minimal RBAC | 2026-07-11 | Four authentication methods over one minimal three-role RBAC; the same accounts serve UI, API and registry clients. |
| [ADR-0010](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0010-ui-server-rendered-htmx.md) | Web UI: Go server-rendered + htmx, no Node toolchain, i18n, themable CSS | 2026-07-11 | The UI is rendered by the Go binary with htmx interactivity — zero Node at build and runtime, bilingual, themable by stylesheet override. |
| [ADR-0011](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0011-supply-chain-slsa-l3-apko.md) | Tobby's own supply chain: SLSA Build L3, melange+apko, reproducible builds | 2026-07-11, amended 2026-08-04 | How Tobby is itself built and attested: reproducible builds, melange/apko images, SLSA Build L3, license-compliance gate and OpenVEX. |
| [ADR-0012](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0012-observability.md) | Observability & operations: structured logs, OpenMetrics, health probes | 2026-07-11 | JSON logs with stable correlation keys, an OpenMetrics endpoint, liveness/readiness probes and graceful shutdown. |
| [ADR-0013](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0013-ingredient-relocation-destination-naming.md) | Ingredient relocation and destination naming | 2026-08-03 | The deterministic naming convention: `docker.io/x` becomes `<zone-registry>/docker.io/x`, canonical hosts, port `:` written `_`. |
| [ADR-0014](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0014-crucible-test-infrastructure-incus.md) | Crucible test infrastructure: Incus | 2026-08-04 | The acceptance crucible: real multi-zone topologies on Incus, scenarios per milestone, raw reports kept. |
| [ADR-0015](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0015-ui-invariants.md) | Web UI invariants: canonical URLs, reserved prefixes, htmx doctrine | 2026-08-12 | The rules every UI contribution applies: canonical URL scheme with the `/-/` separator, reserved path prefixes, and the htmx usage doctrine. |

The requirements ↔ mechanisms ↔ proofs mapping built on top of these
documents is in [Compliance](../../project/compliance/), and the acceptance
evidence in [Acceptance reports](../../project/acceptance-reports/).
