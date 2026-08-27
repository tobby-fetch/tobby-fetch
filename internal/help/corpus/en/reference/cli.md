---
title: CLI reference
description: Every tobby command with its flags and defaults, the published exit-code table, the --output json contract, non-interactive behaviour and message language.
sidebar:
  order: 1
---

This page documents the command-line interface as shipped in the current
v0.5.x series, extracted from the binary itself (`tobby <command> --help`)
and from the CLI source (`internal/cli/`).

The CLI carries a **contract an automation can depend on across versions**
(SRS FR-066, amendment R-08):

- `--output json` on every command that reports anything, with the machine
  document alone on standard output and its schema published beside the
  OpenAPI document;
- an exhaustive, published [exit-code table](#exit-codes) covered by the
  project's semantic-versioning promise — removing a code or renumbering
  one is a breaking change;
- a guaranteed [non-interactive mode](#non-interactive-behaviour): no
  command prompts, none requires a terminal;
- `--wait` on every command that starts a task on an instance.

Each of those four is held by a test that walks the real command tree, not
by this page: `internal/cli/contract_test.go` and
`internal/taxonomy/exit_test.go`.

## Command tree

```
tobby
├── serve          Run the instance (HTTP listener, embedded registry)
├── quickstart     Guided first start: answer a few questions, get a serving instance
├── sync           Trigger a synchronization on a running instance, or plan one (--dry-run)
├── export         Export the store to a standard OCI image layout
├── import         Import a standard OCI image layout into the store
├── config
│   └── dump       Print the effective configuration (secrets redacted)
├── fileset
│   └── pack       Package a local directory as a FileSet in the store
├── media
│   ├── verify     Re-verify a transported store without pushing anything
│   └── import     Verify a transported store, then push what it cleared
├── recipe
│   └── push       Validate a recipe and publish it to a cookbook
├── user
│   ├── add        Create a local account (the first account defaults to admin)
│   ├── list       List the local accounts
│   └── passwd     Change a local account's password
├── version        Print the Tobby version and build metadata
└── completion     Shell completion scripts (bash, zsh, fish, powershell)
```

The CLI deliberately does not mirror every UI screen: it covers the
automation-relevant operations (SRS FR-066). The strict parity requirement
applies between the [UI and the API](../../reference/api/).

## Common flags

Every command that loads the layered configuration accepts the same set of
flags. A flag is the highest configuration layer: it overrides the matching
environment variable and file key (see the
[configuration reference](../../reference/configuration/)).

| Flag | Default | Purpose |
|---|---|---|
| `--config <path>` | `/etc/tobby/config.yaml` | Path to the YAML configuration file. The default location is optional; an explicitly given path must exist. |
| `--mode <m>` | *(none — required to serve)* | Operating mode: `passthrough` or `mirror`. |
| `--storage-root <dir>` | *(none)* | Directory of the self-contained store. |
| `--state-root <dir>` | *(none)* | Directory of the instance state (accounts, tokens) — outside the store. |
| `--server-addr <addr>` | `:8080` | HTTP listen address. |
| `--log-level <l>` | `info` | `debug`, `info`, `warn`, or `error`. |
| `--shutdown-grace-period <d>` | `30s` | Graceful-shutdown drain budget. |
| `--proxy-url <url>` | *(none)* | Forward proxy for all outbound traffic. The proxy password is deliberately **not** a flag: flag values are visible in the process table. It comes from the configuration file or `TOBBY_NETWORK_PROXY_PASSWORD`. |
| `--tls-cert-file <pem>` | *(none)* | PEM certificate the listener presents (requires `--tls-key-file`). |
| `--tls-key-file <pem>` | *(none)* | PEM private key of `--tls-cert-file`. |

Validation is per command: a command never demands a setting it does not
use. `tobby user` needs only the state directory; `tobby recipe push` needs
only the registry-facing settings; `tobby sync` without `--dry-run` needs
neither a store nor a mode, because it drives an instance rather than the
store. None of them requires a mode.

## Report format

`--output` names the **report format** on every command, and nothing else.
It never names a destination path — `tobby export <path>` takes its
destination as a positional argument, symmetric with `tobby import <path>`.

| Value | Where | Meaning |
|---|---|---|
| `text` | default everywhere except `config dump` | The human report. |
| `json` | every reporting command | The machine document, alone on stdout. |
| `yaml` | `config dump` only, and its default | The configuration as a file you can write back. |

Two rules hold on every command:

- **The machine document is alone on standard output.** Human narration,
  structured logs, progress and audit records go to standard error, so
  `tobby … --output json | jq` composes and `tobby config dump > config.yaml`
  writes a file that is only the configuration.
- **An unknown value is a usage error** (exit code `2`), never a silent
  fallback to text.

The documents are described by a JSON Schema published beside the OpenAPI
document and served by a running instance at
`GET /api/v1/cli-output.schema.json`. It carries one entry per reporting
command, keyed by the command path. Adding a member to a document is
additive; removing one, or changing its type, is a breaking change — the
same promise the exit codes carry.

## Commands

### tobby serve

```
tobby serve [flags]
```

Runs the instance: single HTTP listener carrying the web UI, the REST API,
the embedded OCI registry (`/v2/`), the FileSet surface (`/files/`), the
probes and `/metrics`. Uses the common flags only.

Startup is fail-fast and secure by default:

- `storage.root` is required, created if missing, and probed for writability.
- Unless authentication is explicitly disabled, `state.root` is required and
  at least one local account must exist — otherwise the instance refuses to
  start with [`TBY-AUTH-001`](../../reference/errors/#tby-auth-001).
- An invalid configuration (bad proxy URL, half-declared TLS pair,
  malformed trust scope…) refuses startup with an actionable message rather
  than degrading silently.
- On SIGTERM/SIGINT, readiness flips to 503 first, then in-flight requests
  get the grace period to finish.

It reports nothing at the end because it never ends: its output is the
structured log stream, which has [its own schema](../../reference/metrics-logs/).

### tobby quickstart

```
tobby quickstart [flags]
```

Interactive first start: asks for the storage directory, the state
directory, the mode and the first administrator account, writes the
configuration file, and optionally starts serving.

| Flag | Default | Purpose |
|---|---|---|
| `--config <path>` | `./tobby.yaml` | Destination of the written configuration file. |
| `--mode <m>` | *(asked)* | Pre-answers the mode question. |
| `--storage-root <dir>` | `./storage` | Pre-answers the store question. |
| `--state-root <dir>` | `./state` | Pre-answers the state question. |
| `--password-stdin` | `false` | Read the admin password from one line of standard input. |
| `--serve` | `false` | Start the instance right after writing the configuration. |

A flag already given skips its question. Prompts go to stderr; stdout stays
machine-only. Quickstart never overwrites an existing configuration file.
It is an aid and never a requirement: without a terminal and without
answers it refuses, handing out the equivalent flag-driven commands.

### tobby sync

```
tobby sync [--wait] [--prune] [flags]              # trigger, drives an instance
tobby sync --dry-run [--retriever <src>] [flags]   # plan, drives nothing
```

**Without `--dry-run` this command drives a running instance.** It calls
`POST /api/v1/sync` — the endpoint behind the *Synchronize* button — and
reports the task the instance created. It does not synchronize by itself
and never opens the store: the instance serving that store holds it open
for writing, and a second writer is exactly what the store format forbids.

| Flag | Default | Purpose |
|---|---|---|
| `--instance <url>` | `TOBBY_INSTANCE_URL`, then the configured listen address on localhost | Base URL of the instance to drive, e.g. `https://tobby.example:8443`. |
| `--token-file <path>` | `TOBBY_API_TOKEN` when absent | File holding the static API token (operator role). The token is deliberately **not** a flag: flag values are visible in the process table. |
| `--request-timeout <d>` | `30s` | Per-request budget against the instance. It bounds a request, never the wait. |
| `--wait` | `false` | Block until the task reaches a terminal state, and exit on the task's own outcome. |
| `--wait-timeout <d>` | *(none)* | Give up waiting after this long. Giving up says so and names the task — the synchronization keeps running on the instance. |
| `--prune` | *(the instance's own setting)* | `--prune` removes content no recipe reaches any more; `--prune=false` forbids it for this run. Absent means "do what this instance does by default". |

**With `--dry-run` nothing is contacted and nothing is written.** The plan
runs locally against the store directory and reports what a synchronization
would do: the resolved versions of every recipe, the per-digest status of
every ingredient, the deduplicated volume to transfer, the projected store
size against the target's free space and filesystem capability, the content
a prune would remove, and the policy verdicts that need no transfer — the
registry allow-list and the recipes' own signatures. The automatic
reconciliation cadence of a passthrough instance is left exactly where it
was.

| Flag | Purpose |
|---|---|
| `--dry-run` | Plan mode. |
| `--retriever <src>` | Plan a candidate Retriever instead of the configured one (file path, HTTP(S) URL, or OCI reference). |
| `--skip-destination` | Do not contact the promotion destination. |

Mixing the two sets is a usage error rather than a silent no-op: a pipeline
that wrote `tobby sync --dry-run --wait` is told it waited for nothing.

```sh
tobby sync --wait                                    # trigger and block
TOBBY_API_TOKEN=… tobby sync --instance https://tobby.example:8443 --wait --output json
tobby sync --dry-run --retriever ./retriever.yaml --output json
```

Exit codes: `0` the synchronization succeeded, or the plan found nothing to
do; `5` changes are planned (`--dry-run` only); `3` refused by policy; `4`
verification failed; `1` the run or the plan could not complete.

### tobby export

```
tobby export <path> [flags]
```

Writes the local store — or a selection of it — as a standard OCI image
layout, readable by `skopeo`, `oras` and `crane` (SRS FR-051, ADR-0006).
This is the interoperability exit ramp: the content belongs to whoever
stored it and must be recoverable without Tobby.

The layout is a single uncompressed tar by default — one file crosses a
physical gap more reliably than a tree — or a directory with `--directory`.

| Flag | Default | Purpose |
|---|---|---|
| `--format <f>` | `oci-layout` | Interoperability format. Only one exists today; the flag exists so a second is additive. |
| `--directory` | `false` | Write the layout as a directory instead of a single tar. |
| `--overwrite` | `false` | Replace the destination if it already exists. Without it, an existing destination is [`TBY-LAY-003`](../../reference/errors/#tby-lay-003). |
| `--dry-run` | `false` | Report what the export would contain and how big it would be, writing nothing. |
| `--recipe <sel>` | *(all)* | Export one recipe and everything it manages (`name` or `name@version`); repeatable. |
| `--repository <repo>` | *(all)* | Export one relocated repository; repeatable. |

A recipe selection carries its ingredients, the recipe artifact, and the
cosign signature artifacts of both, in either of the layouts cosign
publishes — signatures travel with the content they attest.

Run it against a stopped instance, or use `POST /api/v1/oci-layout/export`
on a running one: two processes writing one storage directory is one
process too many.

```sh
tobby export --storage-root /var/lib/tobby /media/usb/payload.tar
tobby export --storage-root /var/lib/tobby /media/usb/payload.tar --dry-run --output json
```

### tobby import

```
tobby import <path> [flags]
```

Restores a standard OCI image layout — a directory, or an uncompressed tar
of one — into the local store, at identical digests.

The layout is untrusted data: every manifest is accepted only if its bytes
hash to the digest addressing it, every blob is committed against the
digest its manifest pins, and an archive carrying anything other than
`oci-layout`, `index.json` and `blobs/<algorithm>/<digest>` files is refused
before it is read ([`TBY-LAY-002`](../../reference/errors/#tby-lay-002)).
Compressed archives are refused too: decompress first.

| Flag | Purpose |
|---|---|
| `--format <f>` | Interoperability format (`oci-layout`). |
| `--repository <repo>` | Repository the entries belong to, for layouts that name only a tag — what `skopeo copy` produces. |

Entries are independent: one image that did not survive the medium fails on
its own line and the rest still lands. An import that lost entries exits
`1` and names them.

### tobby config dump

```
tobby config dump [--output yaml|json] [flags]
```

Prints the effective configuration — all layers merged, **secrets redacted
by construction** — on stdout, so `tobby config dump > config.yaml`
captures exactly what the instance would run with. This is the control tool
the corrective action of [`TBY-CFG-001`](../../reference/errors/#tby-cfg-001)
points at. Details in the
[configuration reference](../../reference/configuration/).

`--output json` re-encodes the same redacted document for callers that
would rather not carry a YAML parser.

### tobby fileset pack

```
tobby fileset pack <directory> <name>:<version> [flags]
```

Packages a local directory as a FileSet — a standard OCI image whose layer
is the directory's file tree — and imports it into this host's store,
pinned by its digest (SRS FR-048). The sanctioned way to serve a handful of
local files in an isolated zone: no upload endpoint is opened, the content
is addressed by digest, inventoried, scannable and removable like
everything else in the store.

Packing is reproducible: the same directory always produces the same
digest. A packed FileSet is **unsigned** and recorded as a manual import of
local origin — Tobby holds no signing key (ADR-0007) — and listings say so.
Serving it is a separate, explicit step: the command prints the
configuration block to add.

In text mode the digest alone is on stdout, so the command composes into a
script; the rest of the report is on stderr.

### tobby media verify

```
tobby media verify [flags]
```

Re-verifies a store that arrived on a physical medium and reports, without
pushing anything and **without writing to the store** — not even the
medium's own operation log.

The medium is untrusted until proven otherwise, so the order is fixed by
SRS FR-054: the manifest's completeness and checksums first, then the
recipes' signatures against **this** instance's trust roots, then every
ingredient against its pinned digest. Trust roots present on the medium are
ignored.

The report names, for every refusal, both why and **which file**. A recipe
whose signature verifies and whose every reachable file matches its pinned
digest is pushable; any other is blocked whole, with no override, and its
neighbours on the same medium are unaffected. A missing or unreadable
manifest and an altered recipe graph block the medium as a whole with no
override; a medium addressed to another zone or older than the last one
imported here block it too, and those two an administrator may waive.

| Flag | Purpose |
|---|---|
| `--zone` | The zone this instance serves. Required — from the flag, `TOBBY_ZONE`, or `zone:` in the configuration file. |
| `--output text\|json` | `json` emits the verification report itself on stdout, the same document the API returns. |
| `--allow-zone-mismatch` | Proceed on a medium addressed to another zone. On `verify` this only previews what a waived import would do. |
| `--allow-stale` | Proceed on a medium older than the last one imported for this zone. |

```sh
tobby media verify --storage-root /mnt/usb --zone production
tobby media verify --storage-root /mnt/usb --zone production --output json | jq .verdict
```

Exit codes: `0` every delivery is pushable, `3` refused by policy (zone
identity, freshness), `4` a verification failure.

### tobby media import

```
tobby media import [flags]
```

Verifies a transported store and pushes what verification cleared into the
zone registry (SRS FR-052). The order is not a sequence of steps, it is the
guarantee: nothing is pushed, served or written before the whole medium has
been re-verified.

What crosses then goes through the same controls a passthrough promotion
goes through — the registry allow-list and the recipe signatures, re-checked
over the exact bytes about to leave — only what the destination is missing
moves, and the signed recipes land in the zone's own cookbook with their
signatures. Content the medium carries that no verified recipe reaches is
reported and never pushed.

The operation journals itself onto the medium, under `_tobby/logs/`,
outside the manifest's coverage: the return audit channel of the transfer.
A completed import advances the per-zone freshness record, which is what
makes re-importing last month's medium a refusal rather than a silent
rollback.

Takes the same flags as `media verify`, plus a configured
`destination.registry` — verifying needs none, importing cannot proceed
without one. Waiving a guard is an administrator's act and is recorded in
the audit journal.

It carries no `--wait`: the task runs in this process, so the command is
already blocking. Under `--output json` its report is the task itself, in
the shape `GET /api/v1/tasks/{id}` serves it.

```sh
tobby media import --config /etc/tobby/config.yaml
```

Exit codes: `0` imported, `1` a push failed, `3` refused by policy, `4` a
verification failure.

### tobby recipe push

```
tobby recipe push <file> <registry>/<cookbook>/<name>:<version> [flags]
```

Validates a recipe document and publishes it as the OCI artifact the
[recipe specification](https://tobby-fetch.github.io/recipe-spec/) defines.
The publication is refused when:

- the document is not a valid Recipe;
- it is not fully pinned — a cookbook holds cooked recipes only: every
  ingredient carries an exact tag **and** a digest;
- its name or version contradicts the reference it is published under;
- the tag already exists with different content — a published recipe
  version is immutable
  ([`TBY-POL-004`](../../reference/errors/#tby-pol-004)). Publishing the same
  document twice is a no-op, not a conflict.

Signing stays outside Tobby, which never holds a private key. In text mode
the published digest is printed alone on stdout, ready for cosign:

```sh
tobby recipe push harbor.yaml registry.example.com/cookbook/harbor:2.15.2
cosign sign --key cosign.key --use-signing-config=false --tlog-upload=false \
  registry.example.com/cookbook/harbor@<the printed digest>
```

### tobby user

```
tobby user add <name> [--role viewer|operator|admin] [--password-stdin]
tobby user list
tobby user passwd <name> [--password-stdin]
```

Manages the instance's local accounts, directly against the state
directory on the instance host. The first account created defaults to the
`admin` role; later ones default to `viewer`. The tool computes the
password hash (argon2id) — a hash never has to be crafted by hand.

The audit record of an account operation goes to **stderr**, with every
other structured log: stdout carries the report.

### tobby version

Prints the version and build metadata on one line: version, commit, build
date, Go version, platform. Under `--output json` the same facts arrive as
separate members, plus the API major this binary speaks and the exhaustive
list of exit codes it can return. Verifying that this output matches a
signed release is covered in
[Verify a release](../../project/verify-a-release/).

### tobby completion

Generates the shell completion script for `bash`, `zsh`, `fish` or
`powershell`.

## Exit codes

The exit code classifies the outcome, so scripts branch without parsing
messages (SRS FR-066, amendment R-08). **The table below is generated from
the code** — `internal/taxonomy` — and a test fails the build if the two
ever disagree, in either direction: a code the table does not list, or a
row nothing can produce.

The table is covered by the project's
[semantic-versioning promise](../../project/release-compatibility/):
removing a code, or renumbering one, is a breaking change.

<!-- generated:exit-codes -->
| Code | Name | Meaning |
|---|---|---|
| `0` | `ok` | **Success** — The command did what was asked. |
| `1` | `failure` | **Operational failure** — Network, storage or internal error — everything that failed without a policy or a verification verdict. Every code of the operational class exits here, and so does a command that started a task the instance then failed. |
| `2` | `usage` | **Usage error** — Bad flag, unknown command, or a command asked for something it cannot honour. The message carries a `see 'tobby … --help'` hint naming the command that misparsed. |
| `3` | `policy` | **Policy refusal** — Refused by explicit policy or authorization: registry allow-list, roles, secure-by-default startup refusals, immutable recipe tags, the two waivable media guards. |
| `4` | `verification` | **Verification failure** — A failed integrity or authenticity check: signatures, pinned digests, artifact types, media checksums. The most severe class, and the one no override reopens. |
| `5` | `changes-planned` | **Changes planned** — A side-effect-free run found work to do — `tobby sync --dry-run` over a Retriever with pending changes. A success with something to say, so a gate can branch on it without treating it as a broken build. |
<!-- /generated:exit-codes -->

Every error of the [TBY-\* taxonomy](../../reference/errors/) belongs to one
of the three failure classes, so its exit code is part of the same contract
as its code. The `name` column is the stable machine name of a row: it is
part of the contract too, and is never translated.

Under `--wait`, a command that started a task exits on the **task's** own
outcome, mapped through the same table: a policy refusal on the instance is
exit `3` on the command line.

## Error output

Errors print on **stderr**; stdout is reserved for machine output. A
taxonomy error prints its structured three-part form — what happened,
probable cause, corrective action — with its stable `TBY-*` code and, when
available, the correlation identifier that finds the matching log records.
Other errors keep a plain one-line `tobby: <message>` form.

## Non-interactive behaviour

The CLI is automation-first: interactivity is an aid, never a requirement.
No command prompts, and none requires a terminal — a guarantee held by a
test that runs the whole command tree with a pipe on standard input and
fails on any command that blocks or prints a prompt.

- `tobby quickstart` refuses to run when standard input is not a terminal
  and answers are missing — the refusal message hands out the equivalent
  flag-driven commands (`tobby user add … --password-stdin`, then
  `tobby serve …`).
- `tobby user add` and `tobby user passwd` prompt for the password with
  echo disabled, twice; under `--password-stdin` they read it from the
  first line of standard input instead, and refuse an empty input. Without
  a terminal and without `--password-stdin`, they fail in the first second
  with a message naming the flag.
- Credentials are never flags. The proxy password comes from the
  configuration file or `TOBBY_NETWORK_PROXY_PASSWORD`; the API token of
  `tobby sync` comes from `TOBBY_API_TOKEN` or `--token-file`. A flag value
  is visible in the process table and in shell history.
- Prompts and progress go to stderr; only machine output goes to stdout.
- No command ever asks a question it can answer from flags, environment or
  the configuration file.

## Message language

CLI messages follow the host convention: if `LC_ALL` (checked first) or
`LANG` starts with `fr`, taxonomy errors print in French; anything else
prints English. This affects messages only — flags, commands, exit codes,
exit-code names and machine output are identical in both languages. The web
UI negotiates its own language per user, independently of the host locale.
