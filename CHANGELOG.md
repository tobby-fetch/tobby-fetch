# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
starting with `v0.1.0`.

## [Unreleased]

## [0.2.0] - 2026-08-12

Milestone 2 — user-experience preview: the first complete journey (sign in,
import, track, browse, pull) behind authentication that is on by default.

### Added

- Error taxonomy (`TBY-<domain>-<nnn>`): every user-visible error carries a
  short stable code and a structured bilingual message — what / probable
  cause / corrective action — rendered identically by the web UI, the CLI,
  and the REST API (RFC 9457 problem documents). CLI exit codes follow the
  taxonomy classes (0 success, 1 failure, 2 usage, 3 policy refusal,
  4 verification failure).
- Local authentication, secure by default: argon2id accounts created
  through `tobby user add|passwd|list` (the tool computes the hash), an
  instance refuses to start without an account, session-based sign-in with
  CSRF protection for the web UI, Basic/Bearer for the API and the embedded
  registry (`docker login` works with accounts and static API tokens),
  viewer/operator/admin role gating, and audit events for sign-ins and the
  token lifecycle. Disabling authentication is an explicit opt-in with a
  permanent banner and an audit record.
- Server-rendered web UI (Go templates + a vendored htmx and idiomorph,
  no Node toolchain): bilingual EN/FR from the first screen, dark and
  light themes on CSS design tokens with a WCAG-contrast regression test,
  embedded assets served with ETag revalidation, an operator theme
  override without rebuild.
- Content browsing: repositories grouped by canonical source host with
  search, kind filters and server-side pagination; repository and manifest
  detail down to per-platform presence (sparse indexes shown as such);
  copyable pull commands; identical parameters on the `/api/v1/content`
  mirror endpoints.
- On-demand unit import: bounded remote inspection with per-digest status
  (new / outdated / up-to-date), platform selection, direct-to-storage
  streaming transfer verified against pinned digests at commit, original
  index preserved bit-exactly (sparse when partially selected), Helm chart
  dependency verification (a chart missing an embedded dependency is
  refused, naming it), and per-host insecure-registry opt-in.
- Persistent task queue inside the store: per-item status, task-scoped log
  streams with correlation fields, resumption after interruption (a task
  caught mid-run restarts, never orphaned), live-updating screens through
  self-terminating polling, and full `/api/v1` mirrors including raw log
  download.
- Administration screen for accounts and API tokens (secrets shown exactly
  once), an embedded troubleshooting stub generated from the taxonomy
  (`/help#<code>` anchors), an about page, and a self-served OpenAPI 3.1
  document with a dependency-free HTML viewer.
- Milestone-2 scenarios in both test tiers: the hermetic CI topology and
  the crucible, covering the no-account refusal, anonymous rejection,
  API-driven import, bit-exact digests, authenticated standard-client
  pulls, idempotence, and task resumption across a hard kill.
## [0.1.0] - 2026-08-11

### Added

- Repository governance: license, `CONTRIBUTING.md`, `SECURITY.md`, DCO
  enforcement in CI, issue and pull request templates.
- Application skeleton: configuration precedence (flags > environment >
  YAML file), structured JSON logging, `/healthz` and `/readyz` probes,
  an OpenMetrics endpoint, and graceful shutdown.
- Audit journal foundation and per-run identifier (run ID) propagated
  through logs and the audit trail.
- Embedded OCI registry (CNCF `distribution/distribution` v3 as a library)
  serving read/write on a filesystem backend, with the on-disk storage
  layout following the ingredient-relocation convention (nominal,
  canonicalized source host as repository prefix).
- Quality gates enforced as blocking CI checks from the first commit: unit
  tests with the race detector and anti-flaky double run, per-package
  coverage floors, strict lint with zero suppressions, dependency-license
  compliance, and a Trivy vulnerability scan.
- Release chain groundwork for SLSA Build L3 provenance and signed
  artifacts.

[Unreleased]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/tobby-fetch/tobby-fetch/releases/tag/v0.1.0
