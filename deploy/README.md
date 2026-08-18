<!--
SPDX-License-Identifier: GPL-3.0-only
Copyright © 2026 infraBuilder SASU and contributors
-->

# Reference deployment

Two ways to run a Tobby instance on Kubernetes, both producing the same
pod:

| Path | What it is |
| --- | --- |
| [`charts/tobby/`](charts/tobby/) | The Helm chart. Parameterised, documented values, the supported path. |
| [`manifests/`](manifests/) | The same deployment written out as plain YAML, for clusters that do not run Helm. |

Both give you a non-root container on a read-only root filesystem with
every Linux capability dropped, `/healthz` and `/readyz` wired as probes,
OpenMetrics on `/metrics`, and — the part that is easy to get wrong —
**two separate volumes**, one for the transportable store and one for the
instance state.

---

## The one thing to get right: store and state are not the same volume

Tobby writes to two directories, and conflating them is the mistake this
deployment is shaped to prevent.

**The store** (`storage.root`, `/var/lib/tobby/store`) holds artifacts,
recipes and operation logs. It is self-contained and relocatable: in mirror
mode this directory *is* the thing that physically crosses the air gap.
It is large, and everything in it can be fetched again.

**The state** (`state.root`, `/var/lib/tobby/state`) holds the instance's
identity: accounts, tokens, and the secrets that follow them. It is small,
and nothing recreates it.

Two consequences, and they are not stylistic:

- **The state directory is the backup target.** Losing the store costs
  bandwidth and time. Losing the state costs every account and token of
  the instance.
- **The state directory must never live on the store volume.** Secrets do
  not travel on transportable media. Tobby enforces this: it refuses to
  start when one root is nested inside the other, and the chart refuses to
  render when both mount paths are equal.

Registry credentials follow the same rule — they are a separate
`kubernetes.io/dockerconfigjson` Secret (FR-004), mounted read-only, never
written into the configuration file and never inside the store.

---

## Installing with Helm

```sh
helm install tobby ./deploy/charts/tobby \
  --namespace tobby --create-namespace \
  --set config.mode=passthrough
```

### Required values

| Value | Why |
| --- | --- |
| `config.mode` | `passthrough` or `mirror`. Tobby itself has no default here — an instance must state what it is (FR-001). The chart ships `passthrough` so that a bare `helm template` renders, but set it explicitly: it is the single most consequential thing about an instance, and it should not be inherited from a values file nobody read. Emptying it makes the chart fail to render rather than guess. |

Everything else has a working default, but four are worth setting
deliberately before the first real deployment:

| Value | Default | Set it when |
| --- | --- | --- |
| `persistence.storage.size` | `100Gi` | Almost always. Sized for the content you intend to carry. |
| `persistence.storage.storageClassName` | cluster default | The store wants capacity and the state wants durability; they are rarely the same class. |
| `config.retriever.source` | `""` | You have a desired-state document to consume (FR-010). |
| `registryCredentials` | disabled | The registries you read from or write to need authentication. |

### Registry credentials

Either hand the chart a Secret you already manage:

```sh
kubectl -n tobby create secret docker-registry tobby-registry-credentials \
  --docker-server=registry.example.com \
  --docker-username=tobby \
  --docker-password="$(cat ./token)"

helm upgrade tobby ./deploy/charts/tobby --namespace tobby --reuse-values \
  --set registryCredentials.enabled=true \
  --set registryCredentials.existingSecret=tobby-registry-credentials
```

…or let it render one from `registryCredentials.dockerconfigjson`. Prefer
the first: a value passed to Helm ends up in the release Secret.

Entries are looked up by **the host actually contacted**. With
`config.registries.substitutions` in play, that is the substitute
registry, not the nominal one.

### Configuration layers

The chart drives all three layers Tobby reads, lowest precedence first
(FR-003):

| Chart value | Becomes | Precedence |
| --- | --- | --- |
| `config` | `/etc/tobby/config.yaml`, mounted from a Secret | lowest |
| `env` | `TOBBY_*` environment variables | middle |
| `extraArgs` | flags appended after `serve` | highest |

Use `config` for everything that is not a per-environment override. The
keys mirror Tobby's own configuration structure exactly, so the output of
`tobby config dump` can be pasted in as-is.

Four keys are owned by the chart, because the pod spec depends on them
too. Setting them yourself makes the chart fail with a message naming the
value to change instead:

