# Architecture Decision Records

This directory contains the Architecture Decision Records (ADRs) for the
Tobby project, in the [Nygard format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions):
*Status / Context / Decision / Consequences / Alternatives considered*.

ADRs are numbered in the order the decisions were made and are never deleted;
a superseded ADR keeps its file with an updated status pointing to its
successor.

## Index

| ADR | Title | Status |
|---|---|---|
| [ADR-0001](ADR-0001-recipe-format-k8s-style.md) | Recipe format: Kubernetes-style YAML manifests, versioned API group | Accepted |
| [ADR-0002](ADR-0002-recipes-as-oci-artifacts.md) | Recipes as OCI artifacts; cookbook = OCI repository | Accepted |
| [ADR-0003](ADR-0003-repo-and-licensing-split.md) | Repository & licensing split: recipe-spec (Apache-2.0) / tobby (GPL-3.0) | Accepted |
| [ADR-0004](ADR-0004-embedded-registry-cncf-distribution.md) | Embedded OCI registry: CNCF distribution v3 as a library | Accepted |
| [ADR-0005](ADR-0005-direct-to-storage-import.md) | Direct-to-storage import (no self-push loopback) | Accepted |
| [ADR-0006](ADR-0006-removable-media-transport.md) | Removable-media transport: self-contained store + OCI image layout export | Accepted |
| [ADR-0007](ADR-0007-signing-cosign-key-based.md) | Signing & verification: Sigstore/cosign, key-based for air-gap | Accepted |
| [ADR-0008](ADR-0008-vulnerability-scanning-trivy.md) | Vulnerability scanning: Trivy, offline DB shipped as OCI artifact | Accepted |
| [ADR-0009](ADR-0009-authentication-oidc-saml-tokens-basic.md) | Authentication: OIDC, SAML, static tokens, basic auth; minimal RBAC | Accepted |
| [ADR-0010](ADR-0010-ui-server-rendered-htmx.md) | Web UI: Go server-rendered + htmx, no Node toolchain, i18n, themable CSS | Accepted |
| [ADR-0011](ADR-0011-supply-chain-slsa-l3-apko.md) | Tobby's own supply chain: SLSA Build L3, melange+apko, reproducible builds | Accepted |
| [ADR-0012](ADR-0012-observability.md) | Observability & operations: structured logs, OpenMetrics, health probes | Accepted |
| [ADR-0013](ADR-0013-ingredient-relocation-destination-naming.md) | Ingredient relocation and destination naming | Accepted |
| [ADR-0014](ADR-0014-crucible-test-infrastructure-incus.md) | Crucible test infrastructure: Incus | Accepted |
| [ADR-0015](ADR-0015-ui-invariants.md) | Web UI invariants: canonical URLs, reserved prefixes, htmx doctrine | Accepted |

## Statuses

- **Accepted** — decision is in force. An accepted ADR may be **amended in
  place** for additive or corrective changes that do not reverse the decision;
  the Status line then records the amendment date and scope (e.g.
  "Accepted — 2026-07-11 · Amended 2026-08-04 (…)"). A reversal is never an
  amendment: it gets a new ADR that supersedes the old one.
- **Drafting** — decision direction is settled and referenced by other design
  documents; the full record is being written.
- **Superseded by ADR-XXXX** — replaced; kept for history.
