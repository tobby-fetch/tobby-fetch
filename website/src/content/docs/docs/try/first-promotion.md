---
title: Your first promotion
description: Two local instances, one signed recipe, a full promotion, and a docker pull from the zone registry — step 2 of 2.
sidebar:
  order: 2
---

**Step 2 of 2** — you have a running instance from
[step 1](../install-and-start/). This page stages a second instance as the
upstream zone, publishes one signed recipe, lets your instance promote it,
and ends with a `docker pull` from the zone registry. Everything below
uses ordinary user tooling — nothing from the source tree.

Nothing leaves your machine: the two instances talk over the loopback
interface. The only external access is one `docker pull` from Docker Hub
at the start.

**You need:** the `tobby` binary and the `tobby.yaml` from step 1, plus
`docker`, `cosign` and `curl`.

## The cast

| Piece | Role |
|---|---|
| "Upstream" instance (`:8092`) | Plays the upstream zone: a registry and a cookbook. Authentication explicitly disabled — it is a stage prop, not a production setup. |
| Your instance (`:8080`) | The one from step 1: secured, with a trust key and a Retriever. It does the promoting. |
| A `cosign` key pair | Signs the recipe. The public key becomes your instance's trust root. |

## 1. Stand up the upstream zone

In a second terminal, from the same `tobby-demo` directory:

```sh
TOBBY_MODE=passthrough TOBBY_AUTH_DISABLED=true \
TOBBY_SERVER_ADDR=127.0.0.1:8092 TOBBY_STORAGE_ROOT=./upstream \
  tobby serve
```

Disabling authentication is a deliberate, loud exception: the instance
records it in its audit log and banners it in the interface. Fine for a
stage prop on loopback; never do it to an instance that matters.

## 2. Give it content

Tobby embeds a standard OCI registry, so ordinary tools push to it. Pull a
small public image and push it into the upstream instance, under the path
where its origin is preserved:

```sh
docker pull docker.io/library/alpine:3.22.1
docker tag docker.io/library/alpine:3.22.1 127.0.0.1:8092/docker.io/library/alpine:3.22.1
docker push 127.0.0.1:8092/docker.io/library/alpine:3.22.1
```

`docker push` prints a line ending in `digest: sha256:…` — **keep that
digest**, the recipe pins it.

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
      digest: sha256:<the digest docker push printed>
```

Concepts — recipes, cookbooks, retrievers — are covered in
[Understand recipes](../../recipes/understand/).

## 4. Publish and sign it

`tobby recipe push` publishes the recipe as an OCI artifact after checking
it: a document that is not a valid recipe, not fully pinned, or already
published with different content is refused. The upstream instance speaks
plain HTTP, which needs an explicit per-host opt-in:

```sh
export TOBBY_REGISTRIES_INSECURE=127.0.0.1:8092
tobby recipe push alpine.yaml 127.0.0.1:8092/cookbook/alpine:3.22.1
```

The published digest goes to stdout, ready for signing. Signing stays
outside Tobby — it never holds a private key:

```sh
cosign generate-key-pair
cosign sign --key cosign.key --yes --allow-insecure-registry \
  --use-signing-config=false --tlog-upload=false \
  "127.0.0.1:8092/cookbook/alpine@<the digest recipe push printed>"
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
  cookbook: 127.0.0.1:8092/cookbook
  recipes:
    - name: alpine
      version: "3.22.1"
```

An exact version is taken at its word; version constraints (`6.x`,
`~0.16.1`) resolve against the cookbook at each synchronization, so a
patch release lands by publishing it — no file to edit.

## 6. Point your instance at it

Stop the instance from step 1 with `Ctrl-C` — shutdown is graceful — and
add four things to its `tobby.yaml`: the Retriever, the trust root, the
plain-HTTP opt-in, and one source substitution:

```yaml
retriever:
  source: ./retriever.yaml   # a file, an https:// URL, or an OCI reference

trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub

registries:
  insecure: ["127.0.0.1:8092"]
  substitutions:
    "docker.io": "127.0.0.1:8092"
```

The substitution is the piece that makes zones chain: the recipe keeps
saying `docker.io/library/alpine`, and only the address actually contacted
changes — the content is fetched from the upstream instance, at the path
where it preserves the origin. Recipes are never rewritten to fit a zone.
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
fetches the recipe, verifies its cosign signature against your trust
root, checks the pinned digest, and transfers only what the zone is
missing. The **Recipes** screen then shows the recipe, its resolved
version and its verification verdict.

<!-- TODO: screenshot: Tasks screen with the sync task and its live log -->
<!-- TODO: screenshot: Recipes screen showing the verified recipe -->

Re-run the sync: it completes without transferring anything — the zone
already matches its desired state.

## 8. Pull from the zone registry

Your instance's embedded registry now serves the promoted content. Docker
authenticates with the same account the interface uses:

```sh
docker login 127.0.0.1:8080    # the account created by quickstart
docker pull 127.0.0.1:8080/docker.io/library/alpine:3.22.1
```

The pull succeeds, digest intact: the image was carried, verified, and
served — never rewritten, never re-signed. The path spells out where the
content came from; why that matters, and how to wire real clients
(containerd mirrors, GitOps), is in
[Connect your clients](../../passthrough/connect-clients/).

:::note[Behind a corporate proxy?]
This scenario is loopback-only, so it replays as-is on a locked-down
workstation — only the initial `docker pull` needs a way out. When your
real sources sit behind an authenticated proxy or a private PKI, Tobby's
single outbound path is configured once for the whole instance: see
[Enterprise networks](../../passthrough/network/).
:::

## What you just did

The stage was small, but nothing was simulated: a signed recipe in an
upstream cookbook, a zone declaring its desired state, a promotion that
re-verifies everything before serving it. Production replaces the props —
the upstream becomes a real registry or another Tobby instance, the
Retriever is published as an OCI artifact, the keys come from your
qualification process.

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