| Chart-owned key | Derived from |
| --- | --- |
| `storage.root` | `persistence.storage.mountPath` |
| `state.root` | `persistence.state.mountPath` |
| `server.addr` | `containerPort` |
| `registries.credentialsFile` | the registry-credentials mount |

### Optional pieces

| Value | Default | Notes |
| --- | --- | --- |
| `ingress.enabled` | `false` | Exposes the UI, the API **and** the embedded registry — see the caveat below. |
| `metrics.serviceMonitor.enabled` | `false` | Needs the Prometheus Operator CRDs. |
| `metrics.podAnnotations` | `false` | Annotation-based scrape discovery instead. |
| `serviceAccount.create` | `true` | No RBAC is attached and no token is projected: Tobby never calls the Kubernetes API. |

Reviewing what you are about to install:

```sh
helm template tobby ./deploy/charts/tobby --set config.mode=passthrough
```

---

## Installing from the raw manifests

The files in `manifests/` are numbered in apply order and all target a
`tobby` namespace they create themselves.

```sh
kubectl apply -f ./deploy/manifests/
```

Before applying, edit two files:

1. **`20-secret-config.yaml`** — set `mode`, and `retriever.source` if you
   have one. If you skip the credentials Secret, comment out
   `registries.credentialsFile` as well.
2. **`21-secret-registry-credentials.yaml`** — the shipped payload is an
   empty credential set (`{"auths":{}}`), which is valid and grants
   nothing. Replace it, or produce the Secret with
   `kubectl create secret docker-registry`, or delete the file and let
   external-secrets manage it.

The `30-persistentvolumeclaims.yaml` claims use the cluster's default
StorageClass and request 100Gi for the store and 1Gi for the state.
Adjust both before the first synchronization.

The namespace manifest enforces the **restricted** Pod Security Standard.
That is not decoration: it means a later edit weakening the pod's security
context is rejected by the API server rather than silently accepted.

---

## Creating the first account

Authentication is on by default, and Tobby refuses to serve until the
state directory holds at least one account (`TBY-AUTH-001`). A brand-new
install therefore sits in `CrashLoopBackOff` until you create the
administrator — which is the intended behaviour: no surface is ever
exposed open.

The account lives in the state volume, not in the release, so this is done
once per instance. The published image ships no shell, and
`tobby user add` reads the password from a terminal or from standard
input, so the practical route is an interactive pod that mounts the state
claim:

```sh
kubectl -n tobby scale deploy/tobby --replicas=0

kubectl -n tobby run tobby-bootstrap --rm -it --restart=Never \
  --image=ghcr.io/tobby-fetch/tobby-fetch:v0.3.0 \
  --overrides='{
    "spec": {
      "securityContext": {
        "runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
        "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}
      },
      "containers": [{
        "name": "tobby-bootstrap",
        "image": "ghcr.io/tobby-fetch/tobby-fetch:v0.3.0",
        "stdin": true, "tty": true,
        "args": ["user", "add", "admin", "--state-root", "/var/lib/tobby/state"],
        "securityContext": {
          "allowPrivilegeEscalation": false,
          "readOnlyRootFilesystem": true,
          "capabilities": {"drop": ["ALL"]},
          "seccompProfile": {"type": "RuntimeDefault"}
        },
        "volumeMounts": [{"name": "state", "mountPath": "/var/lib/tobby/state"}]
      }],
      "volumes": [{"name": "state", "persistentVolumeClaim": {"claimName": "tobby-state"}}]
    }
  }'

kubectl -n tobby scale deploy/tobby --replicas=1
```

The first account of an instance is always `admin`; the tool computes the
password hash itself, so a hash is never written by hand.

Scaling to zero first is required with `ReadWriteOnce` claims: two pods
cannot mount the state volume at the same time.

If you would rather not do this at all, `config.auth.disabled: true`
starts an instance with every surface open and a permanent warning banner
in the UI (FR-075). It is an explicit, auditable opt-in — never leave it
on.

---

## Exposing the instance

The UI, the API and the embedded OCI registry share one listener, so
whatever exposes the instance exposes all three.

For a look around, a port-forward is enough:

```sh
kubectl -n tobby port-forward svc/tobby 8080:8080
```

For real exposure through an Ingress, set the controller's limits
explicitly. An OCI registry pushes multi-hundred-megabyte blobs, and a
controller with a default body-size cap or a short read timeout fails
those pushes in ways that look like network flakiness:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "0"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "600"
  hosts:
    - host: tobby.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: tobby-tls
      hosts: [tobby.example.com]
