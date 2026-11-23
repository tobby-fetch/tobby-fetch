---
title: Air-gap on one machine
description: The removable-media journey replayed on a single workstation — a mirror instance fills a folder standing in for a USB drive, a second instance verifies it before serving anything.
sidebar:
  order: 3
---

**Going further, after [step 2](../first-promotion/).** The same signed
recipe now crosses an air gap: one instance writes it onto a removable
medium, a second one — standing in for the isolated zone — verifies the
medium before it serves a single byte. Everything runs on this machine,
and the "USB drive" is a plain folder.

:::note[You need]
- What step 2 left in `tobby-demo`: `cosign.pub`, and the `cookbook`
  registry still running on `:5001` with the signed `alpine` recipe in it
  (`docker ps` shows it; if you removed it, redo sections 1 to 4 of
  [step 2](../first-promotion/)).
- The instance from step 2 **stopped** (`Ctrl-C`): this page starts its
  own two instances, on ports `8081` and `8082`.
- Two terminals in `tobby-demo`, as before, and the outbound access to
  Docker Hub that step 2 used: the source instance fetches the image again.
:::

## How mirror mode differs

In step 2 the instance ran in *passthrough* mode: a service that
reconciles on a schedule and pushes over the network. Where no network
path exists, Tobby runs in **mirror** mode instead. Nothing runs
unattended: an operator triggers a synchronization, which fills a
transportable store; the store is carried by hand; on the far side a
second instance re-verifies everything against **its own** trust roots.

| Piece | Role |
|---|---|
| Source instance (`:8081`) | Mirror mode, on the connected side. Fetches, verifies, and writes the store onto the medium. |
| `./usb/tobby-store` | The medium. On a real drive it would be `/media/usb/tobby-store` or `E:/tobby-store`. |
| Destination instance (`:8082`) | Mirror mode, on the isolated side. Opens the same store, refuses to serve it until it has verified it. |

## 1. Address the medium to a zone

A medium is addressed to one zone: the name of the Retriever that filled
it. Write `retriever-isolated.yaml`:

```yaml
apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Retriever

metadata:
  name: isolated-zone        # the zone the medium is addressed to

spec:
  cookbook: 127.0.0.1:5001/cookbook
  recipes:
    - name: alpine
      version: "3.24.4"
```

## 2. The source instance

Write `source.yaml`. The store root points **at the medium**: in mirror
mode the store is what travels.

```yaml
mode: mirror
storage:
  root: ./usb/tobby-store      # the store IS the medium
state:
  root: ./state-source         # accounts stay on this machine, never on the medium
server:
  addr: 127.0.0.1:8081
retriever:
  source: ./retriever-isolated.yaml
trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub
registries:
  insecure: ["127.0.0.1:5001"]
```

In the first terminal, create its admin account — it reads the password
from one line on stdin — and start it:

```sh
echo 'choose-a-password' | tobby user add admin --state-root ./state-source --password-stdin
tobby serve --config source.yaml
```

`user add` answers `first account of this instance: role admin`, and the
instance log ends with `http server listening`.

## 3. Fill the medium

A mirror instance never synchronizes on its own. In the second terminal,
trigger it — or use the synchronize action of the **Recipes** screen on
`http://127.0.0.1:8081` (sign in as `admin`, with the password you just chose):

```sh
curl -u admin -X POST http://127.0.0.1:8081/api/v1/sync
```

The answer is the created task, as in step 2. Follow it on the **Tasks**
screen: it ends `done` with two items, the image and the recipe. Nothing
is pushed anywhere — there is no destination on this side.

The medium now holds a complete store, and its inventory, written last:

```sh
ls usb/tobby-store/meta/media.json
```

The **Media** screen shows the handover card: the zone the medium is
addressed to, the recipes it carries, its size.

Stop the source instance with `Ctrl-C` **before** touching the medium. A
drive is never unplugged while an instance has it open.

