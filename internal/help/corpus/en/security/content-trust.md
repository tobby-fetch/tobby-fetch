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
Recipe verification runs twice: at import into the store, and
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
by construction). What it admitted stays visible too, item by item:
content admitted under a relaxed scope carries a
[reduced-assurance marker](../reduced-assurance/) on every surface that
reports it.

**A scope's `roots` are a compartment.** A recipe matching a scope that
lists `roots` is verified against those roots only. A key that is a valid
trust root elsewhere in the same configuration — but is not listed by the
scope — does not verify it, and the refusal names the scope:
`TBY-SIG-001` carries a `scope` parameter, so you read which perimeter
refused the recipe instead of hunting through every root
([error reference](../../reference/errors/#tby-sig-001)). The same holds on
a synchronization, on the signature re-check before a push, in a plan, and
when a transported medium is verified on the destination side.

:::note[Upcoming — provenance, content by content]
One refinement is still on the roadmap: **provenance visible content by
content** — recipe-verified, unit import, direct push (R-17 — milestone
7). Track it on the [project status](../../discover/status/) page.
:::

A scope can also **restrict** rather than relax: its `roots` fence a
dedicated key, such as the organization's emergency key, to one cookbook.
That is the mechanism behind the documented
[emergency path](../../recipes/emergency-path/).

## Ingredient signatures — recorded, required only on request

A recipe's signature is what authenticates a delivery: signing a cooked
recipe covers its document, and with it the pinned digest of every
ingredient (RECIPE-SPEC §12.2). An ingredient may *also* carry a cosign
signature of its own — its upstream publisher's, typically: Rancher,
`registry.k8s.io` and Chainguard sign their images, often keyless, with
keys your zone has no reason to know. Verifying those is, per the
specification, a consumer policy decision. Tobby's default:

- the ingredient's signature is **copied** along with it, in either
  layout, through the store, the medium and the destination registry —
  so it stays verifiable downstream;
- it is **verified for information** against the trust roots that apply
  to the ingredient, and the outcome is recorded — on the task item, on
  the resolution row, in the `media verify` report and in the log —
  as one of: *verified by* a named root, *present, not verifiable by this
  zone's trust roots* (with the keys tried), or *none*;
- it **never blocks**: not the synchronization, not the promotion, not
  `media verify`, not the media import.

To make ingredient signatures a requirement, opt in:

```yaml
trust:
  requireIngredientSignatures: true
```

Every ingredient must then carry a signature that the applicable trust
roots verify — resolved exactly as for a recipe, with scopes matched on
the ingredient's canonical nominal repository and a scope's `roots`
restriction honoured. An ingredient that is unsigned, or signed only by
an unknown key, blocks its **recipe whole** (R-19): nothing of it is
stored as a cookbook entry, recorded for a medium, or pushed, and
sibling recipes are unaffected. The refusal is
[`TBY-SIG-001`](../../reference/errors/#tby-sig-001), naming the
ingredient, its recipe, its digest and the fingerprints tried. The same
rule runs, unchanged, at the synchronization, before every push, in
`media verify` and in the media import — so a delivery `media verify`
calls pushable is one the import pushes. An ingredient that falls inside
an `allowUnsigned` scope is not required to carry a signature: the scope
is the declared perimeter for unsigned content, and the opt-in does not
override it.

The setting is part of the effective configuration
(`tobby config dump`), and an instance states its posture at startup —
"ingredient signatures informational" or "ingredient signatures
required" (FR-075).

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
registries, cascades, and on the media — which is what lets the
destination re-verify everything from its own trust roots. Trust roots
found on a medium are ignored: they arrived with the content they would be
vouching for.

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
