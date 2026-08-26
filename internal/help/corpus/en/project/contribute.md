---
title: Contribute
description: Build from source, sign off your commits (DCO), find your way around the code, and run the full test pyramid locally.
sidebar:
  order: 6
---

Tobby is developed in the open — GPL-3.0-only, design documents first,
every decision in a public ADR. This page gets you from a clone to a
merged pull request. The workflow rules live in
[CONTRIBUTING.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/CONTRIBUTING.md);
this page adds the context.

## Build from source

Tooling is pinned in
[`mise.toml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/mise.toml)
so every machine — and CI — builds with the same versions: Go 1.25.13,
golangci-lint 2.6.1, gitleaks 8.30.1, and Node 24.18.0 with pnpm 10.33.2
(website build only; the product itself has no Node anywhere):

```sh
git clone https://github.com/tobby-fetch/tobby-fetch && cd tobby-fetch
mise install        # installs the pinned toolchain
mise run setup      # one-time: activates the git hooks (secret scan on commit)
mise run build      # produces bin/tobby
```

Only Git and the pinned Go are strictly required — `mise run build` wraps
`go build -trimpath -o bin/tobby ./cmd/tobby`.

## DCO — the sign-off every commit needs

This is the canonical explanation; other pages link here.

Every commit must carry a `Signed-off-by` trailer, certifying under the
[Developer Certificate of Origin](https://developercertificate.org) that
you wrote the change or otherwise have the right to submit it under the
project's license. Git adds the trailer for you:

```sh
git commit -s -m "feat: add per-recipe pre-flight byte estimate"
git commit --amend -s --no-edit    # forgot on the last commit
git rebase --signoff main          # forgot on a whole branch
```

There is **no CLA**. The DCO is deliberately lighter: you sign nothing
over and keep the copyright on your contributions
([ADR-0003](../../reference/srs-adr/)). Enforcement is
[`dco.yml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/dco.yml) —
a plain shell check over `git log`, no third-party action, checkout pinned
by full SHA like everything else. It blocks the pull request on any
unsigned commit and warns when the sign-off email differs from the commit
author's. New source files also carry an SPDX header
(`GPL-3.0-only`) — see CONTRIBUTING.md for the exact lines.

## The code, in broad strokes

One module, one binary (`cmd/tobby`), everything else under `internal/` in
small, strictly layered packages — the milestone-4 audit confirmed the
dependency graph has no cycles and interfaces sit on the consumer side.
Rough map:

- **Entry and surface** — `cli` (commands), `server` (HTTP wiring), `ui`
  (server-rendered templates + vendored htmx, no Node), `api` (`/api/v1`
  mirrors), `fileserve` (FileSet HTTP serving).
- **Engine** — `engine` (recipe resolution, synchronization, promotion,
  cookbook), `importer` (unit import), `blobfetch` (streaming transfers,
  fine-grained resume), `relocate` (destination naming, ADR-0013),
  `schedule`, `tasks` (persistent queue).
- **Trust and policy** — `sigverify` (both cosign layouts, offline),
  `policy` (trust roots, scopes, allowlist), `auth` (accounts, tokens,
  RBAC), `audit`, `taxonomy` (the `TBY-*` bilingual error catalog).
- **Substrate** — `store` (embedded OCI registry storage, GC), `config`,
  `netx` (the single shared outbound transport: proxy, private CAs),
  `logging`, `metrics`, `runid`, `tlsadmin`, `buildinfo`.

Test infrastructure lives in `test/` (hermetic `topology/` scenarios, the
chromedp `browser/` suite, `ocilogin/` real-client checks) and `crucible/`.

## Run the pyramid locally

The mise tasks are the same commands CI runs — see
[tests and proofs](../tests-and-proofs/) for what each gate means:

```sh
mise run test          # go test -race -count=2 ./...  (unit + integration)
mise run lint          # golangci-lint, strict profile, zero suppressions
mise run coverage      # per-package coverage floor (>= 70% per internal package)
mise run vuln          # govulncheck, reachable known vulnerabilities
mise run secrets-scan  # gitleaks over the full git history
mise run test-browser  # chromedp suite — uses your installed Chrome,
                       # never downloads one
mise run doc           # serve this website locally
```

The topology scenarios (`test/topology/scenario-m*.sh`) need Docker and
run anywhere Docker runs. Run `mise run lint` and `mise run test` before
opening a pull request; CI enforces both plus the rest of the gates, and
`main` takes changes only through a reviewed, green pull request.

## Replaying the crucible: hardware prerequisites

From the
[crucible README](https://github.com/tobby-fetch/tobby-fetch/blob/main/crucible/README.md):

- **A Linux host** with [Incus](https://linuxcontainers.org/incus/)
  installed and initialized. Milestone 1–4 scenarios run entirely in
  system containers, so any Linux host works, including cloud VMs without
  nested virtualization — milestone 1 was validated on a small disposable
  cloud instance. Later-milestone scenarios that need per-node kernels
  will require KVM (`/dev/kvm`).
- **A ZFS or btrfs storage pool** for instant snapshot-based scenario
  reset (`dir` works, but resets are slow).
- **Root on the host** — the removable medium is a loop-backed block
  device (`losetup`), the same privilege bar as Incus administration.
- **Host tooling from the environment**: `go`, `jq`, `curl`, `openssl` and
  `helm`. Nothing is downloaded at scenario time, with one deliberate
  exception: `cosign` is installed on demand by the m3/m4 scenarios when
  absent, because real signatures are the point of those scenarios.

macOS and Windows can drive a crucible host remotely over the Incus API,
but never host one. Everything runs in a dedicated Incus project
(`tobby-crucible`); `./crucible/teardown.sh` removes it entirely.
