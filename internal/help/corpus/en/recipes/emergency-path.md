---
title: The emergency path
description: A locally written recipe, signed with the organization's emergency key and admitted under a declared emergency trust scope — the documented, visible way to deliver something the normal pipeline cannot deliver in time.
sidebar:
  order: 3
---

Sometimes the qualification pipeline cannot produce what a zone needs
before it needs it: a patched image built in-house, a configuration bundle
that must change tonight. The wrong answer is to loosen verification. The
right one is a **second, narrower door**, declared in advance: a recipe
written locally, signed with a key the organization keeps for exactly this,
admitted under a trust scope that says so on every surface.

Nothing on this page adds a switch to Tobby. It assembles four mechanisms
that already exist — the recipe format, `tobby recipe push`, cosign
verification and [trust scopes](../../security/content-trust/) — into one
procedure you can rehearse before the day you need it.

## What the emergency path is, and is not

- **It is signed.** Tobby signs nothing, here as everywhere: you sign with
  cosign, outside Tobby, with a key the organization controls. The recipe
  is verified like any other, against a trust root.
- **It is compartmented, by configuration.** The emergency key is trusted
  for one cookbook path only: a root a scope lists is reserved to that
  scope (step 2).
- **It is visible.** The scope name travels with the recipe on the API, the
  task reports, the plan and the recipes screen, and the scope is part of
  the effective configuration.
- **It is not a bypass.** The same checks apply: pinned digests are
  verified, registries must be on the allowlist, content is scanned under
  the instance's [vulnerability policy](../../security/vulnerability-scanning/),
  and a medium carrying the recipe is re-verified on the destination side
  against the destination's own trust roots.
- **"emergency" is a name, not a mode.** Tobby does not interpret it: any
  scope name works. The word is a convention that makes reports readable
  by someone who was not there.

## Prepare once, before the emergency

### 1. An emergency key pair, kept apart

Generate a dedicated cosign key pair and keep the private key where the
organization keeps its most restricted secrets, outside any Tobby host:

```sh
cosign generate-key-pair      # writes cosign.key and cosign.pub
mv cosign.key emergency.key
mv cosign.pub emergency.pub
```

Only `emergency.pub` is ever given to Tobby. How the private key is held
and who may use it (split custody, hardware token, a break-glass procedure)
is the organization's decision, deliberately out of Tobby's scope
(ADR-0007).

### 2. An emergency cookbook, and a scope that fences it

Choose a cookbook repository path used for emergencies only, for example
`registry.example.com/emergency`. Then declare, in the configuration file
(`trust.*` is list-valued, so the configuration file only):

```yaml
trust:
  roots:
    - name: org-release-key
      keyFile: /etc/tobby/keys/org-release.pub
    - name: org-emergency-key
      keyFile: /etc/tobby/keys/emergency.pub

  scopes:
    # The emergency door: this cookbook, verified against the emergency
    # key only. Listing the key here also RESERVES it: it verifies
    # nothing outside this scope.
    - name: emergency
      repositories: ["registry.example.com/emergency/**"]
      roots: [org-emergency-key]
```

