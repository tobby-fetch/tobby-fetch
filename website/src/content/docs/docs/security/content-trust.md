---
title: Signatures, trust roots and allowlist
description: How trust roots are configured, what the relaxation scopes really allow, the two accepted cosign formats, and the registry allowlist semantics.
sidebar:
  order: 3
---

Tobby verifies cosign **key-based** signatures, fully offline — no
Fulcio, no Rekor, no online service (FR-033, ADR-0007 — delivered
v0.3.0). Keyless signing depends on services unreachable from restricted
zones, so it is not the model; the organization's own keys are.
Verification runs twice on every item: at import into the store, and
again before any push to a destination — so tampering at rest or in
transit is caught at the last gate, not just the first. On the
destination side of a physical transfer the same checks replay against
the transported store (FR-052).

Tobby never signs. Signing happens in the organization's qualification
pipeline; the [recipe-spec publishing guide](https://tobby-fetch.github.io/recipe-spec/guides/publishing-recipes/)
carries the verified cosign 3.x commands.

## Trust roots

A trust root is one trusted public key, named, configured on the instance
(`trust.roots`, configuration file only). Exactly one of three forms per
root:

```yaml
trust:
  roots:
    - name: org-release-key
      keyFile: /etc/tobby/keys/org-release.pub   # file on disk
    - name: platform-team
      key: |                                     # inline PEM
        -----BEGIN PUBLIC KEY-----
        ...
        -----END PUBLIC KEY-----
    - name: partner
      keyURL: https://pki.example.com/partner.pub # fetched at configuration
                                                  # time, never at verification time
```

Air-gapped instances use the inline or file forms. A `keyURL` is resolved
and cached when the configuration loads — verification itself never
touches the network (FR-033).

**Rotation by overlap** (ADR-0007): the roots are a set. To rotate a key,
add the new root alongside the old, re-sign the recipes upstream, then
remove the old root once nothing verified by it remains in flight. There
is no cutover instant during which verification would have to be relaxed.

## Relaxation scopes — and their visibility

Verification is on by default for every recipe. There is **no global
bypass**. The only relaxation is a declared scope (`trust.scopes`):
named, restricted to explicit repository patterns, evaluated in
declaration order with first match winning; no match means the strict
default.

```yaml
trust:
  scopes:
    - name: lab-experiments
      repositories: ["lab.example.com_5000/cookbook/*"]
      allowUnsigned: true          # reported on every surface, never silent
    - name: partner-cookbook
      repositories: ["partner.example.com/cookbook/**"]
      roots: [partner]             # restrict WHICH roots may verify here
```

Patterns match the recipe's *nominal* cookbook path in canonical form —
host lowercased, Docker Hub aliases folded to `docker.io`, a port's `:`
written `_` (a colon cannot appear in a repository path). `*` stays
within one path segment, `**` spans separators. Trust follows the
nominal reference, never a substituted endpoint (FR-036).

A scope that admits unsigned content is not a quiet setting: it is
reported in the effective configuration, in logs, and in every task
report that used it (FR-075 principle — security relaxations are visible
by construction).

:::note[Upcoming]
Three refinements are on the roadmap: a per-content **reduced-trust
marker** carried into reports, UI, metrics, and the media inventory
(R-22 — milestone 6); **provenance visible content by content** — recipe-
verified, unit import, direct push (R-17 — milestone 7); and the
documented **emergency path** — a locally written recipe signed with the
organization's emergency key, admitted under a declared `emergency` scope
(R-21 — milestone 7, documentation-only). Track them on the
[project status](../../discover/status/) page.
:::

## The two accepted signature formats

Both cosign layouts verify, so recipes signed years apart coexist
(RECIPE-SPEC §12.2):

- **Attached (historical)** — the signature of `<repo>@sha256:<hex>` is
  an OCI manifest in the same repository under the tag
  `sha256-<hex>.sig`, carrying SimpleSigning payloads that pin the
  subject digest.
- **Sigstore bundle (cosign 3.x default)** — an OCI artifact whose
  `artifactType` is the bundle media type
  (`application/vnd.dev.sigstore.bundle…`), referring to the subject
  manifest, single layer holding the bundle JSON.

In both cases the signature travels with the content — through
registries, cascades, and (at milestone 5) on the media — which is what
lets the destination re-verify everything from its own trust roots.

## Registry allowlist

The allowlist bounds which registries the instance may contact at all,
as source or destination, evaluated **before any transfer** (FR-030 —
delivered v0.4.0):

```yaml
registries:
  allowlist:
    - registry.example.com
    - "*.internal.example.com"    # * within one DNS label, ** across labels
    - lab.example.com:5000
```

Two semantics matter to an auditor:

- **Absent is not empty.** No `allowlist` key means no restriction —
  reported as *undeclared* everywhere, never silently passing for a
  satisfied policy. `allowlist: []` means nothing is allowed. The
  distinction is deliberate (same shape as a Kubernetes NetworkPolicy).
- **The effective reference is what is checked** (FR-036). When source
  substitution rewrites where a reference is fetched from, the allowlist
  and the credentials apply to the host *actually contacted*; trust
  scopes apply to the nominal reference. Logs record the
  nominal→effective mapping.

A refusal is the dedicated error `TBY-POL-001` (HTTP 403, naming the
host — see [errors](../../reference/errors/)), is logged, and is counted in
the `tobby_policy_rejections_total` and `tobby_promotion_refusals_total`
metric families by taxonomy code
([metrics and logs](../../reference/metrics-logs/)).
