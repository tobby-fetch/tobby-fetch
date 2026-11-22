---
title: Your first promotion
description: One Tobby instance between an upstream registry and your zone's registry — a signed recipe, a full promotion, and a docker pull from the zone registry — step 2 of 2.
sidebar:
  order: 2
---

**Step 2 of 2** — you have a running instance from
[step 1](../install-and-start/). This page puts it in its real place:
between the registries where content already lives and the zone that
consumes it.

A **promotion** is what Tobby exists for: content that already exists
upstream is copied into a zone's registry, unchanged, after its signature
and its digests have been verified. You will pin one public image in a
*recipe* (the list of what to deliver), sign the recipe, publish it to a
*cookbook* (the repository that holds recipes), declare the zone's desired
state in a *Retriever*, let your instance promote it — and finish with a
`docker pull` from the zone registry. Each of these words has an entry in
the [glossary](../../discover/glossary/). Everything below uses ordinary
user tooling — nothing from the source tree.

<svg viewBox="0 0 640 230" role="img" aria-label="An upstream registry and a cookbook on the left, one Tobby instance verifying and pushing into the zone registry on the right, where zone clients pull" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
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
  <text x="382" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">verifies, then pushes</text>
  <!-- clients -->
  <rect x="492" y="88" width="120" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="552" y="108" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">zone registry</text>
  <text x="552" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">clients pull here</text>
  <!-- flows -->
  <line x1="204" y1="71" x2="308" y2="100" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="72" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">image, by digest</text>
  <line x1="204" y1="153" x2="308" y2="124" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="158" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">recipe + signature</text>
  <line x1="452" y1="112" x2="488" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <!-- note -->
  <text x="460" y="186" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-2)">Verified at the boundary — never rewritten, never re-signed</text>
</svg>

:::note[You need]
- The instance from step 1, **still running** in its own terminal, and a
  **second terminal** opened in the same `tobby-demo` directory. Every
  command and every file below lives in that directory: the configuration
  refers to `./retriever.yaml` and `./cosign.pub` by relative path.
- Docker with the `buildx` plugin (`docker buildx version` answers).
- `cosign` **3.x** (`cosign version`). The signing command below uses
  flags a 2.x release does not all have.
- `curl`, and outbound access to Docker Hub for two small images.
- TCP ports `5001` and `5002` free, for two throwaway registries.
:::

## The cast

| Piece | Role |
|---|---|
| `docker.io` | The upstream registry. The image already lives there; nothing is pushed to it. |
| A cookbook registry (`:5001`) | An ordinary OCI registry holding your signed recipe. Any registry you can push to works — here, a throwaway local one. |
| Your instance (`:8080`) | The one from step 1. On this page it receives a Retriever, a trust root and a destination. It does the promoting. |
| The zone registry (`:5002`) | The registry your zone's clients pull from: the destination of the promotion. In production it already exists (Harbor, a cloud registry, …) — here, a second throwaway one. |
| A `cosign` key pair | Signs the recipe. The public key becomes your instance's **trust root**: the only key whose signatures it accepts. |

## 1. Two throwaway registries

Recipes are published into a *cookbook*: an ordinary OCI repository, on
any registry you can push to. If you already have one (ghcr.io, Harbor,
…), use it and adapt the addresses below. For the walkthrough, a
throwaway registry on loopback does fine — and a second one plays the
zone registry:

```sh
docker run -d --rm --name cookbook -p 5001:5000 registry:2
docker run -d --rm --name zone -p 5002:5000 registry:2
```

Each command prints a container ID. Both registries are empty and forget
everything when stopped.

The host ports start at 5001, not 5000: on macOS the AirPlay receiver squats
port 5000 and answers 403 in the registry's place — a classic trap of
local-registry tutorials.

## 2. Pin the image

A recipe names content by digest, never just by tag. Read the digest that
`alpine:3.24.4` resolves to today:

```sh
docker buildx imagetools inspect docker.io/library/alpine:3.24.4
```

The first lines look like this — **keep the `Digest` value**, the recipe
pins it:

```
Name:      docker.io/library/alpine:3.24.4
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:f59abe65…
```

Take the digest at the top, the one of the multi-platform index, not the
per-platform ones listed under `Manifests`. Without `buildx`,
`crane digest docker.io/library/alpine:3.24.4` or
`skopeo inspect --raw docker://docker.io/library/alpine:3.24.4 | sha256sum`
give the same value.

## 3. Write the recipe

