---
title: Your first promotion
description: One Tobby instance between an upstream registry and your zone — a signed recipe, a full promotion, and a docker pull from the zone registry — step 2 of 2.
sidebar:
  order: 2
---

**Step 2 of 2** — you have a running instance from
[step 1](../install-and-start/). This page puts it in its real place:
between the registries where content already lives and the zone that
consumes it. You will pin one public image in a recipe, sign the recipe,
publish it to a cookbook, declare the zone's desired state, let your
instance promote it — and finish with a `docker pull` from the zone
registry. Everything below uses ordinary user tooling — nothing from the
source tree.

<svg viewBox="0 0 640 230" role="img" aria-label="An upstream registry and a cookbook on the left, one Tobby instance promoting into its zone on the right, zone clients pulling from Tobby's registry" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="fp-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- zones -->
  <rect x="8" y="30" width="216" height="170" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="288" y="30" width="344" height="170" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="116" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Where content lives</text>
  <text x="460" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Your zone</text>
  <!-- upstream boxes -->
  <rect x="28" y="48" width="176" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="116" y="67" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">docker.io</text>
  <text x="116" y="82" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">the image, already published</text>
  <rect x="28" y="130" width="176" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="116" y="149" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">cookbook registry</text>
  <text x="116" y="164" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">your signed recipe</text>
  <!-- tobby -->
  <rect x="312" y="88" width="140" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="382" y="108" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="382" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">store + zone registry</text>
  <!-- clients -->
  <rect x="492" y="88" width="120" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="552" y="108" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">zone clients</text>
  <text x="552" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">docker pull</text>
  <!-- flows -->
  <line x1="204" y1="71" x2="308" y2="100" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="72" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">image, by digest</text>
  <line x1="204" y1="153" x2="308" y2="124" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="158" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">recipe + signature</text>
  <line x1="452" y1="112" x2="488" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <!-- note -->
  <text x="460" y="186" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-2)">Verified at the boundary — never rewritten, never re-signed</text>
</svg>

**You need:** the `tobby` binary and the `tobby.yaml` from step 1, plus
`docker`, `cosign` and `curl`. The image comes from Docker Hub; the only
thing you stand up yourself is a throwaway cookbook registry on loopback.

## The cast

| Piece | Role |
|---|---|
| `docker.io` | The upstream registry. The image already lives there; nothing is pushed to it. |
| A cookbook registry (`:5001`) | An ordinary OCI registry holding your signed recipe. Any registry you can push to works — here, a throwaway local one. |
| Your instance (`:8080`) | The one from step 1: secured, with a trust key and a Retriever. It does the promoting. |
| A `cosign` key pair | Signs the recipe. The public key becomes your instance's trust root. |

## 1. A cookbook registry

Recipes are published into a *cookbook*: an ordinary OCI repository, on
any registry you can push to. If you already have one (ghcr.io, Harbor,
…), use it and adapt the addresses below. For the walkthrough, a
throwaway registry on loopback does fine:

```sh
docker run -d --rm --name cookbook -p 5001:5000 registry:2
```

The host port is 5001, not 5000: on macOS the AirPlay receiver squats
port 5000 and answers 403 in the registry's place — a classic trap of
local-registry tutorials.

## 2. Pin the image

A recipe names content by digest, never just by tag. Read the digest that
`alpine:3.22.1` resolves to today:

```sh
docker buildx imagetools inspect docker.io/library/alpine:3.22.1
```

The first lines print `Digest: sha256:…` — **keep that digest**, the
recipe pins it.

## 3. Write the recipe

A recipe describes one coherent delivery. A *cooked* recipe — the only
kind a cookbook publishes — pins every ingredient by digest, so one
signature attests the exact bytes of the whole delivery. Write
`alpine.yaml`, with the digest from the previous step:

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe

metadata:
  name: alpine
  version: 3.22.1
  description: First promotion — one pinned image

spec:
  ingredients:
    - name: alpine
      kind: ContainerImage
      ref: docker.io/library/alpine   # nominal reference, no tag
      version: 3.22.1
      digest: sha256:<the digest imagetools printed>
```

Concepts — recipes, cookbooks, retrievers — are covered in
[Understand recipes](../../recipes/understand/).

## 4. Publish and sign it

`tobby recipe push` publishes the recipe as an OCI artifact after checking
it: a document that is not a valid recipe, not fully pinned, or already
published with different content is refused. The throwaway registry
speaks plain HTTP, which needs an explicit per-host opt-in:

```sh
export TOBBY_REGISTRIES_INSECURE=127.0.0.1:5001
tobby recipe push alpine.yaml 127.0.0.1:5001/cookbook/alpine:3.22.1
```

The published digest goes to stdout, ready for signing. Signing stays
outside Tobby — it never holds a private key:

```sh
cosign generate-key-pair
cosign sign --key cosign.key --yes --allow-insecure-registry \
  --use-signing-config=false --tlog-upload=false \
  "127.0.0.1:5001/cookbook/alpine@<the digest recipe push printed>"
```

The two flags `--use-signing-config=false --tlog-upload=false` keep the
signature verifiable offline: without them, cosign 3.x publishes to the
public transparency log — a network call a restricted-zone signer should
not make, and a lookup the destination could not perform anyway.

## 5. Declare the zone's desired state

A Retriever names what the zone should contain. Write `retriever.yaml`:

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever

metadata:
  name: demo-zone

spec:
  cookbook: 127.0.0.1:5001/cookbook
  recipes:
    - name: alpine
      version: "3.22.1"
```

