# ADR-0013 — Ingredient relocation and destination naming

## Status

Accepted — 2026-08-03

## Context

Tobby copies ingredients from source registries into its embedded store and into
zone registries, but no document defined **under which repository path an
ingredient lands at its destination**. ADR-0005 fixed a "canonical local name
(single source-registry prefix)" for the embedded store without defining it, and
neither the SRS nor the Recipe specification said anything for zone registries.

Everything downstream of a transfer depends on that rule:

- **Consumption.** Helm chart values and deployment manifests reference the
  *source* paths (`docker.io/bitnami/wordpress`); consumers in the zone must know
  the relocated path — or be given a registry-mirror configuration that hides it.
- **Cleanup.** The external mark-and-sweep purge (SRS §5.1) must correlate the
  `ref` fields of the zone's recipes with the actual content of the zone
  registry; without a deterministic mapping that correlation cannot be written.
- **Cascade.** In multi-hop topologies (connected → restricted → more restricted),
  each zone fetches from the previous zone's registry while recipes — immutable
  and bit-exact — keep naming the origin hosts.
- **Verification.** Digest comparison against the destination (FR-026/FR-028)
  presupposes a known destination path.

The rule must not weaken the trust model: digests and attached signatures pin
*content*, not location, and must remain verifiable wherever the content lands.

## Decision

**Ingredients are relocated under their nominal, canonicalized source host; the
relocated path is invariant across hops.** Normatively specified in
RECIPE-SPEC §11.5 (RECOMMENDED, for third-party interoperability) and enforced
for Tobby by SRS FR-035; the cascade mechanism is SRS FR-036; consumption aids
are SRS FR-065.

```
<destination>[/<base-prefix>]/<canonical-source-host>[_<port>]/<repository-path>
```

1. **Nominal host, not contacted host.** The prefix derives from the host written
   in the recipe's `ref`. Source substitution (mirrors, cascaded zone registries)
   changes only the network endpoint contacted — never the computed path. A
   literal "prefix with whatever host you pulled from" would double-prefix on a
   mirror of a mirror (`reg.zone2/reg.zone1/docker.io/...`), the exact bug class
   ADR-0005 documented in the POC.
2. **Host canonicalization, closed list.** Hosts are lowercased; the Docker Hub
   aliases `index.docker.io` and `registry-1.docker.io` canonicalize to
   `docker.io`; no other implicit normalization. Without this, two recipes naming
   the same content through different aliases would produce two locations, and
   mirror configurations keyed on the logical name `docker.io` would miss content
   stored under an alias.
3. **Port normalization.** `:<port>` becomes `_<port>` (colons are invalid in
   repository names). Unambiguous: a valid hostname cannot contain `_`. IPv6
   literal hosts are not relocatable and are rejected explicitly.
4. **No truncation, ever.** If a relocated name exceeds a destination's limits
   (e.g. 255 characters), the transfer fails explicitly, naming the limit.
5. **Destination compatibility is checked, not assumed.** Tobby probes the
   destination for nested-path support before pushing and fails with an explicit
   error otherwise. The product targets **any OCI Distribution-conformant
   registry** — CNCF distribution, Harbor, Artifactory, cloud SaaS registries —
   with no vendor prerequisite; known per-product preconditions (e.g. Harbor
   requires the top-level project to exist, Quay needs
   `FEATURE_EXTENDED_REPOSITORY_NAMES`) are documented in a support matrix.
6. **Two naming rules by design.** This convention covers *ingredients*. Recipe
   artifacts keep the cookbook convention (`<registry>/<cookbook>/<name>:<version>`,
   RECIPE-SPEC §11.3, FR-034): an ingredient's path encodes its *provenance*, a
   recipe's path encodes its *catalog location*.
7. **Policy follows the wire, trust follows the recipe.** With source
   substitution active, the registry allowlist (FR-030) and credential lookup
   (RECIPE-SPEC §13.2) apply to the **effective** host actually contacted;
   trust-root scopes (FR-033) match the **nominal** `ref` — signed provenance
   does not change when content is fetched from a closer copy. Logs record the
   nominal→effective mapping.
8. **Consumption aids, not values rewriting.** Tobby exposes the per-recipe
   source→destination mapping table (API/UI) and generates a K3s/RKE2
   `registries.yaml` mirror snippet (mirrors + rewrite rules) so that **runtime
   image pulls** need no values changes at all. Admission policies and GitOps
   chart sources do not go through containerd mirrors and must reference
   relocated paths explicitly, using the mapping table. Tobby never rewrites
   chart values (RECIPE-SPEC §7.2 deliberately requires no values evaluation).

## Consequences

- Given a recipe and a destination, the expected location of every ingredient is
  computable with no additional metadata: the client-side purge, third-party
  audits, and Tobby's own differential comparison all rely on the same pure
  function of (`ref`, destination, base prefix).
- Digest pinning and attached signatures are location-independent by
  construction (bit-exact copies, signatures copied into the relocated
  repository): relocation costs nothing in verifiability. Verifiers that match
  cosign's `critical.identity.docker-reference` against the *pulled* location
  will observe the origin reference instead; policy engines must be configured
  accordingly (documented with the consumption aids).
- Multi-hop topologies compose: every zone holds the same relocated paths under
  its own registry host, and a zone's Retriever points at the previous zone's
  cookbook without recipe modification.
- The embedded store and every destination share one layout; ADR-0005's
  "canonical local name" is now this convention.
- Nested repository paths (≥ 2 path components, first component containing dots)
  are a hard requirement on destination registries; the pre-push compatibility
  check turns incompatibilities into explicit, early errors instead of mid-push
  failures.

## Alternatives considered

### Flattened paths (`reg.zone/bitnami/wordpress`)

Shortest names, friendliest to registries with shallow namespaces. Rejected:
sources collide (`docker.io/foo/bar` vs `ghcr.io/foo/bar`), which is both a
correctness bug and a repository-confusion attack surface; and the mapping stops
being invertible, breaking purge correlation.

### Configurable per-ingredient or per-source mapping

Maximum flexibility for constrained destination namespaces. Rejected as the
primary mechanism: a configured mapping is another thing to get wrong per zone,
destroys cross-zone predictability (each zone could map differently), and turns
the purge correlation into configuration-dependent logic. The single optional
`base-prefix` covers the legitimate "one namespace for all of Tobby's content"
need without reopening per-item freedom.

### Rewriting chart values at transfer time

Would make charts deploy as-is without mirror configuration. Rejected: it
requires evaluating values semantics per chart (explicitly excluded from the
format, RECIPE-SPEC §7.2), breaks chart digests and signatures (same trade-off
as dependency vendoring, FR-025 / RECIPE-SPEC §14.6), and silently diverges
from upstream.
The mirror snippet plus the mapping table achieve the operational goal without
touching artifacts.
