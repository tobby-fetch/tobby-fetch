---
title: Zone Retriever and cascade
description: The desired-state document that drives a zone, and how zones chain — downstream fetching from upstream without a single recipe changing.
sidebar:
  order: 4
---

A zone is driven by one document: its **Retriever**. It lists, by name
and version constraint, the recipes the zone should hold, and names the
cookbook to resolve them from. The instance re-reads it at every
synchronization; changing what a zone contains means changing this
document — or publishing a new recipe version that an existing
constraint already covers. The document format is normative on the
recipe-spec site: see the
[Retriever specification](https://tobby-fetch.github.io/recipe-spec/)
(the `Retriever` kind, `recipe.tobby.dev/v1alpha1`). A complete
commented example ships in the repository as
[`examples/retriever.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/retriever.yaml).

## Three sources

`retriever.source` accepts three forms (FR-010):

| Form | Example | When |
| --- | --- | --- |
| Local file | `/etc/tobby/retriever.yaml` | The document is managed with the instance's configuration. |
| HTTP(S) URL | `https://git.example.com/platform/retriever.yaml` | The document lives in a Git repository or any web server — the common GitOps arrangement. |
| OCI reference | `oci://registry.example.com/config/retriever:v1` | The document travels like the content it describes — including across zones, carried by Tobby itself. |

The configured source is reported as configured on the **Retriever**
administration screen (`/admin/retriever`, admin role) and its API
mirror `GET /api/v1/retriever` — alongside the declared relaxed trust
scopes and the effective synchronization interval. The interval override
lives on the same screen (`PUT /api/v1/retriever/interval`, and `DELETE`
to return to the configured value); it persists in the state directory,
survives restarts, wins over `sync.interval`, and is audited as a
sensitive configuration change (FR-094).

![The retriever administration screen: configured source, interval and its runtime override](../../../../assets/docs/admin-retriever.png)

At each cycle the instance resolves every listed recipe from the
cookbook, verifies its signature against the configured trust roots
(FR-033), and reconciles. Version constraints are resolved at each pass;
if no published version satisfies a constraint, that entry fails and
says so — the other entries carry on. One unpublishable recipe never
blocks the zone.

## The cascade: connected → restricted → more restricted

<svg viewBox="0 0 640 226" role="img" aria-label="Three chained zones, each with a Tobby instance that re-verifies on entry and promotes into its zone registry; the same signed recipes flow from zone A to zone B to zone C, and the relocated path is identical in every zone" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="rc-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- zones -->
  <rect x="8" y="30" width="196" height="150" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="222" y="30" width="196" height="150" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="436" y="30" width="196" height="150" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="106" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone A — upstream</text>
  <text x="320" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone B — downstream</text>
  <text x="534" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone C — further down</text>
  <!-- tobby instances -->
  <rect x="38" y="44" width="136" height="42" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="106" y="61" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="106" y="76" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">re-verifies on entry</text>
  <rect x="252" y="44" width="136" height="42" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="320" y="61" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="320" y="76" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">re-verifies on entry</text>
  <rect x="466" y="44" width="136" height="42" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="534" y="61" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="534" y="76" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">re-verifies on entry</text>
  <!-- zone registries -->
  <rect x="28" y="116" width="156" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="106" y="133" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">zone registry</text>
  <text x="106" y="148" text-anchor="middle" font-size="8.5" font-family="monospace" fill="var(--sl-color-gray-3)">…/docker.io/bitnami/wordpress</text>
  <rect x="242" y="116" width="156" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="320" y="133" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">zone registry</text>
  <text x="320" y="148" text-anchor="middle" font-size="8.5" font-family="monospace" fill="var(--sl-color-gray-3)">…/docker.io/bitnami/wordpress</text>
  <rect x="456" y="116" width="156" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="534" y="133" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">zone registry</text>
  <text x="534" y="148" text-anchor="middle" font-size="8.5" font-family="monospace" fill="var(--sl-color-gray-3)">…/docker.io/bitnami/wordpress</text>
  <!-- flows -->
  <line x1="106" y1="86" x2="106" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <line x1="320" y1="86" x2="320" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <line x1="534" y1="86" x2="534" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <line x1="174" y1="65" x2="248" y2="65" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <text x="211" y="57" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">recipes</text>
  <line x1="388" y1="65" x2="462" y2="65" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <text x="425" y="57" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">recipes</text>
  <!-- captions -->
  <text x="320" y="202" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-2)">The same signed recipes flow down unmodified — each instance re-verifies against its own trust roots</text>
  <text x="320" y="219" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">Relocated paths are invariant: the same …/docker.io/… path in every zone, however many hops</text>
</svg>

Real topologies chain zones. The upstream zone promotes into its
registry; the downstream zone's Tobby fetches **from that registry**
even though the recipes — immutable, signed, bit-exact — keep naming the
origin hosts (`docker.io/...`). The bridge is **source substitution**
(FR-036):

```yaml
# Downstream instance
registries:
  substitutions:
    docker.io: registry.upstream.example/docker.io
    ghcr.io: registry.upstream.example/ghcr.io

retriever:
  source: oci://registry.upstream.example/cookbook/retriever:v1
```

Substitution changes **only the network endpoint contacted — never the
computed destination path** (FR-035). That invariance is what makes the
cascade compose: `docker.io/bitnami/wordpress` relocates to
`<registry>/docker.io/bitnami/wordpress` in *every* zone, however many
hops it crossed, and never degenerates into
`reg.zone2/reg.zone1/docker.io/...`. Each zone's registry holds the same
relocated paths under its own host, and each zone's cookbook — populated
by recipe propagation (FR-034) — is what the next zone's Retriever
points at. The rule and its rationale are
[ADR-0013](../../reference/srs-adr/); the normative grammar (host
canonicalization, port encoding, substitution semantics) is
[RECIPE-SPEC §11.5](https://tobby-fetch.github.io/recipe-spec/).

Two policies deliberately read different references:

- The **registry allowlist** (FR-030) and **credential lookup** apply to
  the *effective* host actually contacted — the substitute. That is
  where the bytes come from, so that is what network policy must name.
- **Trust-root scopes** (FR-033) match the *nominal* `ref` written in
  the recipe. Signed provenance does not change because content was
  fetched from a closer copy.

Logs record the nominal→effective mapping on every substituted fetch, so
an audit can always answer both "what was this content" and "where did
these bytes come from".

## Credentials between instances

A downstream instance authenticates to its upstream like any registry
client. Create a dedicated account (or token) on the upstream instance —
see [authentication and RBAC](../../security/auth-rbac/) — with read as its
only need, and hand it to the downstream instance through its
credentials file (FR-004):

```yaml
registries:
  credentialsFile: /etc/tobby-credentials/config.json
```

The file is a standard `dockerconfigjson` payload; entries are looked up
by the **effective** host, so the entry names the upstream registry —
`registry.upstream.example` — not `docker.io`. It must live outside the
store (secrets never travel on transportable media), and on Kubernetes
it is a mounted Secret the chart wires for you. Write-scoped credentials
follow the same path: pushing to the zone's destination registry uses
the same credentials file, keyed by the destination host.

The destination side of the same instance is its own configuration
section — `destination.registry`, plus `destination.basePath` and
`destination.cookbook` — deliberately separate from substitutions: one
answers "where do I read from", the other "where do I promote to", and
applying a read-side rewrite to a write would publish to a registry
nobody named.

Next: your clusters and hosts consume what landed —
[connect your clients](../../passthrough/connect-clients/).
