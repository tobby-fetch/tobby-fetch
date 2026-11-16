---
title: Back up and rebuild an instance
description: What makes an instance this instance, the one directory to back up besides the store, the check Tobby runs on it at startup, and a rebuild procedure that is tested end to end.
sidebar:
  order: 9
---

An instance is two things: its **content** and its **identity**. The content
is the store, and everything in it can be fetched again. The identity is
what no synchronization recreates — the accounts, the tokens, the
certificate clients pinned, the record that stops last month's medium from
being imported again. Tobby keeps that identity in one place, the **state
directory** (`state.root`), with a layout that is documented, versioned and
checked at every start. This page is what to back up, what the start refuses,
and how to rebuild.

## What the state directory holds

Every entry is either **identity** — a rebuild needs it, back it up — or
**discardable** — a cache or a spool, rebuilt on demand, not worth carrying.
Nothing else is written there.

| Entry | Class | Secret | What it holds | If it is lost |
|---|---|---|---|---|
| `layout.json` | identity | no | The layout version of the directory. | Recreated at the next start. |
| `accounts.yaml` | identity | **yes** | Local accounts with their password hashes, and static tokens as hashes. | Every account and token is gone; the instance refuses to start with no account. |
| `tls/` | identity | **yes** | The generated self-signed certificate and its private key, or an adopted pair. | A new certificate is generated and clients see a **new fingerprint**. |
| `schedule.json` | identity | no | The refresh interval an administrator set at runtime. | The configured interval applies again. |
| `media-imports.json` | identity | no | Per zone, the resolution time and identifier of the last medium imported. | The staleness guard forgets the last import: an older medium is accepted again. |
| `trust-cache/` | discardable | no | Trust roots fetched from HTTPS URLs when the configuration was read. | Fetched again. |
| `partials/` | discardable | no | Resumable partial downloads. | A blob restarts at zero. |
| `scanner/` | discardable | no | The vulnerability scanner's private work directory. | Rebuilt from the delivery. |
| `serving.json` | discardable | no | Marks the instance serving the store, for the local command line. | Recreated at the next start. |

The state directory lives outside the store and never inside it
(NFR-020): the store leaves the site on a medium, the state does not.

## What to back up

Four things make up an instance. Together they are the whole backup:

1. **The state directory**, its identity entries (the discardable ones may be
   copied or skipped).
2. **The configuration file** (and the `TOBBY_*` environment your deployment
   injects, if any).
3. **The secret files the configuration references**: `registries.credentialsFile`,
   `server.tls.keyFile` and its certificate when you supply your own pair,
   `auth.oidc.clientSecretFile`, `auth.saml.keyFile`. They are outside the
   store and outside the state directory by design.
4. **The store**, or the means to rebuild it: either its own copy, or a
   re-synchronization (see below).

`tobby state inspect` lists the directory with the class, size and
permissions of each entry — names and sizes, never contents — and is safe to
run beside a running instance:

```sh
tobby state inspect --config /etc/tobby/config.yaml
tobby state inspect --state-root /var/lib/tobby/state --output json
```

The same inventory is on the administration screen **State**
(`/admin/state`) and at `GET /api/v1/state`, both for administrators, and it
is recorded in the [diagnostic bundle](../diagnostics/). On Kubernetes, the
reference chart keeps the state on its own volume
(`helm.sh/resource-policy: keep`): snapshot that volume.

## Secrets at rest

Tobby protects its secret entries and says exactly how far:

- Secret entries are files created **mode 0600** (an owner-only access list
  on Windows). Credentials Tobby verifies are stored **hashed**, never in
  clear: passwords with argon2id, tokens as digests. The exceptions are the
  private key of the listener's certificate, which has to be usable, and the
  files you point the configuration at.
- Tobby does **not encrypt** them. Encryption at rest is the volume's job —
  an encrypted disk, an encrypted persistent volume, BitLocker. A backup of
  the state directory carries the same hashes and the same private key: store
  it with the protection you give the volume.
- An encrypted backup bundle produced by Tobby itself is outside version 1.

## What the start checks

At startup, before anything reads from it, Tobby checks the state directory:

| Condition | Result |
|---|---|
| The layout version is one this build does not know, or the layout file cannot be read | The instance **refuses to start**: [`TBY-STA-001`](../../reference/errors/#tby-sta-001). |
| A secret entry is readable by **every user of the host** | The instance **refuses to start**, naming the file: [`TBY-STA-002`](../../reference/errors/#tby-sta-002). |
| An identity entry this configuration depends on is missing from a directory that has run before | The instance starts and **says so**: a warning record per entry, [`TBY-STA-003`](../../reference/errors/#tby-sta-003). |
| A discardable entry is missing | Recreated silently. |

"Readable by every user" is any permission for *other* on Unix, and an access
list granting Everyone, BUILTIN\Users or Authenticated Users on Windows.
**Group access is tolerated**: a Kubernetes `fsGroup`, as in the reference
chart, gives the volume's files to the pod's own group, and refusing that
would refuse every real deployment. The check applies to the key files inside
`tls/`, not to the certificate beside them, which is public.

A new directory is **adopted**: Tobby records its layout version and starts.
A directory from before this check is adopted the same way, after the
permission check.

## Rebuild an instance

On a host the instance has never run on (or after a loss):

1. Install the **same Tobby version** — or a later one: the layout is
   versioned, entries may be added and are never renamed or repurposed, and a
   version that does not know the layout refuses rather than guesses.
2. Restore the **state directory** with its permissions: `tar -xpf`,
   `rsync -a`, `cp -a`, a volume snapshot; on Windows `robocopy /COPYALL` or a
   snapshot. A restore that lost the permissions is not a mystery: the start
   refuses and names the file. Fix it (`chmod o-rwx` on Unix; remove Everyone
   and Users from the access list on Windows), check who could read the file
   meanwhile, and start again.
3. Restore the **configuration** and the **secret files** it references, at
   the paths it names.
4. Put the **store** back: restore its own copy, or start empty and
   re-synchronize. A mirror store is a medium; a passthrough store is a
   cache. Nothing in the state depends on the store's content.
5. Start the instance. Read the startup records: a `TBY-STA-003` warning
   names an identity entry the backup did not carry.

What you get back, and what is tested: the original accounts sign in with
their passwords; a token issued before the loss still authenticates; the
listener presents the **same certificate fingerprint**; the import record
refuses the same stale medium; the runtime interval override holds. The
repository's end-to-end suite does exactly this — stop an instance, copy the
state, configuration and store to a fresh place, start there, and compare —
and the crucible scenario `b-rebuild` repeats it between two nodes with no
shared volume.

If you supply your own certificate (`server.tls.certFile`, `keyFile`), the
fingerprint depends on those files, not on the state directory: they are item
3 above.

## Limits

- The check is on **secret entries**. It does not look at the permissions of
  the directory that contains the state directory, nor at the store or at
  files you reference from the configuration.
- Undocumented files found in the state directory are listed by `tobby state
  inspect` and left alone.
- Older versions do not read a directory written by a newer layout: restore
  the version that wrote it, or a backup made with the version you run.
