---
title: CLI reference
description: Every tobby command with its flags and defaults, the exit-code contract, non-interactive behaviour and message language.
sidebar:
  order: 1
---

This page documents the command-line interface as shipped in the current
v0.4.x series, extracted from the binary itself (`tobby <command> --help`)
and from the CLI source (`internal/cli/`).

:::note[Upcoming — milestone 5]
A published SemVer contract for the CLI surface and a machine-readable
`--output json` mode ship with milestone 5 (R-08). Until then the command
tree and flags below are accurate but not yet frozen. Track it on the
[project status](../../discover/status/) page.
:::

## Command tree

```
tobby
├── serve          Run the instance (HTTP listener, embedded registry)
├── quickstart     Guided first start: answer a few questions, get a serving instance
├── config
│   └── dump       Print the effective configuration (secrets redacted)
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

Every command that loads the layered configuration (`serve`, `config dump`,
`media verify|import`, `recipe push`, `user add|list|passwd`) accepts the same set of flags. A flag
is the highest configuration layer: it overrides the matching environment
variable and file key (see the
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
only the registry-facing settings; neither requires a mode.

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

### tobby config dump

```
tobby config dump [flags]
```

Prints the effective configuration — all layers merged, **secrets redacted
by construction** — as YAML on stdout, so `tobby config dump > config.yaml`
captures exactly what the instance would run with. This is the control tool
the corrective action of [`TBY-CFG-001`](../../reference/errors/#tby-cfg-001)
points at. Details in the
[configuration reference](../../reference/configuration/).

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

Signing stays outside Tobby, which never holds a private key. The published
digest is printed on stdout, ready for cosign:

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

### tobby version

Prints the version and build metadata on one line: version, commit, build
date, Go version, platform. Verifying that this output matches a signed
release is covered in [Verify a release](../../project/verify-a-release/).

### tobby completion

Generates the shell completion script for `bash`, `zsh`, `fish` or
`powershell`.

## Exit codes

The exit code classifies the failure, so scripts can branch without parsing
messages (SRS FR-066). The mapping is implemented in `internal/cli/root.go`
and `internal/taxonomy`:

| Code | Class | Meaning |
|---|---|---|
| `0` | Success | The command did what was asked. |
| `1` | Operational failure | Network, storage, internal errors — everything that failed without a policy or verification verdict. |
| `2` | Usage error | Bad flag, unknown command. The message carries a `see 'tobby … --help'` hint. |
| `3` | Policy refusal | Refused by explicit policy or authorization: allowlist, roles, secure-by-default startup refusals, immutable-tag refusals. |
| `4` | Verification failure | Failed integrity or authenticity check: signatures, pinned digests, artifact types. The most severe class. |

Every error of the [TBY-\* taxonomy](../../reference/errors/) belongs to one of
the three failure classes, so its exit code is part of the same contract as
its code.

## Error output

Errors print on **stderr**; stdout is reserved for machine output (the
configuration dump, the published digest). A taxonomy error prints its
structured three-part form — what happened, probable cause, corrective
action — with its stable `TBY-*` code and, when available, the correlation
identifier that finds the matching log records. Other errors keep a plain
one-line `tobby: <message>` form.

## Non-interactive behaviour

The CLI is automation-first: interactivity is an aid, never a requirement.

- `tobby quickstart` refuses to run when standard input is not a terminal
  and answers are missing — the refusal message hands out the equivalent
  flag-driven commands (`tobby user add … --password-stdin`, then
  `tobby serve …`).
- `tobby user add` and `tobby user passwd` prompt for the password with
  echo disabled, twice; under `--password-stdin` they read it from the
  first line of standard input instead, and refuse an empty input. Without
  a terminal and without `--password-stdin`, they fail with a message
  naming the flag.
- Prompts and progress go to stderr; only machine output goes to stdout.
- No command ever asks a question it can answer from flags, environment or
  the configuration file.

## Message language

CLI messages follow the host convention: if `LC_ALL` (checked first) or
`LANG` starts with `fr`, taxonomy errors print in French; anything else
prints English. This affects messages only — flags, commands, exit codes
and machine output are identical in both languages. The web UI negotiates
its own language per user, independently of the host locale.
