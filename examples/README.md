# Example recipes for air-gapped platforms

Five recipes for software that actually gets carried into restricted zones,
plus the Retriever that ties them into one zone. They are written to be read
as much as to be run: each one carries the reasoning that produced its
ingredient list, because that reasoning is the hard part.

All five were verified against live upstreams on 2026-08-15 and validate
against the specification:

```bash
recipe lint -profile draft examples/
```

| Recipe | Profile | What it teaches |
|---|---|---|
| [`otel-collector.yaml`](otel-collector.yaml) | cooked | The nominal case — and a chart that renders **no** image by default. |
| [`keycloak.yaml`](keycloak.yaml) | cooked | A dependency the chart never mentions, and platform labels that are not what you'd type. |
| [`victoria-metrics-operator.yaml`](victoria-metrics-operator.yaml) | cooked | An operator that carries its images in its own defaults. **Read this one.** |
| [`harbor.yaml`](harbor.yaml) | draft | Single-architecture images, and mutable data that must not be pinned. |
| [`metallb.yaml`](metallb.yaml) | draft | A chart that is not an OCI artifact, and a sidecar on its own release cadence. |
| [`retriever.yaml`](retriever.yaml) | — | The desired state of a zone, with version constraints. |

**Draft or cooked.** A *cooked* recipe pins every ingredient by digest and is
what a cookbook publishes. A *draft* leaves digests out. Two of these are
drafts for the same honest reason: their Helm chart is only published to a
legacy `index.yaml` repository, so it has to be republished into a registry
**you** control before it can be an ingredient at all — and the digest is
then the one your registry returns, not one we can print here.

Take any of them, replace the `registry.example.com` placeholders with your
own hosts, adjust `platforms` to the architectures your zone runs, then cook
and publish.

## The one problem these examples are about

Carrying a Helm chart across a boundary is easy. Knowing **which container
images that chart will need, on the other side, once the door is shut** is
the part that goes wrong — and it goes wrong late, in a zone where the fix
costs a physical trip.

The reflex answer is `helm template | grep image:`. It is a good start and it
is not sufficient. Here are four ways an image escapes it, one per example.

### 1. The chart renders nothing

The OpenTelemetry Collector chart ships `image.repository: ""`. It refuses to
choose between the core and the contrib distribution, so rendering the
defaults yields an empty inventory — and a zone that receives a chart with
nothing to run.

**Rule:** inventory the manifests rendered from *your* values, never the
chart's defaults.

### 2. The chart assumes something already exists

The Keycloak chart renders exactly one image. It also requires a PostgreSQL
that it never mentions, because in a connected cluster somebody always
already has one. In a zone built from nothing, that assumption surfaces at
deployment time, after the media is sealed.

**Rule:** a rendered inventory tells you what a chart *deploys*, never what
it *depends on*. Read the values and the README for external dependencies.

### 3. The operator carries its images somewhere else entirely

This is the one that bites hardest, and the reason the VictoriaMetrics
example exists. Render its chart and you get a single image:

```
victoriametrics/operator:v0.74.0
```

Carry that in and the operator starts, healthy and green. Then someone
applies a `VMSingle`, and the pod the operator creates sits in
`ImagePullBackOff` — for an image that appeared in no manifest, no chart, and
no template.

An operator does not deploy its own image. It deploys the images written in
**its defaults**, compiled into the binary and overridable by environment
variable. So ask the operator:

```bash
docker run --rm victoriametrics/operator:v0.74.0 --printDefaults | grep -E 'IMAGE|VERSION'
```

Around twenty-five images come back, and the versions are *templates*
(`${VM_METRICS_VERSION}`, `${VM_METRICS_VERSION}-cluster`) that must be
resolved before pinning. The recipe lists the seven a single-node metrics
stack needs, and names the ones it leaves out so the omission reads as a
decision.

**Rule:** for any operator — cert-manager, Strimzi, Prometheus, ECK — find
the image list in its defaults, its CRD defaults, or its documentation.
Before the zone is sealed, not after.

A cheap safety net: pin the operator's own image variables to your
destination registry. A resource whose image was never carried then fails at
admission, in the open, rather than at pull time in a cluster nobody watches.

### 4. The chart is not an OCI artifact at all

MetalLB and Harbor publish only to `index.yaml` repositories. The recipe
format takes those out of scope on purpose ([RECIPE-SPEC §7.2][spec]): an
ingredient is an OCI artifact, full stop. Republish the chart once, into your
own registry:

```bash
helm repo add metallb https://metallb.github.io/metallb
helm pull metallb/metallb --version 0.16.1
helm push metallb-0.16.1.tgz oci://registry.example.com/charts
```

The archive is not rewritten — the bytes, and therefore the upstream
signature, survive the move.

## Three more things that are learned the expensive way

**Platform labels are matched exactly.** `linux/arm64` and `linux/arm64/v8`
are different strings, and PostgreSQL publishes the second one. Tobby fails
the ingredient rather than guessing — a loud failure beats a silent
architecture swap. Read the labels off the index instead of typing them:

```bash
oras manifest fetch docker.io/library/postgres:17.6-alpine \
  | jq -r '.manifests[].platform | .os + "/" + .architecture + (if .variant then "/" + .variant else "" end)'
```

**Not everything multi-platform is multi-platform.** Every Harbor image is a
single `linux/amd64` manifest. Platform selection only applies to an index,
so a `platforms:` list there is ignored — and tells a reader something false.
Check the media type before writing one:

```bash
oras manifest fetch --descriptor docker.io/goharbor/harbor-core:v2.15.2
```

**Mutable data does not belong in a slow recipe.** Harbor's Trivy adapter
ships the scanner, never the vulnerability database — which is re-published
several times a week. Pinning it beside a registry you upgrade twice a year
means re-cooking everything to refresh one artifact. Give fast-moving data
its own recipe, on its own cadence.

## Publishing one

A recipe is an ordinary OCI artifact. Push it, sign it, and the zone will
verify the signature before it reads the document. The layer must be named
`recipe.yaml` — that is what the artifact layout expects, whatever the file
is called on your disk:

```bash
cp metallb.yaml recipe.yaml
oras push registry.example.com/cookbook/metallb:0.16.1 \
  --artifact-type application/vnd.tobby.recipe.v1+yaml \
  recipe.yaml:application/vnd.tobby.recipe.v1+yaml

cosign sign --key cosign.key \
  --use-signing-config=false --tlog-upload=false \
  registry.example.com/cookbook/metallb@sha256:…
```

Those last two flags keep the signature verifiable offline: without them,
cosign 3.x publishes to the public Rekor transparency log — a network call a
restricted-zone signer should not make, and a lookup the destination cannot
perform.

[spec]: https://github.com/tobby-fetch/recipe-spec