## 4. The isolated side

In reality this is another machine, with no route to the outside, holding
**its own** copy of `cosign.pub` — a key that travelled on the medium
would be ignored. Here the same folder plays that machine.

Write `destination.yaml`. There is no Retriever and no source registry:
the content arrives on the medium. The instance is told which zone it
serves:

```yaml
mode: mirror
zone: isolated-zone            # refuses a medium addressed to another zone
storage:
  root: ./usb/tobby-store
state:
  root: ./state-destination
server:
  addr: 127.0.0.1:8082
trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub
```

Create its account and start it, in the first terminal:

```sh
echo 'choose-a-password' | tobby user add admin --state-root ./state-destination --password-stdin
tobby serve --config destination.yaml
```

## 5. Nothing is served before verification

The content is on disk, and the instance is running. Ask it for the
image's tags, in the second terminal:

```sh
curl -u admin http://127.0.0.1:8082/v2/docker.io/library/alpine/tags/list
```

It refuses — HTTP 403, with the code `TBY-MED-030` and what to do about
it: the medium is there, but nobody has verified it on this side yet.

Verify it: open `http://127.0.0.1:8082`, sign in as `admin`, go to the **Media** screen and
choose **Verify the medium** — or call the API:

```sh
curl -u admin -X POST http://127.0.0.1:8082/api/v1/media/verify
```

The instance checks, in this order, the inventory, the digest of every
file, then the signature of every recipe against the destination's trust
root. The report starts with the verdict:

```
{"verdict":"pushable","media":{"mediaId":"…","zone":"isolated-zone", …
```

Ask for the tags again:

```sh
curl -u admin http://127.0.0.1:8082/v2/docker.io/library/alpine/tags/list
```

```
{"name":"docker.io/library/alpine","tags":["3.24.4"]}
```

The gate opened by itself the moment the medium cleared. Restart the
instance and it closes again: a verification vouches for the medium as it
was read then, not for whatever is plugged in next.

## 6. The same check from the command line

Stop the destination instance (`Ctrl-C` in the first terminal). Then
`tobby media verify` gives the
same verdict in its own process, without writing anything to the medium:

```sh
tobby media verify --config destination.yaml
```

```
medium … addressed to zone isolated-zone, resolved …, produced by tobby v0.7.0
verdict: pushable (135 checked files, 30315074 bytes)
  pushable  alpine@3.24.4 (129 files, 30311807 bytes)
```

Its exit code is `0` (`echo $?` right after the command shows it; in
PowerShell, `$LASTEXITCODE`). Now present the same medium to the wrong zone:

```sh
tobby media verify --config destination.yaml --zone another-zone
```

```
verdict: blocked (1 checked files, 2196 bytes)
  TBY-MED-006 blocking (an administrator may override it)
```

The exit code is `3`: refused by policy. The full message follows on
standard error: it names the cause, the action, and the
[error reference](../../reference/errors/#tby-med-006) entry.

:::note[If a step refuses]
Every refusal on this page names a `TBY-MED-…` code.
[Troubleshooting — a removable medium is blocked](../../reference/troubleshooting/#a-removable-medium-is-blocked)
starts from the symptom.
:::

## What this page left out

A real isolated zone usually has its own registry, and the last step of
the journey is the **push**: with `destination.registry` set on the
destination instance, the Media screen's *Push into this zone* — or
`tobby media import` — pushes what the verification cleared, exactly as
the passthrough instance did in step 2. That, the pre-flight checks
before filling a drive, and what happens to a medium over several trips
are in the air-gap section:

- [The media journey](../../air-gap/media-workflow/) — the five steps and
  who does what.
- [Prepare a medium on the source side](../../air-gap/prepare-source/).
- [Import on the isolated side](../../air-gap/import-destination/).

To clean up: `Ctrl-C` on any instance still running,
`docker rm -f cookbook zone`, then delete the `tobby-demo` directory.
