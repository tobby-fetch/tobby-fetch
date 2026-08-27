---
title: Configuration reference
description: Every configuration key with its type, default and purpose, the flags > env > file layering, the TOBBY_* mapping and a complete annotated production example.
sidebar:
  order: 2
---

This page documents the layered configuration as implemented in the current
v0.5.x series (`internal/config/`). Every key below exists in the code
today.

## Layering

Precedence, highest to lowest (SRS FR-003, verified in `config.LoadFor`):

1. **Command-line flags** — see the [CLI reference](../../reference/cli/).
2. **`TOBBY_*` environment variables.**
3. **The YAML configuration file** — `/etc/tobby/config.yaml` by default,
   or the `--config` path. The default location is optional; an explicitly
   given path must exist.
4. **Built-in defaults.**

Validation runs on the merged result, per command scope: what is set must
always be coherent, what is absent is only an error when the command needs
it. A contradictory configuration (a base path without a destination,
credentials without a proxy, half of a TLS pair) is **refused at startup**
rather than silently ignored — a setting that reads like it works must
work.

## Environment variable mapping

The rule is mechanical: `TOBBY_` plus the configuration path in upper snake
case — `network.proxy.url` becomes `TOBBY_NETWORK_PROXY_URL`. Each
supported variable is declared explicitly in the code (`internal/config/env.go`);
the full list appears in the key tables below.

