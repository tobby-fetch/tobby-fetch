---
title: Helm chart values
description: Every value of the reference chart grouped by purpose, with its default and its effect, the keys the chart owns and refuses, and what the repository states about sizing.
sidebar:
  order: 16
---

The reference chart is the supported way to run Tobby on Kubernetes. This
page lists its values: what each one does, what it defaults to, and the few
keys the chart refuses to let you set. To install it, start from
[Deploy](../deploy/#with-helm); the chart's own source is
[`deploy/charts/tobby/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/deploy/charts/tobby),
and `values.yaml` there carries the same information as comments.

:::tip[In short]
- `config` becomes Tobby's configuration file; `env` and `extraArgs` layer
  over it. Four keys of that file are the chart's, and it refuses to render
  if you set them — [owned keys](#keys-the-chart-owns-and-refuses).
- The store and the state are two separate claims (100Gi and 20Gi by
  default), kept when the release is removed — [persistence](#persistence).
- Ingress, the ServiceMonitor, the alert rules and the dashboard are all
  **off** by default — [service and ingress](#service-and-ingress),
  [metrics and alerts](#metrics-alerts-and-dashboard).
- The pod runs non-root on a read-only root filesystem with every capability
  dropped — [security](#security-context-and-service-account).
- The repository measures no memory or disk figure; it gives defaults and
  one rule of thumb — [sizing](#sizing).
:::

## How the values reach Tobby

Tobby reads its settings from three layers, lowest precedence first. The
chart drives all three:

| Value | Becomes | Precedence |
|---|---|---|
| `config` | `/etc/tobby/config.yaml`, rendered into a Secret and mounted read-only | lowest |
| `env` | `TOBBY_*` environment variables on the container | middle |
| `extraArgs` | flags appended after `serve` | highest |

The keys under `config` mirror Tobby's own configuration exactly — the
[configuration reference](../../reference/configuration/) lists them all —
and the binary reads the file strictly: a key it does not know stops the
start. Everything under `config` is rendered verbatim, so this page lists
only the keys the chart's `values.yaml` itself documents.

To bring your own file, set `existingConfigSecret` to the name of a Secret
that carries a `config.yaml` key. The chart then renders no configuration
Secret and ignores the content of `config` — but it still checks `config`
against the refusals below, and the pod's restart-on-change checksum is
computed from `config`, not from your Secret: change that Secret and restart
the pod yourself.

## Keys the chart owns and refuses

The chart derives four settings of the configuration file from the values
that also drive the volume mounts and the listener. A configuration that
disagrees with its own pod is refused at `helm template` time, not
debugged at runtime.

| Configuration key | Derived from | Refused when |
|---|---|---|
| `storage.root` | `persistence.storage.mountPath` | you set `config.storage.root` |
| `state.root` | `persistence.state.mountPath` | you set `config.state.root` |
| `server.addr` | `containerPort` (as `:<port>`) | you set `config.server.addr` |
| `registries.credentialsFile` | the registry-credentials mount | you set it **while the credentials Secret is wired** (see below) |

The refusals, as Helm prints them:

```text
config.storage.root is set by the chart: change persistence.storage.mountPath instead
config.state.root is set by the chart: change persistence.state.mountPath instead
config.server.addr is set by the chart: change containerPort instead
config.registries.credentialsFile is set by the chart while registryCredentials is enabled: configure registryCredentials.existingSecret or registryCredentials.dockerconfigjson instead
```

Two more refusals protect the instance's shape:

```text
config.mode is required: set "passthrough" or "mirror"
persistence.storage.mountPath and persistence.state.mountPath must differ: the instance state never travels on the transportable store
```

`registries.credentialsFile` is the one conditional case. The credentials
Secret counts as wired when `registryCredentials.existingSecret` is set,
or when `registryCredentials.enabled` is true **and**
`registryCredentials.dockerconfigjson` is non-empty. `enabled: true` alone
wires nothing. While it is not wired the chart manages no such file, so a
`config.registries.credentialsFile` of yours — a file you mount through
`extraVolumes` — is kept as written.

Pasting the output of `tobby config dump` into `config` works once those
four keys are removed from it.

## Image and workload

| Key | Default | Effect |
|---|---|---|
| `image.repository` | `ghcr.io/tobby-fetch/tobby-fetch` | The container image. |
| `image.tag` | `""` | Image tag. Empty means `v` followed by the chart's `appVersion`: the chart version **is** the Tobby version. |
| `image.digest` | `""` | When set, the image reference is `repository@digest` and the tag is ignored. The digest is what the release signature and provenance are made against: pin it in production. |
| `image.pullPolicy` | `IfNotPresent` | Kubernetes pull policy. |
| `imagePullSecrets` | `[]` | Secrets the kubelet uses to pull the Tobby image. Unrelated to `registryCredentials`, which is what Tobby uses to reach the registries it moves content between. |
| `nameOverride`, `fullnameOverride` | `""` | Rename the chart or the resources it creates. Resources are named `<release>-tobby` (or the release name alone when it already contains `tobby`), with the suffixes `-config`, `-store`, `-state`, `-registry-credentials`, `-alert-rules`, `-dashboard`. |
| `containerPort` | `8080` | The port the process listens on. The chart derives `server.addr` from it. |
| `extraArgs` | `[]` | Flags appended after `serve`. |
| `env` | `[]` | Environment variables for the container (`name`/`value`, or `valueFrom`). |
| `envFrom` | `[]` | Whole sets of variables from ConfigMaps or Secrets. The way to supply `TOBBY_NETWORK_PROXY_PASSWORD` without putting it in a values file. |
| `terminationGracePeriodSeconds` | `60` | Time the kubelet waits after SIGTERM. Keep it above `config.shutdown.gracePeriod`. |
| `podAnnotations`, `podLabels` | `{}` | Added to the pod. |
| `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints` | `{}` / `[]` | Passed to the pod as written. |
| `priorityClassName`, `runtimeClassName`, `dnsPolicy` | `""` | Set on the pod when non-empty. |
| `dnsConfig`, `hostAliases` | `{}` / `[]` | Set on the pod when non-empty. |
| `extraVolumes`, `extraVolumeMounts` | `[]` | Extra volumes and mounts: a private CA bundle, trust roots, a theme override, a proxy configuration. Mount them read-only. |
| `extraContainers`, `initContainers` | `[]` | Sidecars (a log shipper, for instance) and init containers. |

The deployment is **one replica with the `Recreate` strategy**, and neither
is a value: the store needs a single writer, so the old pod releases the
volumes before the new one starts. A change to the rendered configuration
restarts the pod (its checksum is a pod annotation). See
[Upgrade and roll back](../upgrade-rollback/#helm-chart).

## Configuration keys the chart documents

All of these live under `config`. Defaults are the chart's; the
[configuration reference](../../reference/configuration/) is the complete
list, with environment variables and meaning.

| Key | Default | Effect |
|---|---|---|
| `config.mode` | `passthrough` | `passthrough` is a long-lived service between two connected zones; `mirror` drives a transportable store. An empty value is refused. Set it explicitly. |
| `config.logging.level` | `info` | `debug`, `info`, `warn` or `error`. |
| `config.auth.disabled` | `false` | `true` opens every surface and puts a permanent warning banner in the UI. Never leave it on. |
| `config.auth.sessionTTL` | `12h` | Lifetime of a UI session. Sessions live in memory: a restart signs everyone out. |
| `config.shutdown.gracePeriod` | `30s` | Budget for in-flight transfers to finish or checkpoint after SIGTERM. |
| `config.sync.parallelism` | `3` | Concurrent ingredient transfers. |
| `config.sync.retries` | `3` | Retries per ingredient on a transient failure. |
| `config.sync.interval` | `15m` | How often a passthrough instance re-fetches the Retriever and reconciles. `0` stops the loop; the manual trigger still works. An administrator's runtime override is saved in the state directory, so a chart upgrade does not take it back. Ignored in mirror mode. |
| `config.transfer.resumeThreshold` | `64MiB` | Blob size from which a download resumes inside the blob. Such a blob transits the state volume, so it costs its own size there. `0` disables it. |
| `config.retriever.source` | `""` | Where the desired-state document is fetched from: an HTTP(S) URL, an OCI reference, or a path in the container. |
| `config.destination.registry` | `""` | The zone registry this instance promotes into, as a bare host. Empty means nothing is promoted. |
| `config.destination.basePath` | *(unset)* | Optional path prefix under which relocated ingredients land at the destination. |
| `config.destination.cookbook` | `cookbook` | Repository under which recipes are republished with their signatures. |
| `config.network` | `{}` | Proxy (`proxy.url`, `httpsURL`, `noProxy`, `username`) and extra trust (`tls.caFiles`, `tls.exclusiveTrust`). See [Enterprise network](../network/). There is no key to disable certificate verification. |
| `config.server.secureCookies` | *(unset)* | Set to `true` when TLS ends at an ingress or a mesh, so the session cookie is marked `Secure`. |
| `config.server.tls.*` | *(unset)* | The certificate the listener presents. See the limit [below](#tls-on-the-listener-and-the-probes). |
| `config.registries.insecure` | *(unset)* | Hosts reachable over plain HTTP, one by one. |
| `config.registries.substitutions` | *(unset)* | Fetch a nominal registry from a substitute endpoint. See [the cascade](../retriever-cascade/). |
| `config.ui.showUpcoming` | `false` | Renders navigation entries of future features as inert placeholders. Off in production. |
| `config.ui.themeOverride` | `""` | Path of an operator stylesheet. Mount it through `extraVolumes`. |
| `config.trust` | `{}` | Trust roots and scopes. |
| `config.files` | `{}` | FileSets served under `/files/<name>/`. Disabled unless listed. |

## Registry credentials

Registry credentials are a separate `kubernetes.io/dockerconfigjson`
Secret, mounted read-only at `/etc/tobby-registries`. They never enter the
configuration file or the store. Entries are looked up by the host actually
contacted.

| Key | Default | Effect |
|---|---|---|
| `registryCredentials.enabled` | `false` | With `dockerconfigjson` set, wires an inline payload. Alone, it wires nothing. |
| `registryCredentials.existingSecret` | `""` | Name of a Secret you manage, of type `kubernetes.io/dockerconfigjson`. Takes precedence over `dockerconfigjson`, and wires the credentials on its own. The chart creates nothing. |
| `registryCredentials.dockerconfigjson` | `""` | An inline payload, rendered into a chart-managed Secret. Fine for a lab: Helm keeps rendered values in the release Secret, so prefer `existingSecret`. |

When wired, the chart sets `registries.credentialsFile` to
`/etc/tobby-registries/.dockerconfigjson` for you.

## Persistence

The store and the state never share a volume, and never share a mount path
(the chart refuses equal paths; Tobby refuses to start when one root sits
inside the other). See [Deploy](../deploy/#store-and-state-are-not-the-same-volume).

| Key | Default | Effect |
|---|---|---|
| `persistence.storage.enabled` | `true` | `false` falls back to an `emptyDir`: everything fetched is lost when the pod is replaced. |
| `persistence.storage.mountPath` | `/var/lib/tobby/store` | Where the store is mounted. Becomes `storage.root`. |
| `persistence.storage.existingClaim` | `""` | Use a claim you own. The chart then creates none. |
| `persistence.storage.accessModes` | `[ReadWriteOnce]` | Access modes of the claim the chart creates. |
| `persistence.storage.size` | `100Gi` | Requested size. |
| `persistence.storage.storageClassName` | `""` | Empty uses the cluster's default class. Any other value is written to the claim as is. |
| `persistence.storage.annotations` | `{}` | Added to the claim. |
| `persistence.state.enabled` | `true` | `false` falls back to an `emptyDir`, which loses every account when the pod is replaced. Defensible only in a throwaway environment. |
| `persistence.state.mountPath` | `/var/lib/tobby/state` | Becomes `state.root`. |
| `persistence.state.existingClaim` | `""` | Use a claim you own. |
| `persistence.state.accessModes` | `[ReadWriteOnce]` | As above. |
| `persistence.state.size` | `20Gi` | Requested size. Holds accounts and tokens, and the resumable downloads in flight. |
| `persistence.state.storageClassName` | `""` | As above. |
| `persistence.state.annotations` | `{}` | Added to the claim. |
| `tmpSizeLimit` | `1Gi` | Size limit of the writable `/tmp`, an `emptyDir` on the node's disk (not memory). The root filesystem is read-only, so this is the only scratch space. |

The two claims the chart creates carry `helm.sh/resource-policy: keep`:
`helm uninstall` leaves them. The state claim is the one to back up; see
[Back up and rebuild an instance](../backup-rebuild/).

:::note[The `-` storage class]
The comments of `values.yaml` describe `"-"` as disabling dynamic
provisioning. The template does not special-case it: the value is written
to the claim verbatim, so a class named `-` is what Kubernetes looks for.
Leave the key empty to use the default class.
:::

## Service and ingress

One listener carries the UI, the API, the embedded registry (`/v2/`), the
probes and `/metrics`, so one port is enough.

| Key | Default | Effect |
|---|---|---|
| `service.type` | `ClusterIP` | Service type. |
| `service.port` | `8080` | Port of the Service; it targets the container port. The Ingress also uses it. |
| `service.nodePort` | `""` | Only honoured for `NodePort` and `LoadBalancer`. |
| `service.annotations`, `service.labels` | `{}` | Added to the Service. |
| `ingress.enabled` | `false` | Renders an Ingress. It exposes the UI, the API **and** the registry. |
| `ingress.className` | `""` | `ingressClassName`, when set. |
| `ingress.annotations` | `{}` | For a registry behind an ingress, raise the controller's body-size and timeout limits here (for ingress-nginx: `proxy-body-size: "0"` and a long `proxy-read-timeout`), or large pushes fail in ways that look like network faults. |
| `ingress.hosts` | one host, `tobby.example.com`, path `/`, `Prefix` | List of hosts, each with `paths` (`path`, `pathType`, default `Prefix`). |
| `ingress.tls` | `[]` | Passed to the Ingress as written. When TLS ends here, also set `config.server.secureCookies: true`. |

## Security context and service account

| Key | Default | Effect |
|---|---|---|
| `serviceAccount.create` | `true` | Creates a ServiceAccount. |
| `serviceAccount.name` | `""` | Name to use; empty derives it from the release. |
| `serviceAccount.annotations` | `{}` | Added to the ServiceAccount. |
| `serviceAccount.automountServiceAccountToken` | `false` | No token is projected: Tobby never calls the Kubernetes API, and no RBAC is attached. |
| `podSecurityContext` | non-root, uid/gid `65532`, `fsGroup: 65532`, `fsGroupChangePolicy: OnRootMismatch`, `seccompProfile: RuntimeDefault` | Pod level. `fsGroup` makes the volumes writable by the image's user; the start check tolerates the group access this gives (see [backup](../backup-rebuild/#what-the-start-checks)). |
| `securityContext` | non-root, uid/gid `65532`, no privilege escalation, `privileged: false`, `readOnlyRootFilesystem: true`, all capabilities dropped, `seccompProfile: RuntimeDefault` | Container level. Satisfies the *restricted* Pod Security Standard as written. |

Helm merges maps with the defaults, so `--set podSecurityContext.runAsUser=1000`
changes one field. A list is replaced as a whole: setting
`securityContext.capabilities.drop` drops only what you list, not `ALL`.

## Probes

`/healthz` answers as soon as the listener is up. `/readyz` answers `503`
until the store and the configuration are usable, and again during the
shutdown drain. The probes are `httpGet` on the container port.

| Key | Startup | Liveness | Readiness |
|---|---|---|---|
| `probes.<name>.enabled` | `true` | `true` | `true` |
| `probes.<name>.initialDelaySeconds` | `0` | `0` | `0` |
| `probes.<name>.periodSeconds` | `5` | `20` | `10` |
| `probes.<name>.timeoutSeconds` | `3` | `3` | `3` |
| `probes.<name>.failureThreshold` | `30` | `3` | `3` |
| `probes.readiness.successThreshold` | | | `1` |
| Path | `/healthz` | `/healthz` | `/readyz` |

The startup probe allows 30 × 5 s = 150 seconds to open a large store
before the pod is declared dead. Raise `probes.startup.failureThreshold`
for a store that takes longer.

### TLS on the listener and the probes

If you give the listener its own certificate (`config.server.tls`), it
serves **only** TLS. The chart's probes and its ServiceMonitor speak plain
HTTP, and the chart has no value to change that: the listener answers them
`400 Bad Request` ("Client sent an HTTP request to an HTTPS server"), and
the probes fail. The path the chart is written for is TLS ending at an
ingress or a mesh, with `config.server.secureCookies: true`. If you serve
TLS from the pod, set `probes.<name>.enabled` to `false` and give up the
ServiceMonitor, or maintain your own probes through a patched copy of the
chart.

## Metrics, alerts and dashboard

`/metrics` is always served on the listener, unauthenticated like the
probes. Everything below is **off** by default, so the chart installs on a
cluster that has neither the Prometheus Operator nor Grafana. The alerts
themselves, and the thresholds' meaning, are on
[Monitoring](../monitoring/).

| Key | Default | Effect |
|---|---|---|
| `metrics.podAnnotations` | `false` | `true` adds `prometheus.io/scrape`, `prometheus.io/port` and `prometheus.io/path` to the pod, for annotation-based discovery. |
| `metrics.serviceMonitor.enabled` | `false` | Renders a ServiceMonitor. Needs the Prometheus Operator CRDs. It scrapes `/metrics` over `http`. |
| `metrics.serviceMonitor.namespace` | `""` | Namespace of the ServiceMonitor, when not the release's. |
| `metrics.serviceMonitor.interval` | `30s` | Scrape interval. |
| `metrics.serviceMonitor.scrapeTimeout` | `10s` | Scrape timeout. |
| `metrics.serviceMonitor.labels` | `{}` | Labels your Prometheus selects ServiceMonitors by. |
| `metrics.serviceMonitor.basicAuth` | `{}` | Only for an authenticating proxy in front of the instance; `/metrics` itself asks for nothing. |
| `metrics.serviceMonitor.relabelings`, `metricRelabelings` | `[]` | Passed to the endpoint as written. |
| `alerting.prometheusRule.enabled` | `false` | Renders a `PrometheusRule` with ten alerts. Needs the Operator CRDs. |
| `alerting.prometheusRule.namespace` | `""` | Namespace of the rule, when not the release's. |
| `alerting.prometheusRule.labels` | `{}` | Labels the Operator's `ruleSelector` asks for (for example `release: prometheus`). |
| `alerting.rulesFile.enabled` | `false` | The same rules as a plain Prometheus file, in a ConfigMap, key `tobby-alerts.rules.yaml`, for a Prometheus not run by the Operator. |
| `alerting.extraLabels` | `{}` | Labels added to every alert. |
| `alerting.thresholds.syncFailures` | `3` | Failed runs, within the window below, that fire `TobbySyncFailing`. |
| `alerting.thresholds.syncFailuresWindow` | `1h` | The window of the rule above; also the `for` of `TobbySyncNeverSucceeded`. |
| `alerting.thresholds.syncMaxAgeSeconds` | `21600` | Age of the last successful sync, in seconds, past which `TobbySyncStale` fires. `0` leaves out `TobbySyncStale` **and** `TobbySyncNeverSucceeded`: eight alerts remain. For mirror instances. |
| `alerting.thresholds.saturationFor` | `15m` | How long transfers stay pinned at `sync.parallelism`. |
| `alerting.thresholds.storeOccupancyFor` | `15m` | How long the store stays beyond `storage.occupancyThreshold`. |
| `alerting.thresholds.scannerFor` | `1h` | How long the vulnerability database or the scanner stays degraded. |
| `alerting.thresholds.certificateExpirySeconds` | `1209600` | Remaining validity, in seconds (14 days), under which the listener's certificate alerts. |
| `alerting.thresholds.certificateExpiryFor` | `10m` | The `for` of the certificate alert. |
| `alerting.thresholds.clockFor` | `5m` | How long the clock stays implausible. |
| `dashboard.enabled` | `false` | Renders a ConfigMap carrying a Grafana dashboard (`tobby-dashboard.json`). |
| `dashboard.namespace` | `""` | Namespace of the ConfigMap, when not the release's. |
| `dashboard.labels` | `grafana_dashboard: "1"` | Label the Grafana sidecar watches. |

`TobbyStoreOccupancyHigh` only has something to compare when you set
`storage.occupancyThreshold` in `config`.

## Sizing

The repository states few numbers, and measures none of the following. What
it does state:

| Resource | Chart default | What the chart says about it |
|---|---|---|
| CPU | request `100m`, **no limit** | Throttling a transfer to save cycles nobody else wants is a bad trade. |
| Memory | request `256Mi`, limit `1Gi` | A "starting point, not a recommendation": the right ceiling depends on store size and sync parallelism, and a limit below the working set gets the pod OOM-killed mid-transfer. |
| Store volume | `100Gi` | "Sized for the content you intend to carry." Set `storage.occupancyThreshold` so the instance warns you before the volume fills. |
| State volume | `20Gi` | Accounts and tokens are small. Every blob at or above `transfer.resumeThreshold` (`64MiB`) transits it first, so leave room for the largest layers in flight at once, or lower the threshold. |
| `/tmp` | `1Gi` | Node-backed scratch space; the only writable path outside the volumes. |
| Time to start | 150 s | The startup probe's budget. |

The resource defaults are a request and one limit, set in `resources`:

```yaml
resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    memory: 1Gi
```

No memory or CPU figure measured on a given store size, no blob-size or
parallelism curve, and no store-size projection is published in the
repository or on this site. Treat the defaults as a first guess and watch
the pod's own usage for a few weeks. For a mirror, `tobby sync --dry-run`
projects what a cycle will write
([sizing a cycle](../../air-gap/manage-media/#sizing-a-cycle)).

## Verify what you set

Render before you install; the refusals above appear here, not on the
cluster:

```sh
helm lint ./deploy/charts/tobby --values my-values.yaml
helm template tobby ./deploy/charts/tobby --values my-values.yaml
```

From a checkout, `sh tools/chart-owned-settings.sh` replays the cases of
the owned-keys table against the chart.
