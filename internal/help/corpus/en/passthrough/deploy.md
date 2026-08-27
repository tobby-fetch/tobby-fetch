---
title: "Deploy: Kubernetes and VM"
description: The Helm chart, the raw manifests, the Linux packages, the container image, and the first account — everything a production install needs.
sidebar:
  order: 2
---

Tobby ships in three forms, all from the same reproducible release
chain: a Helm chart (with a plain-manifest twin), Linux packages
(`.deb`, `.rpm`, `.apk`), and a container image. Whatever the form, one
rule dominates the layout.

## Store and state are not the same volume

Tobby writes to two directories:

- **The store** (`storage.root`) holds artifacts, recipes, and operation
  logs. Large, self-contained, and refetchable.
- **The state** (`state.root`) holds the instance's identity: accounts,
  tokens, served certificates, resumable-download spools. Small, and
  nothing recreates it.

The state directory is the backup target; the store costs only
bandwidth to rebuild. The two must never be nested in one another —
secrets do not travel on transportable media — and Tobby refuses to
start when they are. The Helm chart additionally refuses to render when
both mount paths are equal. See
[Operate over time](../../passthrough/operate/#backup-the-state-directory).

## Kubernetes

The reference deployment lives in the repository under
[`deploy/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/deploy):
a Helm chart at `deploy/charts/tobby/` and the same pod written out as
numbered plain YAML at `deploy/manifests/`, for clusters that do not run
Helm. Both produce a non-root container on a read-only root filesystem
with every capability dropped, probes wired, and separate
PersistentVolumeClaims for store (100Gi default) and state (20Gi
default — the state also spools resumable downloads, see
[Operate](../../passthrough/operate/#interrupted-transfers-resume)).

### With Helm

```sh
helm install tobby ./deploy/charts/tobby \
  --namespace tobby --create-namespace \
  --set config.mode=passthrough
```

`config.mode` is the one value with no usable default: an instance must
state what it is (FR-001). The chart's `config` value becomes
`/etc/tobby/config.yaml` and mirrors Tobby's own configuration keys
exactly — the output of `tobby config dump` pastes in as-is. `env` sets
`TOBBY_*` variables and `extraArgs` appends flags; precedence is
flags > environment > file (FR-003). Four keys (`storage.root`,
`state.root`, `server.addr`, `registries.credentialsFile`) are owned by
the chart and rejected if set directly, because the pod spec depends on
them.

Registry credentials are a separate `kubernetes.io/dockerconfigjson`
Secret (FR-004), mounted read-only — never in the configuration file,
never in the store:

```sh
kubectl -n tobby create secret docker-registry tobby-registry-credentials \
  --docker-server=registry.example.com \
  --docker-username=tobby \
  --docker-password="$(cat ./token)"

helm upgrade tobby ./deploy/charts/tobby --namespace tobby --reuse-values \
  --set registryCredentials.enabled=true \
  --set registryCredentials.existingSecret=tobby-registry-credentials
```

Entries are looked up by the host actually contacted — with
[source substitution](../../passthrough/retriever-cascade/) in play, the
substitute registry, not the nominal one.

### From the raw manifests

The files in `deploy/manifests/` are numbered in apply order and create
their own `tobby` namespace, which enforces the **restricted** Pod
Security Standard — a later edit weakening the pod's security context is
rejected by the API server, not silently accepted.

```sh
kubectl apply -f ./deploy/manifests/
```

Edit `20-secret-config.yaml` (set `mode`, and `retriever.source` if you
have one) and `21-secret-registry-credentials.yaml` (the shipped payload
is an empty, valid `{"auths":{}}`) before applying.

### Verify the image before deploying

The published image carries SLSA Build L3 provenance and a cosign
signature, both made against the **digest**. Pin the digest in
production (`image.digest` in the chart) and verify first:

```sh
cosign verify ghcr.io/tobby-fetch/tobby-fetch@sha256:... \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/'
```

The rolling `latest` tag is signed with the same identity but carries no
SLSA provenance — only immutable `vX.Y.Z` digests do. The full
verification guide, provenance included, is
[Verify a release](../../project/verify-a-release/).

## VM install: packages and systemd

Each release publishes `.deb`, `.rpm`, and `.apk` packages wrapping the
exact `tobby-linux-<arch>` release binary — installed to
`/usr/bin/tobby`, no install scripts, fully self-contained (CGO
disabled). They are built for repository-less, offline installation:
verify (`SHA256SUMS` or SLSA provenance), copy the file across, install
with no network:

```sh
dpkg -i tobby_0.4.2_linux_amd64.deb                    # Debian/Ubuntu
rpm -i tobby_0.4.2_linux_amd64.rpm                     # RHEL/SUSE/Fedora
apk add --allow-untrusted tobby_0.4.2_linux_amd64.apk  # Alpine
```

The packages deliberately carry no package-manager signature: trust
comes from the provenance or checksum verification performed before
installation — `--allow-untrusted` on apk states exactly that. The
reasoning is spelled out in [Verify a release](../../project/verify-a-release/).

The packages install the binary only; the service definition is yours.
This reference unit mirrors the container deployment's posture:

```ini
# /etc/systemd/system/tobby.service
[Unit]
Description=Tobby instance
After=network-online.target
Wants=network-online.target

[Service]
User=tobby
Group=tobby
ExecStart=/usr/bin/tobby serve --config /etc/tobby/config.yaml
Restart=on-failure
# SIGTERM starts the graceful drain; give it more than
# shutdown.gracePeriod (default 30s) before SIGKILL.
TimeoutStopSec=60
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/var/lib/tobby/store /var/lib/tobby/state

[Install]
WantedBy=multi-user.target
```

Create the two directories owned by the service user, put `storage.root`
and `state.root` in `/etc/tobby/config.yaml`, and keep secrets out of
the unit file — the proxy password, for instance, belongs in the
configuration file or in an environment file readable only by the
service user (see [Enterprise network](../../passthrough/network/)).

## Container image without Kubernetes

`ghcr.io/tobby-fetch/tobby-fetch` is a minimal, shell-less image running
as UID 65532. Mount the two volumes and the configuration:

```sh
docker run -d --name tobby --read-only \
  -p 8080:8080 \
  -v /srv/tobby/store:/var/lib/tobby/store \
  -v /srv/tobby/state:/var/lib/tobby/state \
  -v /srv/tobby/config.yaml:/etc/tobby/config.yaml:ro \
  ghcr.io/tobby-fetch/tobby-fetch:v0.4.2 \
  serve --config /etc/tobby/config.yaml
```

## Operating-system matrix

| Platform | Tier |
| --- | --- |
| Linux amd64 / arm64 | Validated production scope: passthrough service, packages, container image. |
| Windows amd64 / arm64 | Single static binary published, and the mirror workstation journey is validated on it (NFR-018). Passthrough on Windows is outside the validated scope. See [supported platforms](../../reference/platforms/). |
| macOS amd64 / arm64 | Convenience tier (NFR-001): same reproducible chain, distributed via `brew install tobby-fetch/tap/tobby`, full test suite in CI — but no production support implied. |

The store refuses filesystems that cannot hold its layout — FAT32 among
them; see [limits](../../discover/limits/).

## The first account, non-interactively

Authentication is on by default, and Tobby refuses to serve until the
state directory holds at least one account (`TBY-AUTH-001`) — a fresh
Kubernetes install sits in `CrashLoopBackOff` until the administrator
exists, which is intended: no surface is ever exposed open. On a
terminal, `tobby quickstart` walks you through it. For automation, every
question has a flag, and the password arrives on standard input — never
on the command line:

```sh
printf '%s\n' "$ADMIN_PASSWORD" | tobby quickstart \
  --mode passthrough \
  --config /etc/tobby/config.yaml \
  --storage-root /var/lib/tobby/store \
  --state-root /var/lib/tobby/state \
  --password-stdin
```

The equivalent primitive commands are `tobby user add admin
--state-root /var/lib/tobby/state --password-stdin` followed by `tobby
serve --config /etc/tobby/config.yaml`. The first account of an instance
is always `admin`, and the tool computes the password hash itself. On
Kubernetes, where the image has no shell, run the `user add` step in a
short-lived pod mounting the state claim — the exact recipe is in
[`deploy/README.md`](https://github.com/tobby-fetch/tobby-fetch/blob/main/deploy/README.md).

If you must run open, `auth.disabled: true` is an explicit opt-in that
puts a permanent warning banner in the UI (FR-075). Never leave it on.

Next: point the instance at the outside world through
[the enterprise network](../../passthrough/network/).