A recipe describes one coherent delivery. A *cooked* recipe — the only
kind a cookbook publishes — pins every ingredient by digest, so one
signature attests the exact bytes of the whole delivery. Write
`alpine.yaml`, with the digest from the previous step:

```yaml
apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Recipe

metadata:
  name: alpine
  version: 3.24.4
  description: First promotion — one pinned image

spec:
  ingredients:
    - name: alpine
      kind: ContainerImage
      ref: docker.io/library/alpine   # nominal reference, no tag
      version: 3.24.4
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
tobby recipe push alpine.yaml 127.0.0.1:5001/cookbook/alpine:3.24.4
```

It answers with the digest of the published recipe, and the signing
command to run:

```
sha256:3aa1de2f…
127.0.0.1:5001/cookbook/alpine:3.24.4 published
sign it with:
  cosign sign --key <key> --use-signing-config=false --tlog-upload=false 127.0.0.1:5001/cookbook/alpine@sha256:3aa1de2f…
```

Signing stays outside Tobby — it never holds a private key. Create a key
pair, then sign the digest `recipe push` printed. `--yes` skips cosign's
confirmation prompt and `--allow-insecure-registry` is there because the
throwaway registry speaks plain HTTP:

```sh
cosign generate-key-pair
cosign sign --key cosign.key --yes --allow-insecure-registry \
  --use-signing-config=false --tlog-upload=false \
  "127.0.0.1:5001/cookbook/alpine@<the digest recipe push printed>"
```

`cosign generate-key-pair` asks for a password to protect the private key,
twice, and `cosign sign` asks for it again: choose any, an empty one is
accepted for a try-out. It writes `cosign.key` (private, stays here) and
`cosign.pub` (public). `cosign sign` ends with
`Pushing signature to: 127.0.0.1:5001/cookbook/alpine`; cosign 3.x also
prints two deprecation notices about the flags below, which are expected.

The two flags `--use-signing-config=false --tlog-upload=false` keep the
signature verifiable offline: without them, cosign 3.x publishes to the
public transparency log — a network call a restricted-zone signer should
not make, and a lookup the destination could not perform anyway.

## 5. Declare the zone's desired state

A Retriever names what the zone should contain. Write `retriever.yaml`:

```yaml
apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Retriever

metadata:
  name: demo-zone

spec:
  cookbook: 127.0.0.1:5001/cookbook
  recipes:
    - name: alpine
      version: "3.24.4"
```

An exact version is taken at its word; version constraints (`6.x`,
`~0.16.2`) resolve against the cookbook at each synchronization, so a
patch release lands by publishing it — no file to edit.

## 6. Point your instance at it

In the first terminal, stop the instance from step 1 with `Ctrl-C` —
shutdown is graceful — and add four things at the end of its
`tobby.yaml`: the Retriever, the trust root, the plain-HTTP opt-in for
the two throwaway registries, and the destination:

```yaml
retriever:
  source: ./retriever.yaml   # a file, an https:// URL, or an OCI reference

trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub

registries:
  insecure: ["127.0.0.1:5001", "127.0.0.1:5002"]

destination:
  registry: 127.0.0.1:5002   # the zone registry: a bare host, no scheme, no path
```

`destination.registry` is the key that makes this a promotion: it names
the zone registry the verified content is pushed into. Leave it out and
the instance still fetches and verifies, but pushes nowhere — it only
serves the content from its own embedded registry.

Then restart, in that same terminal (if you moved the instance to another
port in step 1, its `server:` block is already in the file and stays):

```sh
tobby serve --config ./tobby.yaml
```

The log ends with `http server listening`. A refusal to start names the
key at fault — most often a path: `./retriever.yaml` and `./cosign.pub`
are resolved from the directory you start `tobby` in.

## 7. Promote

In passthrough mode the instance reconciles on its own schedule (every 15
minutes by default). Trigger a cycle now instead: the **Recipes** screen
has a synchronize action, or from the command line:

```sh
curl -u admin -X POST http://localhost:8080/api/v1/sync
```

`curl` asks for the admin password, and the answer is the task that was
just created:

```
{"task":{"id":"tsk_…","run_id":"…","type":"sync","status":"running", …}}
```

Watch the **Tasks** screen: the synchronization resolves the Retriever,
fetches the recipe from the cookbook, verifies its cosign signature
against your trust root, then pulls the image straight from `docker.io`
and checks it against the pinned digest — transferring only what the zone
is missing — and pushes both into the zone registry. Count half a minute.
The task ends `done` with four items: the image and the recipe fetched,
then the same two pushed. The **Recipes** screen then shows the recipe,
its resolved version and its verification verdict.

