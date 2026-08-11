# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
starting with `v0.1.0`.

## [Unreleased]

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

[Unreleased]: https://github.com/tobby-fetch/tobby-fetch/compare/main...HEAD
