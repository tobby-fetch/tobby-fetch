---
title: "Consumption aids: mapping table and registries.yaml"
description: Generate, from what the store holds, the source-to-destination table and the K3s/RKE2 registries.yaml that point a cluster at the zone registry — UI, API and command line.
sidebar:
  order: 6
---

Once content has landed in the zone registry, the machines that consume it
need two things: the exact name every ingredient now has, and a runtime
configuration that makes `docker.io/bitnami/wordpress` resolve there. Tobby
generates both from the recipes its store holds (FR-065) — so they match
what was pushed, instead of being typed from the
[mapping convention](../connect-clients/) by hand.

Both aids are **read-only**: Tobby computes them, you copy them. Nothing is
ever written to a node, and nothing in either aid is a credential.

## Which registry they point at

The aids point at the registry the zone's clients pull from:

| Situation | Registry used | Path prefix used |
|---|---|---|
| The instance pushes to a zone registry | `destination.registry` | `destination.basePath` |
| The zone reaches the registry under another name | `consumption.registry` | `destination.basePath` |
| The instance serves its own `/v2/` to the zone (UC3) | `consumption.registry` | `storage.basePrefix` |

```yaml
consumption:
  registry: registry.zone.example:5000   # a bare host, what the nodes pull from
  caFile: /etc/ssl/certs/zone-ca.crt     # path ON THE NODES; only written into the snippet
```

With neither `destination.registry` nor `consumption.registry` set, every
aid is refused with [`TBY-AID-001`](../../reference/errors/#tby-aid-001),
which names both settings. `consumption.caFile` is never read by Tobby: it is
the path of the CA certificate on the cluster nodes, copied as-is into the
snippet.

## The mapping table

One row per ingredient of each selected recipe, FileSets included:

| Column | Meaning |
|---|---|
| `recipe`, `recipeVersion` | The recipe the ingredient belongs to. |
| `ingredient`, `kind` | The ingredient name and its declared kind. |
| `source` | The reference the recipe names, with its tag (or digest when it has none), host canonicalized: `docker.io/bitnami/wordpress:6.5.2`. |
| `destination` | The reference at the destination registry, relocated per [ADR-0013](../../reference/srs-adr/): `registry.zone.example:5000/docker.io/bitnami/wordpress:6.5.2`. |
| `digest` | The pinned digest — unchanged between source and destination. |

Where to get it, always with the same rows:

- **UI.** The recipe's mapping screen (`/recipes/{recipe}/mapping`) and the
  **Consumption** screen (`/consumption`, the whole store) offer the table
  as CSV, JSON or Markdown downloads.
- **API.** `GET /api/v1/mapping?format=csv|json|markdown` for the whole
  store; `GET /api/v1/recipes/{recipe}/mapping?format=…` for one recipe
  (without `format` that route keeps answering the recorded versions).
- **Command line.**

```sh
tobby consumption table --output csv > mapping.csv
tobby consumption table --recipe wordpress@6.5.2 --output markdown
tobby consumption table --recipe rke2@1.36.5 --destination 10.0.0.5:5000 --output json
```

The JSON form is `{"destination": "…", "entries": [{"recipe": …}]}`, the
document published as `tobby consumption table` in the
[command-line output schema](../../reference/cli/#report-format).

## The registries.yaml snippet

K3s and RKE2 read `/etc/rancher/k3s/registries.yaml` and
`/etc/rancher/rke2/registries.yaml` respectively. Tobby generates one
`mirrors` entry per source registry host of the container images of the
selected recipes (`docker.io` for every Docker Hub alias); its `endpoint` is
the zone registry and its `rewrite` maps the repository onto its relocated
path. For a recipe holding images from two registries, with the destination
on plain HTTP (listed in `registries.insecure`):

```yaml
mirrors:
  "docker.io":
    endpoint:
      - "http://10.0.0.5:5000"
    rewrite:
      "^(.*)$": "docker.io/$1"
  "registry.rancher.com":
    endpoint:
      - "http://10.0.0.5:5000"
    rewrite:
      "^(.*)$": "registry.rancher.com/$1"
```

How to read it: containerd matches each `rewrite` key, a regular
expression, against the repository part of the image name (no host, no
tag), so `docker.io/library/busybox:1.36` is pulled from
`10.0.0.5:5000/docker.io/library/busybox:1.36`. A digest-pinned reference
keeps its digest. A port in the source host is written `_5000` in the
relocated path (`forge.example.com_5000/$1`); a `destination.basePath` is
prepended (`mirror/docker.io/$1`).

- The scheme is `https` unless the destination is listed in
  `registries.insecure`, in which case it is `http`.
- When `consumption.caFile` is set, a `configs` entry for the destination
  carries `tls.ca_file`. That is the only `configs` content Tobby writes.
- **No credential, ever.** If the zone registry requires authentication, a
  commented `auth` placeholder shows where to put it; fill it on the node.
- The output is deterministic — same store and configuration, same bytes —
  and has no timestamp. A header comment states the Tobby version, the
  destination, the recipes covered, and the limits below.

Get it from the same three places:

```sh
tobby consumption registries-yaml --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
  --destination 10.0.0.5:5000 > registries.yaml
```

`tobby consumption registries` is an alias. In the UI it is shown on the
mapping screen with copy and download buttons; the API serves it at
`GET /api/v1/registries.yaml` and `GET /api/v1/recipes/{recipe}/registries.yaml`.
Install the file on every node, then restart the `k3s` or `rke2` service.

Both commands read the store directory (and the configuration for the
registry and prefixes), so they work on a destination-side host where no
instance is running, like [`tobby store verify`](../../reference/cli/#tobby-store-verify).
`--recipe` is repeatable and takes `name` (every recorded version) or
`name@version`; `--destination` overrides the registry for the run.

## What the aids do not cover

The mirror redirects **only the image pulls made by the node's container
runtime**. It does not apply to:

- Helm chart sources and GitOps controllers (Argo CD `repoURL`, Flux
  `HelmRepository`), which must reference the relocated chart path;
- admission policies and any tool that resolves image references itself,
  which see the reference as written in the pod spec;
- other runtimes (CRI-O, Docker): Tobby generates nothing for them in v1.

For all of those, use the `destination` column of the mapping table.
Tobby never rewrites the values of a chart: doing so would break digests
and signatures. The same warning sits in the header of every generated
snippet.

## Checking it worked

On a node configured with the snippet, `crictl pull docker.io/library/busybox:1.36`
must be served by the zone registry — its access log shows
`GET /v2/docker.io/library/busybox/manifests/1.36` — and the digest equals
the `digest` column of the table.