An exact version is taken at its word; version constraints (`6.x`,
`~0.16.1`) resolve against the cookbook at each synchronization, so a
patch release lands by publishing it — no file to edit.

## 6. Point your instance at it

Stop the instance from step 1 with `Ctrl-C` — shutdown is graceful — and
add three things to its `tobby.yaml`: the Retriever, the trust root, and
the plain-HTTP opt-in for the throwaway registry:

```yaml
retriever:
  source: ./retriever.yaml   # a file, an https:// URL, or an OCI reference

trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub

registries:
  insecure: ["127.0.0.1:5001"]
```

Then restart:

```sh
tobby serve --config ./tobby.yaml
```

## 7. Promote

In passthrough mode the instance reconciles on its own schedule (every 15
minutes by default). Trigger a cycle now instead: the **Recipes** screen
has a synchronize action, or from the command line:

```sh
curl -u admin -X POST http://localhost:8080/api/v1/sync
```

Watch the **Tasks** screen: the synchronization resolves the Retriever,
fetches the recipe from the cookbook, verifies its cosign signature
against your trust root, then pulls the image straight from `docker.io`
and checks it against the pinned digest — transferring only what the zone
is missing. The **Recipes** screen then shows the recipe, its resolved
version and its verification verdict.

![The task detail: sync items with per-item status and the raw JSON log, run identifier included](../../../../assets/docs/task-detail.png)
![The recipes screen showing the promoted recipe, signature verified](../../../../assets/docs/try-recipes-verified.png)

Re-run the sync: it completes without transferring anything — the zone
already matches its desired state.

## 8. Pull from the zone registry

Your instance's embedded registry now serves the promoted content. Docker
authenticates with the same account the interface uses:

```sh
docker login 127.0.0.1:8080    # the account created by quickstart
docker pull 127.0.0.1:8080/docker.io/library/alpine:3.22.1
```

If your Docker daemon runs inside a VM (Rancher Desktop, some Colima
setups), its `127.0.0.1` is the VM, not your machine — use your host's
LAN address in both commands instead.

The pull succeeds, digest intact: the image was carried, verified, and
served — never rewritten, never re-signed. The path spells out where the
content came from; why that matters, and how to wire real clients
(containerd mirrors, GitOps), is in
[Connect your clients](../../passthrough/connect-clients/).

:::note[Behind a corporate proxy?]
The scenario replays as-is on a locked-down workstation: the image fetch
from `docker.io` is Tobby's only outbound call, and Tobby's outbound
traffic is configured once for the whole instance — authenticated proxy,
private PKI included. See
[Enterprise networks](../../passthrough/network/).
:::

## What you just did

The stage was small, but nothing was simulated: a signed recipe in a
cookbook, a zone declaring its desired state, a promotion that verifies
everything before serving it. Production only changes the addresses — the
cookbook moves to a registry your qualification process publishes to, the
Retriever is published as an OCI artifact, the keys come from that same
process.

:::tip[Lab trick: a second Tobby as a stand-in upstream]
This works because a Tobby instance exposes a standard OCI registry: a
second, throwaway instance can play both upstream roles at once — image
source and cookbook — which is a clever way to replay this whole scenario
offline, without touching any real registry. It is **not** Tobby's normal
place in an architecture; its place is the one on the diagram above.

Start the stand-in with authentication explicitly disabled (a loud,
audited exception — fine for a stage prop on loopback, never for an
instance that matters):

```sh
TOBBY_MODE=passthrough TOBBY_AUTH_DISABLED=true \
TOBBY_SERVER_ADDR=127.0.0.1:8092 TOBBY_STORAGE_ROOT=./upstream \
  tobby serve
```

Ordinary tools push to its embedded registry, under the path that
preserves the origin — `docker push` prints the digest your recipe then
pins:

```sh
docker pull docker.io/library/alpine:3.22.1
docker tag docker.io/library/alpine:3.22.1 127.0.0.1:8092/docker.io/library/alpine:3.22.1
docker push 127.0.0.1:8092/docker.io/library/alpine:3.22.1
```

Adapt the recipe: pin the digest `docker push` printed — what you pushed
is a single-platform manifest, so its digest differs from the origin
index's. Publish and sign it against
`127.0.0.1:8092/cookbook/alpine:3.22.1` exactly as above, point the
Retriever's `cookbook` there, and replace the `registries` block of your
`tobby.yaml` with:

```yaml
registries:
  insecure: ["127.0.0.1:8092"]
  substitutions:
    "docker.io": "127.0.0.1:8092"
```

The substitution is the interesting part: the recipe keeps saying
`docker.io/library/alpine`, and only the address actually contacted
changes. That is exactly how real zones chain one Tobby behind another,
recipes unmodified — see
[Retriever and cascade](../../passthrough/retriever-cascade/).
:::

## Where next

- **Connected zones (passthrough)** — the delivered use case, run for
  real: [architecture and continuous promotion](../../passthrough/overview/),
  then [Deploy](../../passthrough/deploy/) on Kubernetes or a VM.
- **Isolated zones (air-gap)** — the same promotion carried on removable
  media: [the media journey](../../air-gap/media-workflow/).
- **Write your own recipes** — the reasoning behind an ingredient list,
  and the pitfalls: [write, publish and sign](../../recipes/write-and-publish/).
- **Security reader?** — the model behind what you just watched, on one
  page: [the security one-pager](../../security/one-pager/).
