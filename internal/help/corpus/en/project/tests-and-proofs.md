---
title: Tests and proofs
description: The five-level test pyramid, the CI gates as they are actually wired, and how to replay the crucible yourself.
sidebar:
  order: 2
---

Quality is an entry barrier, not a final phase: the pyramid and the
target-environment replica existed before the features, and nothing merges
red. This page describes the gates as they are wired today — every claim
links to the workflow or directory that implements it.

## The five-level pyramid

1. **Unit** — `go test -race -count=2 ./...`: race detector always on, and
   every test runs twice in the same process, so a test that only passes
   once never merges.
2. **Integration** — in the same suite, against real protocols: the OCI
   protocol is never mocked (a real embedded registry on ephemeral ports),
   and `docker login`, `helm login` and `oras login` are exercised over
   real sockets against pinned client binaries.
3. **End-to-end core** — hermetic topology scenarios on every pull request:
   containerized multi-zone topologies replaying each milestone's journey.
4. **Browser non-regression** — a real Chrome driving the UI, for the bug
   class that lives in an attribute the client interprets.
5. **Crucible** — the realistic tier on real disposable nodes, gating
   milestone acceptance rather than merges.

## The CI gates, as wired

[`ci.yml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/ci.yml)
runs nine blocking jobs on every pull request; its header states the
contract: *every job gates merges*. By name:

| Job | What blocks the merge |
|---|---|
| `lint` | golangci-lint v2.6.1, strict profile, zero suppressions |
| `test` | race + `-count=2` suite on linux/amd64, linux/arm64 **and** macOS, then the per-package coverage floor |
| `cross-compile` | all six release targets (linux, windows, darwin × amd64/arm64) must build |
| `licenses` | every dependency — test-only tree included — against the GPL-3.0 compatibility allowlist |
| `e2e-topology` | the milestone-1 to milestone-4 hermetic scenarios, raw check reports published to the job summary |
| `e2e-browser` | chromedp scenarios, with `TOBBY_E2E_REQUIRE_CHROME=1` so "no browser" is a failure, not a skip |
| `govulncheck` | reachable known vulnerabilities, stdlib included — a finding is a call path, not a manifest match |
| `secrets` | gitleaks over the full git history, not the checkout tip |
| `vulnerabilities` | Trivy on dependencies; critical and high fail the job |

Third-party actions are pinned by full commit SHA, and downloaded tools
(helm, oras, gitleaks) are verified against pinned SHA-256 digests before
use (ADR-0011). Per
[CONTRIBUTING.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/CONTRIBUTING.md),
`main` is protected: no direct pushes, and a pull request needs an
approving review and a green CI run to merge. There is also a
[DCO gate](../contribute/) on every commit.

## Structural proofs

Some tests exist to make a *class* of regression impossible, not to check
one behavior:

- **The anti-drift RBAC test.** The permission matrix
  ([docs/rbac-matrix.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/rbac-matrix.md))
  is enforced by a table that fails the suite when a route is registered
  without declaring its role floor. A surface added later cannot silently
  ship unprotected — see [authentication and RBAC](../../security/auth-rbac/).
- **The egress canary that must fail.** Every topology and crucible run
  starts with an instance in the air-gapped segment attempting outbound
  traffic; the scenario fails unless that attempt fails. "No route out" is
  proven by construction on each run, never assumed — the claim the
  [threat model](../../security/threat-model/) leans on.
- **The negative that proves the test.** One test asserts that no outbound
  path bypasses the shared transport (proxy, private CAs); its counterpart
  removes the proxy and requires every path to fail — proving the first
  test actually observes something.
- **Browser tests without Node.** The UI suite drives a real Chrome
  through [chromedp](https://github.com/chromedp/chromedp) — pure Go, no
  Node toolchain, consistent with the product's zero-Node stance. Chrome is
  taken from the environment and never downloaded (NFR-019). The scope is
  deliberately narrow: each scenario names the bug or requirement it locks.

## Position on coverage

The gate is a **per-package floor, not a global average**: every package
under `internal/` must hold at least 70 % statement coverage
([`tools/check-coverage.sh`](https://github.com/tobby-fetch/tobby-fetch/blob/main/tools/check-coverage.sh),
NFR-016), a package without tests counts as 0 %, and only `cmd/` (wiring)
is exempt. A global average lets a well-tested package subsidize an
untested one; a floor does not. The measured state at the milestone-4
audit: 86.8 % mean, 76.5 % minimum. Coverage is treated as a tripwire, not
a target — the pyramid's upper levels are what state the guarantees.

## Replay the crucible yourself

The crucible is a disposable, versioned replica of the deployment reality —
a connected zone, an air-gapped zone, a removable medium that is a real
block device — managed by [Incus](https://linuxcontainers.org/incus/)
(ADR-0014). CI gates merges; the crucible gates milestone acceptance.
Everything needed to replay it is in the repository, under
[`crucible/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/crucible):

```
crucible/
├── setup.sh          # one-time: project, networks, profiles, volumes
├── teardown.sh       # destroys the whole crucible project
├── run.sh            # scenario runner: ./run.sh m1 [m2 …] | all
├── lib.sh            # shared helpers (checks, raw report)
└── scenarios/
    ├── m1/run.sh … m4/run.sh   # one scenario per delivered milestone
```

Scenarios are tagged by milestone and independently invocable — the suite
of all delivered milestones stays replayable at any time
(`./crucible/run.sh all`). The air gap is a managed bridge with no uplink
plus a network ACL rejecting everything but intra-zone traffic; the medium
is a loop-backed block device attached, filled, detached and re-attached
across the gap. Hardware prerequisites are in the
[crucible README](https://github.com/tobby-fetch/tobby-fetch/blob/main/crucible/README.md)
and summarized on the [contribute](../contribute/) page.

Each run writes a raw check report: one `ok`/`FAIL` line per named check,
timestamped, with the digests it observed. Milestone acceptance publishes
that report unedited — read them on
[acceptance reports](../acceptance-reports/).
