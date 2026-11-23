---
title: Upgrade and roll back
description: Move an instance to a newer version per install method, what the state directory and the store do across versions, how to verify the result, and what a rollback can and cannot promise.
sidebar:
  order: 13
---

An upgrade replaces the program and leaves the data where it is. That is
what makes it short, and it is also what makes a rollback harder than it
looks: the old program meets data that the new one has touched. This page
gives the steps per install method, what the code checks on the way, and
where its guarantees stop.

:::tip[In short]
- Before anything: back up the state directory, the configuration and the
  secret files it references ([backup](../backup-rebuild/)), and read the
  release's changelog for breaking changes — [before you start](#before-you-start).
- Nothing in Tobby converts data from one format to another. A build
  either reads the formats it finds or refuses to start —
  [what the code checks](#what-the-code-checks-across-versions).
- Upgrade by replacing the package, the image tag or the chart version;
  the instance restarts and is unavailable for that time —
  [per install method](#upgrade-by-install-method).
- Check the result with `tobby version`, `/readyz`, `tobby state inspect`
  and a dry-run synchronization — [verify](#verify-the-result).
- A rollback of the program is easy. A rollback of the data is only
  guaranteed one way: restoring the backup taken before the upgrade —
  [roll back](#roll-back).
:::

## Before you start

1. **Read the release's changelog**, at the top of its entry:
   [breaking changes](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md)
   are listed there, with what to change. The configuration file is read
   strictly (see below), so a renamed or removed key is a refusal to
   start, not a silent default.
2. **Back up** what [Back up and rebuild an instance](../backup-rebuild/)
   lists: the state directory with its permissions, the configuration file
   and the secret files it references. This is the copy a rollback depends
   on.
3. **Decide what to do about the store.** A passthrough store is a cache:
   it can be refilled by the next synchronizations, so the state backup is
   enough. A mirror store *is* the medium: take its own copy (a volume
   snapshot, `cp -a`) if you want to be able to return to the exact bytes.
4. **Note the running version** so you know what to go back to:
   `tobby version` on the host, the `/about` page, or the `version` label
   of the `tobby_build_info` metric.
5. **Check the state directory against the new build, before the switch.**
   `tobby state inspect` runs the checks the start would run on the state
   directory (layout version, permissions of the secret entries) and exits
   non-zero with the same refusal. Run the *new* binary against the current
   state, while the old instance still serves:

   ```sh
   tobby state inspect --state-root /var/lib/tobby/state
   ```

   It is safe beside a running instance, and it does not look at the store
   or at the configuration file.
6. **On Kubernetes with Helm, keep the values you deployed with**:

   ```sh
   helm get values tobby --namespace tobby --output yaml > values-before-upgrade.yaml
   helm history tobby --namespace tobby
   ```

   The revision number `helm history` shows is what a rollback names.

## What the code checks across versions

Four things are versioned on disk, and the configuration file is read
strictly. Nothing else carries a version number.

| What | Where | Today | What a build that does not know the version does |
|---|---|---|---|
| State directory layout | `layout.json` in `state.root` | `1` | **Refuses to start**: [`TBY-STA-001`](../../reference/errors/#tby-sta-001). |
| Local accounts file | `version` field of `accounts.yaml` | `1` | **Refuses to start**, with `accounts.yaml has unsupported version <n> (this build supports 1)`. |
| Store format | `meta/format.json` in `storage.root` | `1` | **Refuses to open the store**, with `store: format version <n> is not supported by this build (it supports version 1): use a Tobby release matching the store, or export/import the content with standard OCI tooling (skopeo, oras, crane) through the OCI image layout`. |
| Media manifest and its store format | `meta/media.json` on a transported store | `1` and `1` | **Blocks the whole medium**: [`TBY-MED-003`](../../reference/errors/#tby-med-003) and [`TBY-MED-004`](../../reference/errors/#tby-med-004). |
| Configuration file | `/etc/tobby/config.yaml` | no version | **Refuses to start** on a key it does not know: `field <key> not found in type config.Config`. |

Three consequences, all read from the code:

- **There is no migration step.** No code converts a state directory, a
  store or a medium from one format to another. The checks above are
  comparisons of an integer; a bump of one of them would be a documented
  event of its own. The store format is `1` in v0.4.0, v0.5.0, v0.6.0 and
  in the development branch, so no release so far has changed it.
- **The checks refuse in both directions.** The store and the media
  manifest are compared for *equality*, so a store stamped by a newer
  format is refused by an older build exactly as the reverse. The state
  layout is refused when it is *higher* than the build knows.
- **The first start of a build may create files.** A state directory with
  no `layout.json` is *adopted*: the build checks the permissions of the
  secret entries, then writes the layout record. If those permissions are
  too broad the start is refused with
  [`TBY-STA-002`](../../reference/errors/#tby-sta-002), and nothing is
  adopted. A start also marks the store with its format version when it
  has none. Builds up to v0.6.0 have no layout check at all, so an
  upgrade from them is the first time a directory is checked. The step
  before, `tobby state inspect`, tells you.

What a start also does, and what it does not:

- **Interrupted work resumes.** A task found pending or running when the
  instance starts is queued again; transfers are incremental by digest, so
  a re-run moves only what is missing. A graceful stop gives in-flight
  transfers `shutdown.gracePeriod` (30 seconds by default) to finish or
  checkpoint — see [graceful shutdown](../operate/#graceful-shutdown).
- **Sessions do not survive.** UI sessions live in memory, so everyone is
  signed out. Tokens and accounts are in the state directory and stay.

How far the compatibility promise reaches, and what freezes at 1.0, is
stated once, on [Releases and compatibility](../../project/release-compatibility/).
It is not repeated here.

## Upgrade by install method

Every method has the same shape: stop the old program, start the new one
on the same two directories. The instance is unavailable in between — the
store needs a single writer, so there is no rolling upgrade (see
[limits](../../discover/limits/)). A new version is verified before it is
installed, as [Verify a release](../../project/verify-a-release/) shows.

### Linux package

The package carries the release binary at `/usr/bin/tobby` and runs no
script at install, so installing it never restarts anything. Install it
over the old one, then restart the service your unit defines:

```sh
dpkg -i tobby_<version>_linux_amd64.deb                    # Debian/Ubuntu
rpm -U tobby_<version>_linux_amd64.rpm                     # RHEL/SUSE/Fedora
apk add --allow-untrusted tobby_<version>_linux_amd64.apk  # Alpine
systemctl restart tobby
```

The unit is yours (see [Deploy](../deploy/#vm-install-packages-and-systemd)):
a restart sends SIGTERM and waits for the drain, so keep `TimeoutStopSec`
above `shutdown.gracePeriod`. The configuration file and both directories
are not part of the package and are not touched.

### Binary, Homebrew, Scoop, winget

A mirror workstation, or any host that runs the binary directly, upgrades
by replacing the file. Stop the instance first if one is running from it.

```sh
brew upgrade tobby-fetch/tap/tobby      # macOS
scoop update tobby                      # Windows
winget upgrade tobby-fetch.tobby        # Windows; can lag a few days behind a release
```

Or download the new release asset and replace the old file, as
[Install and start](../../try/install-and-start/) shows. The store and the
state directory stay where they are. On a medium that crosses an air gap,
read [Updating Tobby in an isolated zone](../../air-gap/update-tobby/):
the new version itself travels as a signed recipe.

### Container image

Pull the new tag, remove the container, run it again with the same
mounts. The image is stateless; the two volumes are the instance.

```sh
docker pull ghcr.io/tobby-fetch/tobby-fetch:v<version>
docker stop tobby && docker rm tobby
docker run -d --name tobby --read-only -p 8080:8080 \
  -v /srv/tobby/store:/var/lib/tobby/store \
  -v /srv/tobby/state:/var/lib/tobby/state \
  -v /srv/tobby/config.yaml:/etc/tobby/config.yaml:ro \
  ghcr.io/tobby-fetch/tobby-fetch:v<version> \
  serve --config /etc/tobby/config.yaml
```

`docker stop` waits ten seconds by default before it kills the process;
pass `--timeout 60` to leave the drain its 30 seconds. Pin an exact tag or a
digest: the rolling `latest` tag is an upgrade you did not choose.

### Helm chart

The chart version **is** the Tobby version, so moving the chart moves the
image:

```sh
helm upgrade tobby oci://ghcr.io/tobby-fetch/charts/tobby --version <version> \
  --namespace tobby --values values-before-upgrade.yaml
```

Passing the values you saved, rather than `--reuse-values`, makes every
value explicit. If you do use `--reuse-values`, an `image.tag` set by an
earlier upgrade is kept and the image does not move: clear it with
`--set image.tag=`, or pin `image.digest`. The deployment is one replica
with the `Recreate` strategy: the old pod releases the volumes before the
new one starts. Both claims carry `helm.sh/resource-policy: keep`, so
neither an upgrade nor `helm uninstall` removes the data.

A change in the rendered configuration restarts the pod (a checksum of it
is an annotation of the pod template). With `existingConfigSecret`, the
checksum is computed from the `config` value, not from your Secret:
changing that Secret does not restart the pod by itself. Every chart value
is in [Helm chart values](../chart-values/).

### Plain Kubernetes manifests

The image line of `deploy/manifests/40-deployment.yaml` pins a tag, and
that tag is not moved for you when a release is cut: set it to the version
you deploy, then apply:

```sh
kubectl apply -f ./deploy/manifests/
kubectl --namespace tobby rollout status deployment/tobby
```

The strategy is `Recreate` here too. Compare the manifests of the old and
the new release before applying: the other changes of a release (security
context, probes, claims) arrive only if you take them.

## Verify the result

In this order, stopping at the first one that fails:

1. **The process starts.** A refusal names its reason in one `TBY-…` line
   ([Troubleshooting](../../reference/troubleshooting/#the-instance-does-not-start)).
   On Kubernetes a refusal shows as a pod that restarts in a loop; read its
   logs.
2. **It is the version you expect.** The startup record `tobby starting`
   carries `version` and `commit`; `tobby version` prints them for the
   binary on the host; `/about` and the `tobby_build_info` metric say what
   is running.
3. **It is ready.** `GET /readyz` answers `200` once the store and the
   configuration are usable; until then it answers `503`. `/healthz` only
   says the process is up.
4. **The identity is intact.** `tobby state inspect` shows no missing
   identity entry, and the startup log holds no
   [`TBY-STA-003`](../../reference/errors/#tby-sta-003) warning.
5. **The store is intact.** `tobby store verify --instance <url> --wait`
   (add `--token-file` when authentication is on) re-hashes every blob, checks the manifests and the recorded signatures
   against the trust roots configured now, and lists orphans, without
   writing to the store. It is a tracked task on the instance. See
   [Verify the store's integrity](../verify-store/).
6. **The next synchronization is what you expect.** `tobby sync --dry-run`
   plans it without doing it: exit code `5` means changes are planned,
   which is not an error. After a version that changes how content is
   verified or scanned, the plan can differ from the one before.

Then watch the [alerts](../monitoring/) for a day. A sync that fails right
after an upgrade names its cause in the failed task.

## Roll back

Say first what a rollback is. The *program* goes back by starting the old
version. The *data* — the state directory and the store — does not go
back by itself: it stays as the new version left it. Whether the old
program is happy with that is the question.

### What the code guarantees

- An older build **refuses** a state layout, a store format or a media
  manifest format higher than the ones it knows, with the messages above.
  It does not start on data it can tell it does not understand.
- That refusal is loud and early: it happens at start, before the instance
  serves anything.
- The configuration is read strictly: an older build refuses a key the
  newer one added.

### What it does not guarantee

- **Nothing makes the data go back.** There is no downgrade step and no
  record of what the newer version changed. `layout.json` states which
  build adopted the directory, not which one used it last.
- **Same format number does not mean same content.** All four format
  numbers are `1` today, so a build one release older passes every check
  above. The check compares one integer; it does not know whether the
  newer build wrote a field, a file or an annotation the older one does not
  handle. The metadata files of the store are plain JSON read without a
  strict decoder, so an older build ignores a field it does not know and
  writes the file back without it. This comes from reading the code; the
  project does not test a downgrade across releases.
- **Builds up to v0.6.0 do not check the state layout at all.** Going back
  to one of them removes the protection itself.
- **A recipe or Retriever written for a newer format** is a document
  problem, not a data one: an older build refuses an `apiVersion` it does
  not know when it reads the document.
- **Before 1.0, nothing promises more.** The store format is promised to
  stay readable in both directions across 1.x; see
  [Releases and compatibility](../../project/release-compatibility/).

### The rollback that is guaranteed: restore, then start the old version

The one path the page can stand behind is the one the
[backup](../backup-rebuild/) page tests: put back the state directory,
the configuration and the secret files taken **before** the upgrade, with
their permissions, and start the version that wrote them. Accounts and
tokens issued after the upgrade are gone with it. For the store:

- **Passthrough:** keep it as it is, or restore its own copy if you took
  one. It holds content, not identity; the next synchronization reconciles
  it with the Retriever.
- **Mirror:** restore the copy you took before the upgrade. A store that
  the newer version wrote is not known to be readable by the older one
  beyond the format gates.

In practice:

- **Package:** install the previous package file with your package
  manager's downgrade path (`rpm` needs `--oldpackage`), restore the
  state, restart.
- **Container:** run the previous tag on the restored volumes.
- **Helm:** `helm rollback tobby <revision> --namespace tobby`, with the
  revision `helm history` showed before the upgrade. It restores the
  deployment and the configuration Secret of that revision. It does **not**
  restore the two claims: they keep the data the new version wrote, so
  restore the state from the backup too, with the instance stopped (see
  [rebuild an instance](../backup-rebuild/#rebuild-an-instance)).
- **Manifests:** put the previous image tag back and apply, with the same
  restore.

If the new version has already run for a while and a rollback would lose
accounts, tokens or imports you want to keep, do not roll back the data:
read the refusal or the failed task, and fix forward.

A medium produced by a newer version and carried to an older destination
is refused only when its manifest or store format number differs
([`TBY-MED-003`](../../reference/errors/#tby-med-003),
[`TBY-MED-004`](../../reference/errors/#tby-med-004)). The code compares
no other version, so keep both sides on the same release when you can.