Two `TOBBY_*` variables are **not** configuration keys and appear in no
table below: `TOBBY_INSTANCE_URL` and `TOBBY_API_TOKEN`, which tell a
command *which running instance to drive* and *with what credential* — see
[`tobby sync`](../../reference/cli/#tobby-sync). They configure a client,
not an instance, so an instance never reads them.

Value syntax in the environment:

- **Booleans**: `true`/`1` and `false`/`0`.
- **Durations**: Go syntax — `30s`, `15m`, `12h`.
- **Sizes**: a unit suffix (`64MiB`, `512kB`, case-insensitive) or a bare
  byte count.
- **Lists**: comma-separated (`TOBBY_REGISTRIES_INSECURE=reg1:5000,reg2`).
  Only three list keys have an environment form; every other structured or
  list-valued key (trust roots, scopes, substitutions, filesets…) is
  **configuration file only**.

## Keys

### Top level and directories

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `mode` | `passthrough` \| `mirror` | *(none — required to serve)* | `TOBBY_MODE` | The operating mode. There is deliberately no default: an instance must state what it is. |
| `zone` | string | *(none)* | `TOBBY_ZONE` | This instance's zone identity — the `metadata.name` of the Retriever that serves the zone. A source-side instance reads it from the Retriever it resolves and needs nothing here; a **destination-side** instance has no Retriever, its content arrives on a medium, and without this key it cannot tell whether a medium is addressed to it. `tobby media verify` and `tobby media import` refuse to run without it. |
| `storage.root` | path | *(none — required to serve)* | `TOBBY_STORAGE_ROOT` | The self-contained store: artifacts, recipes, task history and logs all live under it. |
| `storage.basePrefix` | repository path | *(none)* | `TOBBY_STORAGE_BASE_PREFIX` | Optional relocation base prefix, applied identically to every ingredient of the instance. |
| `storage.occupancyThreshold` | size | *(none — unmonitored)* | `TOBBY_STORAGE_OCCUPANCY_THRESHOLD` | On-disk footprint past which the store raises a **persistent warning** on every UI page, reports `exceeded` on the API, and moves the `tobby_store_occupancy_exceeded` metric. Both crossings are visible: coming back under the threshold retracts the warning and the metric. Unset means unmonitored, never "within limits" — Tobby cannot guess the size of the volume it was given. |
| `state.root` | path | *(none — required unless auth is disabled)* | `TOBBY_STATE_ROOT` | The instance state: accounts, tokens, trust-root cache, partial downloads. **Strictly outside the store** — secrets never travel on the media, and this directory is the single backup target. A state root inside the storage root (or the reverse) is refused. |

### Server

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `server.addr` | host:port | `:8080` | `TOBBY_SERVER_ADDR` | Listen address of the single listener (UI, API, registry, probes, metrics). |
| `server.secureCookies` | bool | `false` | `TOBBY_SERVER_SECURE_COOKIES` | Marks UI cookies `Secure` when a reverse proxy terminates TLS in front of a plain-HTTP listener. Stated explicitly by the operator: Tobby refuses to trust spoofable forwarding headers for security attributes. When the listener serves TLS itself, cookies are Secure regardless. |
| `server.tls.enabled` | bool | `false` | `TOBBY_SERVER_TLS_ENABLED` | Serves TLS with a **self-signed** fallback certificate (fingerprint logged at startup). Supplying a certificate pair implies TLS without this flag. |
| `server.tls.certFile` | path | *(none)* | `TOBBY_SERVER_TLS_CERT_FILE` | Administrator-supplied PEM certificate. Both or neither of the pair. Re-read when the files change on disk: replacing them replaces the served certificate without a restart. |
| `server.tls.keyFile` | path | *(none)* | `TOBBY_SERVER_TLS_KEY_FILE` | The matching PEM private key. |
| `server.tls.hosts` | list | *(none)* | *(file only)* | Extra subject alternative names for the generated self-signed certificate. Refused when a certificate is supplied — it would silently do nothing. |

### Authentication

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `auth.disabled` | bool | `false` | `TOBBY_AUTH_DISABLED` | Switches authentication off for every surface. A deliberate opt-in — settable in the file or the environment, **never by flag** — audited at startup and bannered permanently in the UI. See [Authentication, accounts and RBAC](../../security/auth-rbac/). |
| `auth.sessionTTL` | duration | `12h` | `TOBBY_AUTH_SESSION_TTL` | UI session lifetime. Sessions live in memory: a restart signs everyone out. |

### Outbound network

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `network.proxy.url` | URL | *(none)* | `TOBBY_NETWORK_PROXY_URL` | Forward proxy for **all** outbound traffic — one setting, one transport, every path. `https://` proxies are accepted. Credentials in the URL are refused: they have their own fields. |
| `network.proxy.httpsURL` | URL | *(none)* | `TOBBY_NETWORK_PROXY_HTTPS_URL` | Separate proxy for `https://` destinations when they take a different route. Empty means `url` serves both. |
| `network.proxy.noProxy` | list | *(none)* | `TOBBY_NETWORK_PROXY_NO_PROXY` | Destinations reached directly: a host, a `.suffix`, a CIDR block, or `*`. |
| `network.proxy.username` | string | *(none)* | `TOBBY_NETWORK_PROXY_USERNAME` | Proxy credential. |
| `network.proxy.password` | secret | *(none)* | `TOBBY_NETWORK_PROXY_PASSWORD` | Proxy credential. Never a flag (visible in the process table); redacted by construction in logs, errors and `config dump`. |
| `network.tls.caFiles` | list of paths | *(none)* | `TOBBY_NETWORK_TLS_CA_FILES` | PEM bundles of private certificate authorities trusted **in addition to** the public ones — for registries, Helm repositories, the retriever, trust-root URLs and the proxy hop alike. |
| `network.tls.ca` | PEM string | *(none)* | *(file only)* | Inline PEM bundle, for deployments that inject configuration but cannot mount a file. |
| `network.tls.exclusiveTrust` | bool | `false` | *(file only)* | Drops the host's public root store, leaving only the configured authorities. Only ever narrows trust. There is **no setting anywhere that disables TLS verification**. |

### Registries

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `registries.insecure` | list of hosts | *(none)* | `TOBBY_REGISTRIES_INSECURE` | Hosts reached over plain HTTP. Per-host and explicit, never a global switch. Not the answer to a private PKI — that is `network.tls`. |
| `registries.substitutions` | map host → base | *(none)* | *(file only)* | Fetch-time source substitution: a downstream zone fetches `docker.io/…` from its upstream zone registry without modifying the recipes. Changes only the endpoint contacted, never the relocated path. |
| `registries.allowlist` | list of host patterns | *(absent = unrestricted)* | *(file only)* | Bounds which registries the instance may contact at all, evaluated on the host actually reached. Absent and `[]` are different statements: absent means no restriction (reported as undeclared), `[]` means nothing is allowed. Globs: `*` within a DNS label, `**` across labels. |
| `registries.credentialsFile` | path | *(none)* | *(file only)* | A `kubernetes.io/dockerconfigjson` payload; credentials are looked up by the effective host contacted, whichever direction the bytes travel. Must sit outside the transportable store. |

### Content flow

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `retriever.source` | file path, HTTPS URL or OCI ref | *(none)* | `TOBBY_RETRIEVER_SOURCE` | Where the desired-state document comes from. See [Zone Retriever and cascading](../../passthrough/retriever-cascade/). |
| `destination.registry` | bare host | *(none — empty promotes nothing)* | `TOBBY_DESTINATION_REGISTRY` | The zone registry this instance promotes into. A bare host, never a URL and never a path: the scheme follows `registries.insecure`, the path is computed by the relocation convention. |
| `destination.basePath` | repository path | *(none)* | `TOBBY_DESTINATION_BASE_PATH` | Path prefix under which relocated ingredients land on the destination. Distinct from `storage.basePrefix` on purpose. Refused without a destination. |
| `destination.cookbook` | repository path | `cookbook` | `TOBBY_DESTINATION_COOKBOOK` | Where the zone's recipes are re-published with their signatures. |
| `sync.parallelism` | int | `3` | `TOBBY_SYNC_PARALLELISM` | Caps concurrent ingredient transfers. |
| `sync.retries` | int | `3` | `TOBBY_SYNC_RETRIES` | Per-ingredient retry attempts on transient failures (bounded backoff). |
| `sync.interval` | duration | `15m` | `TOBBY_SYNC_INTERVAL` | Reconciliation cadence, **passthrough mode only** (mirror-mode synchronization is manual by requirement). `0` disables the loop, reported at startup. An operator override persisted in the state directory wins over this value and is audited. |
| `sync.prune` | bool | `false` | `TOBBY_SYNC_PRUNE` | **Passthrough only.** Removes, at the end of every reconciliation cycle, the recipe-managed content the resolved Retriever no longer references (FR-045). Off by default: a transit store is not a delivery unit, and refreshing it never implies shrinking it. Unit imports, the offline vulnerability database and content seeded through `/v2/` are never eligible. Every removal is listed in the run log. In mirror mode the setting is refused — prune is on by default there and confirmed at trigger time, against the projected list and total size. |
| `import.inspectTimeout` | duration | `20s` | `TOBBY_IMPORT_INSPECT_TIMEOUT` | Deadline of one remote inspection on the unit-import screens; a hit maps to the dedicated [`TBY-REG-004`](../../reference/errors/#tby-reg-004). |
| `transfer.resumeThreshold` | size | `64MiB` | `TOBBY_TRANSFER_RESUME_THRESHOLD` | Blob size from which a download becomes resumable inside the blob (spooled in the state directory, resumed by HTTP Range). `0` disables in-blob resumption and streams every blob straight to the store. |
| `preflight.safetyMarginPercent` | int 0–99 | `10` | `TOBBY_PREFLIGHT_SAFETY_MARGIN_PERCENT` | Share of the target's free space held back by the pre-flight check: a synchronization is refused before any transfer when the projection exceeds free space minus this margin, stating the shortfall ([`TBY-STO-004`](../../reference/errors/#tby-sto-004)). The margin exists because the store is never the only writer on its volume. `0` restores the default — an absent key must not silently mean "fill the volume". |
| `preflight.disabled` | bool | `false` | `TOBBY_PREFLIGHT_DISABLED` | Turns the pre-flight gate into a report: volumes and filesystem verdicts are still computed and still shown, and they no longer refuse a synchronization. An explicit, announced removal of a safety check — logged at startup and again every time it lets a refusal through. |
| `tasks.keepFinished` | int | `500` | `TOBBY_TASKS_KEEP_FINISHED` | Finished tasks retained (newest first); older ones are purged with their log files. Pending and running tasks are never purged. `0` keeps the whole history. |

### Trust

Configuration file only. Verification is on by default for every recipe;
relaxation exists only as explicitly declared scopes, never a global
bypass. The full model is on
[Signatures, trust roots and allowlist](../../security/content-trust/).

| Key | Type | Purpose |
|---|---|---|
| `trust.roots[]` | list | Trusted public keys (cosign, key-based). Each root has a `name` and exactly one of `key` (inline PEM), `keyFile`, `keyURL` (`https://` only, fetched and cached at configuration time — never at verification time). Multiple roots enable rotation by overlap. |
| `trust.scopes[]` | list | Declared relaxation perimeters, evaluated in order, first match wins. Each scope has a `name`, `repositories` glob patterns on the recipe's **canonical nominal** cookbook path (`*` within a segment, `**` across; a port's `:` is written `_`), and must change something: `allowUnsigned: true` and/or a `roots` restriction. Relaxed scopes are reported on every surface, never silent. |

### FileSets

Configuration file only. Serving is disabled by default: only FileSets
listed here are served under `/files/<name>/`.

| Key | Type | Purpose |
|---|---|---|
| `files.packRoots` | list of absolute paths | Confines FileSet packing **as reached from the web interface and the API** (`POST /filesets/pack`, `POST /api/v1/filesets/pack`): those surfaces may pack a directory only if it sits under one of these paths. The default — no entry — refuses every path and hides the form, because reading an arbitrary host directory on request is a capability an instance should be *given*, not one it should have. A relative path is refused at startup. [`tobby fileset pack`](../../reference/cli/#tobby-fileset-pack) on the host itself is unaffected: whoever runs it already holds the filesystem's own rights. |
| `files.filesets[].name` | URL segment | Serves under `/files/<name>/`. |
| `files.filesets[].ref` | host + repository | The nominal ingredient reference of the FileSet — no tag or digest; the served content is whatever verified digest the store holds. |
| `files.filesets[].version` | tag | Pins the served tag; empty serves the highest semver tag present locally. |
| `files.filesets[].platform` | e.g. `linux/amd64` | Selects the platform manifest of a multi-platform FileSet; an ambiguous index without it is refused. |
| `files.filesets[].anonymous` | bool | Opts this FileSet into unauthenticated reads (bare-host bootstrap) — reported, never silent. |

### UI, logging, shutdown

| Key | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `ui.themeOverride` | path | *(none)* | `TOBBY_UI_THEME_OVERRIDE` | Operator stylesheet served after the embedded design tokens: rebranding without rebuild. The default tokens pass WCAG AA; overrides carry that responsibility. |
| `ui.showUpcoming` | bool | `false` | `TOBBY_UI_SHOW_UPCOMING` | Renders future-milestone navigation entries as inert, labeled placeholders (demo mode). Production navigation shows only what works. |
| `logging.media.file` | store-relative path | `_tobby/logs/operations.log` | *(file only)* | Where the operation log is written **on the transport medium** (mirror mode only): the return audit channel, so whoever receives the medium can read what was done with it. The path must lie outside the media manifest's coverage — under `_tobby/` — and the instance refuses to start otherwise: a log inside coverage invalidates, line by line, the inventory the destination verifies. |
| `logging.media.maxSize` | size | `10MiB` | *(file only)* | Size-based rotation threshold of that log. |
| `logging.media.keep` | int | `3` | *(file only)* | How many rotated generations to keep. Bounds the log at `maxSize × (keep + 1)` on a medium whose whole point is to carry gigabytes of content. |
| `logging.media.disabled` | bool | `false` | *(file only)* | Turns the medium's log off. Explicit and never a default: a medium arriving without one cannot be audited by whoever receives it. |
| `logging.level` | string | `info` | `TOBBY_LOGGING_LEVEL` | `debug`, `info`, `warn`, or `error`. |
| `shutdown.gracePeriod` | duration | `30s` | `TOBBY_SHUTDOWN_GRACE_PERIOD` | Drain budget after SIGTERM/SIGINT. |

There is no configuration key for the UI language: the CLI follows the host
locale, the web UI negotiates its language per user.

## Checking the effective configuration

`tobby config dump` prints the merged result of all four layers as YAML on
stdout — the exact configuration the instance would run with. **Secrets are
redacted by construction**: sensitive values are carried in a type that
cannot serialize its content, so a credential reaching the dump (or a log
line, or an error message) is impossible rather than reviewed against.
A configured secret dumps as `REDACTED`; an absent one as an empty string.

Use it as the control tool: after any change, diff
`tobby config dump` against what you intended, then restart.

## Complete production example

An annotated passthrough instance promoting into a zone registry, behind a
corporate proxy, with a private CA and a bounded registry perimeter:

```yaml
# /etc/tobby/config.yaml — passthrough instance of the "prod" zone.
mode: passthrough

storage:
  root: /var/lib/tobby/storage        # the self-contained store
state:
  root: /var/lib/tobby/state          # accounts, tokens — the backup target,
                                      # never inside the store

server:
  addr: :8080
  secureCookies: true                 # TLS terminates on the ingress in front
                                      # of this listener

network:
  proxy:
    url: http://proxy.example.com:3128
    noProxy: [registry.zone.example.com, .cluster.local]
    username: tobby-egress
    # password: set through TOBBY_NETWORK_PROXY_PASSWORD — never in a flag,
    # never printed anywhere.
  tls:
    caFiles: [/etc/tobby/pki/corporate-ca.pem]   # private CA, verification stays on

registries:
  credentialsFile: /etc/tobby/dockerconfig.json  # kubernetes.io/dockerconfigjson
  allowlist:                          # everything else is refused pre-transfer
    - registry.upstream.example.com
    - registry.zone.example.com
  substitutions:                      # fetch docker.io content from upstream zone
    docker.io: registry.upstream.example.com/docker.io

retriever:
  source: https://registry.upstream.example.com/zone/retriever.yaml

destination:
  registry: registry.zone.example.com # bare host — path comes from relocation
  cookbook: cookbook                  # where recipes are re-published, signed

trust:
  roots:
    - name: qualification-2026
      keyFile: /etc/tobby/pki/cosign-qualification-2026.pub
    - name: qualification-2025        # rotation by overlap
      keyFile: /etc/tobby/pki/cosign-qualification-2025.pub

sync:
  parallelism: 3
  retries: 3
  interval: 15m

preflight:
  safetyMarginPercent: 10           # refuse before filling the volume (FR-055)

logging:
  level: info
shutdown:
  gracePeriod: 30s
```

Deployment-specific variants (Helm values, systemd unit, offline packages)
live in [Deploy: Kubernetes and VM](../../passthrough/deploy/).