One scope is enough. A root that a scope lists is reserved to that scope:
the emergency key verifies the emergency cookbook and nothing else, and
everything outside the scope is verified against the **default set** — the
roots no scope lists, here `org-release-key` alone. A recipe in the normal
cookbook signed with the emergency key is refused with
[`TBY-SIG-001`](../../reference/errors/#tby-sig-001), and the message names
`org-emergency-key` and the `emergency` scope it is reserved to.

:::caution[Changed in v1.0.0-rc.1]
Up to v0.6.x, a repository that matched no scope was verified against
**every** configured root, the emergency key included (B-057), and this
page recommended a second, catch-all scope (`repositories: ["**"]`,
`roots: [org-release-key]`) to fence the emergency key in. That scope is no
longer needed. It still works, being first-match-wins after `emergency`,
and can be removed.
:::

Patterns match the recipe's nominal cookbook path in canonical form: host
in lowercase, a port's `:` written `_` (`registry.example.com_5000/…`). See
[Signatures, trust roots and allowlist](../../security/content-trust/#relaxation-scopes--and-their-visibility).
The scope does not set `allowUnsigned`: the emergency path never admits
unsigned content, so it carries no [reduced-assurance marker](../../security/reduced-assurance/)
and raises no banner. Its visibility is the scope name, below.

Add the emergency registry to `registries.allowlist` if the instance has
one, and give the Retriever a per-entry cookbook (step 6). Restart the
instance and check the result:

```sh
tobby config dump | grep -A12 '^trust:'
```

Rehearse the whole procedure on a test zone once. The cost of finding a
typo in a scope pattern is much lower on a quiet day.

## When the day comes

### 3. Write the recipe

Write the recipe like any other — see
[Write, publish and sign a recipe](../write-and-publish/) — against the
published [JSON Schema](https://github.com/tobby-fetch/recipe-spec/tree/main/schemas)
or the Go SDK (`github.com/tobby-fetch/recipe-spec/recipe/v1alpha1`,
`ParseRecipe`). An emergency recipe is a **cooked** recipe: every
ingredient has an exact `version` and a `digest`.

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: hotfix-gateway
  version: 1.4.2-hotfix.1
  description: Gateway rebuilt with the patched TLS library.
  annotations:
    example.com/ticket: INC-2041     # why this exists, for the next reader
spec:
  ingredients:
    - name: gateway
      kind: ContainerImage
      ref: registry.build.example.com/platform/gateway
      version: 1.4.2-hotfix.1
      digest: sha256:REPLACE-WITH-THE-DIGEST-OF-THE-INDEX
      platforms: ["linux/amd64"]
```

The recipe name and version are bound to its publication location:
`registry.example.com/emergency/hotfix-gateway:1.4.2-hotfix.1`. Put the
reason in `metadata.annotations`; the next person reading the report will
need it.

### 4. Take the digests from the content browser

An ingredient is pinned by the digest of its **top-level manifest** (for a
multi-platform image, the index). If the content is already held by an
instance, take the digest from the **Content** screen: open the
repository, find the tag, and use the copy button next to its digest (the
same value is on `GET /api/v1/content`). If it is only in a registry, read
it there with `crane digest <ref>` or `oras manifest fetch --descriptor`.
Never retype a digest, and never take it from a tag you have not looked
at twice: the tag is what moves, the digest is the content.

Validate before publishing:

```sh
recipe lint --profile cooked hotfix-gateway.yaml
```

### 5. Publish, then sign by digest

```sh
DIGEST=$(tobby recipe push hotfix-gateway.yaml \
  registry.example.com/emergency/hotfix-gateway:1.4.2-hotfix.1)

cosign sign --key emergency.key --yes \
  --use-signing-config=false --tlog-upload=false \
  "registry.example.com/emergency/hotfix-gateway@${DIGEST}"
```

`tobby recipe push` refuses anything a zone would refuse later (unpinned
ingredients, a name or version that contradicts the reference) and prints
the digest alone on stdout. The two cosign flags keep the signature
verifiable fully offline and keep it out of the public Rekor log; they are
not optional for a restricted zone (see
[Sign with cosign](../write-and-publish/#sign-with-cosign)). Tobby holds no
key at any point of this step.

Verify what you just signed before anyone relies on it:

```sh
cosign verify --key emergency.pub --insecure-ignore-tlog=true \
  "registry.example.com/emergency/hotfix-gateway@${DIGEST}"
```

### 6. Admit it: a Retriever entry with its own cookbook

The Retriever is the desired state of the zone. Add the recipe with a
per-entry `cookbook` pointing at the emergency cookbook (RECIPE-SPEC §10.2),
so that the entry is fenced by the `emergency` scope and the rest of the
Retriever keeps resolving from the normal cookbook:

```yaml
spec:
  cookbook: registry.example.com/cookbook
  recipes:
    # … the zone's usual entries …
    - name: hotfix-gateway
      version: "1.4.2-hotfix.1"
      cookbook: registry.example.com/emergency
```

Plan before you promote. A plan changes nothing and shows the trust
verdict of every recipe, including the scope that decided it:

```sh
tobby sync --dry-run --retriever ./retriever.yaml
```

Then trigger the synchronization (an `operator` token, `TOBBY_API_TOKEN`):

```sh
tobby sync --wait
```

If the signature does not verify against `org-emergency-key`, the recipe
is refused with [`TBY-SIG-001`](../../reference/errors/#tby-sig-001), and
the message names the scope that refused it. A signature made with
the normal `org-release-key` does not verify here either: the scope's
`roots` are a compartment.

## Where the emergency stays visible

The recipe is recorded with the verdict and the scope that admitted it.
Nothing is hidden because the signature is valid.

| Surface | What you see |
| --- | --- |
| Effective configuration | `trust.scopes` with `emergency` and its repositories and roots (`tobby config dump`, and the configuration shown by the instance). |
| Recipes | `GET /api/v1/recipes`: `verified: true` and `trustScope: "emergency"` on the recorded recipe. The Recipes screen shows the scope beside the verdict. |
| Task reports | `trust_scope` on every item of the synchronization task. |
| Plan | The trust scope that decided each recipe's verdict, in the plan report (`tobby sync --dry-run --output json`) and on the plan screen. |
| Media | The scope is shown beside the recipe on the Media screen of a medium carrying it; the destination takes its **own** verdict from its own trust roots, so the destination must declare the same emergency scope and root to admit the recipe. |
| Audit log | The publication through the interface or the API is a `recipe.publish` event; a synchronization is `sync.create`. See the [audit log](../../security/audit-log/). |

:::note
The permanent banner and the [reduced-assurance marker](../../security/reduced-assurance/)
are reserved for scopes that waive something (`allowUnsigned`). A scope
that only **restricts** which keys may verify, as `emergency` does, waives
nothing and so marks nothing; its visibility is the scope name carried on
the surfaces above.
:::

## After the emergency

The emergency path is for delivery, not for staying. Close it properly:

1. Have the normal pipeline qualify, cook and sign the same change in the
   standard cookbook, then replace the Retriever entry and drop its
   `cookbook` override.
2. Rotate the emergency key if it left its custody. Rotation is by
   overlap, like any trust root: add the new root, re-sign what is still
   in use, then remove the old one.
3. Keep the ticket reference in the recipe's annotations and the
   synchronization run in your records: the task report and the audit log
   are the evidence of who admitted what, and when.

Published recipe versions are immutable. A correction to the emergency
recipe is a new `metadata.version`, never a re-push over the same tag.

## Related pages

- [Signatures, trust roots and allowlist](../../security/content-trust/): roots, scopes, the two cosign signature layouts.
- [Write, publish and sign a recipe](../write-and-publish/): the authoring path this procedure follows.
- [The security model](../../security/model/): how this fits the rest of the guarantees.
- [Troubleshooting](../../reference/troubleshooting/): `TBY-SIG-001`, `TBY-POL-001` and friends.