The image's scan verdict reads *not scanned*: a fresh instance has no
vulnerability database yet, and scanning is advisory by default. That is
expected here; [vulnerability scanning](../../security/vulnerability-scanning/)
covers the real setup.

![The task detail: sync items with per-item status and the raw JSON log, run identifier included](../../../../assets/docs/task-detail.png)
![The recipes screen showing the promoted recipe, signature verified](../../../../assets/docs/try-recipes-verified.png)

:::note[If the task fails]
The task detail names a `TBY-…` code on the item that failed. The three
usual causes on this page:

- **The recipe is refused** — the signature does not verify: `cosign.pub`
  is not the key that signed, or the signature was made on the tag
  instead of the digest. See
  [a recipe is refused](../../reference/troubleshooting/#a-recipe-is-refused).
- **The image is refused** — the digest in `alpine.yaml` is not the one
  `imagetools` printed. Fix the file, then publish it under a new version:
  a published version is never overwritten.
- **A registry does not answer** — `docker.io` behind a proxy, or one of
  the two throwaway registries stopped. See
  [a source registry does not answer](../../reference/troubleshooting/#a-source-registry-does-not-answer).

[The error reference](../../reference/errors/) explains every code.
:::

Re-run the sync: it completes in a second with its four items *skipped* —
the zone already matches its desired state, nothing is transferred.

## 8. Pull from the zone registry

The zone registry now holds the promoted content, under a path that
spells out where it came from:

```sh
docker pull 127.0.0.1:5002/docker.io/library/alpine:3.24.4
```

```
Digest: sha256:f59abe65…
Status: Downloaded newer image for 127.0.0.1:5002/docker.io/library/alpine:3.24.4
```

If your Docker daemon runs inside a VM (Rancher Desktop, some Colima
setups) and the pull cannot connect, its `127.0.0.1` is the VM, not your
machine — use your host's LAN address instead.

The pull succeeds and the digest is the one you pinned in step 2: the
image was carried, verified, and pushed — never rewritten, never
re-signed. The signed recipe travelled with it, to `cookbook/alpine` on
the zone registry.

Your instance also serves the same content from its own embedded
registry, for a zone that has no registry of its own: `docker login
127.0.0.1:8080` with the account created by quickstart, then
`docker pull 127.0.0.1:8080/docker.io/library/alpine:3.24.4`.

Why the path keeps the origin, and how to wire real clients (containerd
mirrors, GitOps), is in
[Connect your clients](../../passthrough/connect-clients/).

:::note[Behind a corporate proxy?]
Two things reach the internet on this page, and each needs its proxy
settings: Docker, to pull `registry:2` and to resolve the digest, and
Tobby, to fetch the image from `docker.io`. Tobby's outbound traffic is
configured once for the whole instance — authenticated proxy, private
PKI included: see [Enterprise networks](../../passthrough/network/).
:::

## What you just did

The stage was small, but nothing was simulated: a signed recipe in a
cookbook, a zone declaring its desired state, a promotion that verifies
everything before pushing it. Production keeps this shape and changes
what surrounds it: the cookbook moves to a registry your qualification
process publishes to, the keys come from that same process, the Retriever
is published as an OCI artifact, and `destination.registry` names your
real zone registry. It also adds what a loopback stage leaves out — TLS,
registry credentials, a registry allowlist and durable storage — all in
[Deploy](../../passthrough/deploy/).

When you are done, `Ctrl-C` stops the instance and
`docker rm -f cookbook zone` removes the two registries.

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
docker pull docker.io/library/alpine:3.24.4
docker tag docker.io/library/alpine:3.24.4 127.0.0.1:8092/docker.io/library/alpine:3.24.4
docker push 127.0.0.1:8092/docker.io/library/alpine:3.24.4
```

Adapt the recipe: pin the digest `docker push` printed — what you pushed
is a single-platform manifest, so its digest differs from the origin
index's. Publish and sign it against
`127.0.0.1:8092/cookbook/alpine:3.24.4` exactly as above, point the
Retriever's `cookbook` there, and replace the `registries` block of your
`tobby.yaml` with:

```yaml
registries:
  insecure: ["127.0.0.1:8092", "127.0.0.1:5002"]
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
  media, replayed on this machine in ten more minutes:
  [air-gap on one machine](../air-gap-on-one-machine/).
- **Write your own recipes** — the reasoning behind an ingredient list,
  and the pitfalls: [write, publish and sign](../../recipes/write-and-publish/).
- **Security reader?** — the model behind what you just watched, on one
  page: [the security one-pager](../../security/one-pager/).
