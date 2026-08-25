---
title: "Connect your clients: Docker, containerd, GitOps"
description: Why relocated paths look the way they do, the per-recipe mapping table, containerd mirrors for K3s/RKE2, GitOps pitfalls, and the OS-package endpoint.
sidebar:
  order: 5
---

Content has landed in your zone — promoted continuously by a passthrough
instance, or imported from removable media in an isolated zone. Either
way, this page is the end of the journey: pointing the machines that
consume it at the right place. Everything here applies to both modes.

## Why `docker.io/x` becomes `registry.zone/docker.io/x`

Tobby relocates every ingredient under its **nominal source host**:

```
<zone-registry>[/<base-prefix>]/<canonical-source-host>[_<port>]/<repository-path>
```

`docker.io/bitnami/wordpress` is pullable at
`registry.zone.example/docker.io/bitnami/wordpress`, with an unchanged
digest. The rule ([ADR-0013](../../reference/srs-adr/), FR-035) buys three
things at the cost of longer names:

- **No collisions.** A flattened `registry.zone/bitnami/wordpress`
  cannot tell `docker.io/foo/bar` from `ghcr.io/foo/bar` — a correctness
  bug and a repository-confusion attack surface in one.
- **Predictability.** Given a recipe and a destination, the location of
  every ingredient is computable with no extra metadata — audits,
  cleanup tooling, and Tobby's own differential comparison all rely on
  the same pure function.
- **Cascade invariance.** The path is derived from the host *written in
  the recipe*, not the host contacted, so it is identical in every zone
  of a multi-hop chain.

Hosts are canonicalized (lowercased; `index.docker.io` and
`registry-1.docker.io` fold to `docker.io`), and a port's `:` becomes
`_` — `lab.example.com:5000` relocates under `lab.example.com_5000/`.
Names are never truncated: a destination that cannot take a relocated
name fails explicitly, before the push.

## The mapping table, per recipe

You never compute those paths by hand. Each recipe exposes its
source→destination table — every ingredient's nominal reference, its
relocated repository, tag, and pinned digest (FR-065):

- **UI**: the recipe's mapping view at `/recipes/{recipe}/mapping`, with
  copyable references.
- **API**: `GET /api/v1/recipes/{recipe}/mapping` — the same data as
  JSON, one entry per resolved version, ingredients included.

<!-- TODO: screenshot: the per-recipe mapping table with copy buttons -->

## containerd mirrors for K3s and RKE2

For **runtime image pulls**, you do not have to touch a single chart
value: containerd rewrites references at pull time. On each K3s/RKE2
node, write `/etc/rancher/{k3s,rke2}/registries.yaml` by hand from the
mapping table:

```yaml
# /etc/rancher/rke2/registries.yaml
mirrors:
  docker.io:
    endpoint:
      - "https://registry.zone.example"
    rewrite:
      "^(.*)$": "docker.io/$1"
  ghcr.io:
    endpoint:
      - "https://registry.zone.example"
    rewrite:
      "^(.*)$": "ghcr.io/$1"
configs:
  registry.zone.example:
    auth:
      username: puller
      password: "…"
```

With that in place, a pod referencing `docker.io/bitnami/wordpress`
pulls from the zone registry, and the chart deploys as published —
values untouched, digests intact.

:::note[Upcoming]
Generating this snippet per recipe and destination is the second half of
FR-065 and is not implemented yet — today the mapping table is exposed
and the snippet is written by hand. Track it on the
[project status](../../discover/status/) page.
:::

## What mirrors do not cover

Two classes of reference bypass containerd and **must name relocated
paths explicitly**, using the mapping table:

- **GitOps chart sources.** An Argo CD `repoURL` or Flux
  `HelmRepository` pointing at `oci://registry.zone.example/...` must
  use the relocated chart path — the Helm client does not go through
  node-level mirrors.
- **Admission policies.** Rules matching image references (Kyverno,
  Gatekeeper, signature-verification admission) see the reference as
  written in the pod spec. Decide which side of the rewrite your
  policies match, and keep it consistent.

One more caveat for verifiers: cosign signatures embed the *origin*
reference in `critical.identity.docker-reference`. Tobby copies
signatures bit-exact alongside the content, so a policy engine that
matches `docker-reference` against the **pulled** location will see
`docker.io/...` while pulling from `registry.zone.example/...`.
Configure the policy against the nominal reference. Details in
[content trust](../../security/content-trust/).

Tobby never rewrites chart values — that would break digests and
signatures, the exact thing it exists to preserve.

## Logging in

The embedded registry and the zone-facing surfaces use the instance's
own accounts (FR-076) — the same ones as the UI:

```sh
docker login registry.tobby.zone.example
helm registry login registry.tobby.zone.example
oras login registry.tobby.zone.example
```

Create dedicated read-only accounts or CI tokens for consumers; see
[authentication and RBAC](../../security/auth-rbac/).

## OS packages over HTTP: the `/files/` endpoint

A `FileSet` ingredient can package an apt or rpm repository, and Tobby
can serve its verified content read-only over HTTP under
`/files/<name>/…` (FR-047) — making a bare host installable with no
other infrastructure. Serving is **off by default** and enabled per
FileSet:

```yaml
files:
  filesets:
    - name: debs                     # served under /files/debs/
      ref: registry.example.com/filesets/site-packages
      version: "1.4.0"               # empty = highest semver present locally
      platform: linux/amd64          # only needed for multi-platform FileSets
      anonymous: true                # opt-in unauthenticated reads
```

Only what the store holds *and verified* is served; range requests are
supported; there is no upload surface. Reads require the viewer role by
default. `anonymous: true` exists for the bootstrap case — a bare host
that cannot authenticate before it has installed anything — and is never
silent: every anonymously served FileSet is named in a permanent UI
banner and reported by the API, like every other relaxation of the
secure default (FR-075).

```sh
# /etc/apt/sources.list.d/zone.list on a client host
deb [trusted=yes] https://tobby.zone.example/files/debs stable main
```

Package-manager trust of the repository metadata is your distribution's
mechanism; what Tobby guarantees is that the served tree came from a
FileSet that passed signature verification on its way into the store.

Next: bring content in outside any recipe —
[one-off imports](../../passthrough/one-off-import/) — or jump to
[operating over time](../../passthrough/operate/).