```

Clients authenticate the standard way — `docker login`, `helm registry
login`, `oras login` — against the same accounts as the UI.

### Serving TLS from the instance itself

An Ingress terminating TLS covers the common case, and then the instance
keeps serving in the clear behind it. When nothing terminates in front —
a bare-host deployment, a zone where the hop to the pod also has to be
encrypted — the instance presents its own certificate (FR-082):

```yaml
config:
  server:
    tls:
      certFile: /etc/tobby-tls/tls.crt
      keyFile: /etc/tobby-tls/tls.key
```

Mount the pair through `extraVolumes`. The files are re-read when they
change, so cert-manager rotating a Secret in place is picked up on the
next handshake without a restart. Remember to switch the probes to
`scheme: HTTPS` in the same change — one listener carries the probes too.

With `enabled: true` and no pair, Tobby generates a self-signed
certificate and logs its SHA-256 fingerprint at startup:

```
{"level":"info","msg":"serving TLS","self_signed":true,
 "fingerprint_sha256":"A1:B2:…","requirement":"FR-082"}
```

Compare that value against what your client saw before trusting it. The
generated pair is persisted under `state.root/tls/`, so the fingerprint
survives a restart — an operator who distributed it stays right.

---

## Reaching the outside world through a proxy

In a segmented zone, direct egress is usually dropped rather than
refused, which means a misconfigured instance does not fail — it hangs on
every fetch until the deadline. Tobby has one outbound transport, shared
by every path that leaves the pod (recipe engine, unit import, Helm chart
repositories, the retriever document, trust roots fetched by URL,
publication), and it is configured once:

```yaml
config:
  network:
    proxy:
      url: http://proxy.example.com:3128
      noProxy:
        - .internal.example.com
        - 10.0.0.0/8
      username: tobby
    tls:
      caFiles:
        - /etc/tobby-ca/internal-root.pem
```

The proxy password never goes in the file or in Helm values — Helm keeps
rendered values in the release Secret. Pass it as an environment
variable from a Secret you already manage:

```yaml
env:
  - name: TOBBY_NETWORK_PROXY_PASSWORD
    valueFrom:
      secretKeyRef: {name: tobby-proxy, key: password}
```

It is redacted in every log record and in `tobby config dump` regardless
of how it arrives; the redaction is a property of the type that holds it,
not of the code that prints it.

`network.tls.caFiles` is how a registry behind an internal PKI becomes
reachable — it adds authorities, it does not weaken the check. There is
no setting anywhere in Tobby that disables certificate verification. The
neighbouring `registries.insecure` answers a different question ("this
host speaks plain HTTP"), stays per-host and explicit, and is not needed
once the authority is configured.

---

## Probes, metrics, shutdown

| Path | Role |
| --- | --- |
| `/healthz` | Liveness. Answers as soon as the listener is up: the process is alive, not necessarily useful. |
| `/readyz` | Readiness. 503 until the store and the configuration are usable, and 503 again during the shutdown drain. |
| `/metrics` | OpenMetrics. Behind the same authentication as every other surface. |

Opening a large store takes time, so the deployment uses a startup probe
(30 × 5s) rather than a lax liveness threshold: the instance gets time to
come up without a hung process surviving for minutes afterwards.

On `SIGTERM`, Tobby stops accepting new work and gives in-flight transfers
`config.shutdown.gracePeriod` to finish or checkpoint.
`terminationGracePeriodSeconds` must stay above it — the defaults are 30s
and 60s. Raise both together, or the kubelet kills the process
mid-checkpoint.

---

## Verifying the image

The published image carries SLSA Build L3 provenance and a cosign
signature, both made against the **digest**. Pin it in production
(`image.digest` in the chart) and verify before you deploy:

```sh
cosign verify ghcr.io/tobby-fetch/tobby-fetch@sha256:... \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/'
```

Full guide: [`docs/release-verification.md`](../docs/release-verification.md).

---

## Upgrading

```sh
helm upgrade tobby ./deploy/charts/tobby --namespace tobby --reuse-values \
  --set image.tag=v0.4.0
```

The deployment strategy is `Recreate` with a single replica: the old pod
releases the volumes before the new one starts, so there are never two
writers on the same store. Expect a short outage on every upgrade — that
is the trade the store's consistency is worth.

Both PersistentVolumeClaims carry `helm.sh/resource-policy: keep`, so
`helm uninstall` leaves them behind. Removing them is a deliberate,
manual act.
