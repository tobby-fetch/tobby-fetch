---
title: Release criteria check
description: A script that checks every v1.0.0 release criterion against real evidence and prints a checklist, with the proof or the reason it cannot.
sidebar:
  order: 7
---

The v1.0.0 release is gated by eleven criteria. Nobody should have to take
a box's tick on trust, so a script checks each one from evidence that
exists — the raw acceptance reports, the CI and release-pipeline verdicts,
the published artifacts, the tagged tree:

```bash
mise run release-criteria -- --version v1.0.0-rc.1
# or: go run ./tools/release-criteria --version v1.0.0-rc.1
```

Run it from a clone that has the tag (`git fetch --tags`), with
[`gh`](https://cli.github.com/) logged in. Each criterion prints `OK`, `KO`
or `NON VÉRIFIABLE ICI` followed by the proof (a report name, a CI job
URL, the verifier's verdict) or the reason. The exit code is non-zero when
a criterion that can be verified is `KO`.

## What each criterion checks

| Criterion | Evidence |
|---|---|
| Both modes validated end to end and accepted | **Not verifiable by a script**: the proof is the signed acceptance record of the release candidate. |
| Crucible suite green, crown included | For milestones 1 to 7, the raw reports under `docs/acceptance/` *at the tag*: each ends with its `scenario … complete` line and has no `FAIL` line. Milestones 6 and 7 must have a report for every scenario script, and m7 the crown scenario (`z-crown-rke2`) with the footer stating the acceptance node was destroyed. |
| OCI conformance | The `conformance` CI jobs (linux/amd64 and linux/arm64) of the run on the tagged commit. |
| SLSA L3 and cosign | The commands of [Verify a release](../verify-a-release/), run on the published assets with an empty Docker configuration (anonymous registry access): `slsa-verifier` on every binary, package and image, `cosign verify-blob` on the SBOMs, `cosign verify` on the image, chart and FileSet. |
| No critical or high CVE | The `Image CVE gate (Trivy)` job of the release pipeline's run for the tag. |
| Coverage of 70 % on the core packages | The coverage-floor CI job. |
| e2e on both architectures and Windows | The hermetic-topology jobs on linux/amd64 and linux/arm64, and the Windows test run. |
| Documentation in English and French | Every English page has a French page at the tag, and the website builds. |
| i18n | The `i18n completeness` CI job. |
| Four authentication methods | The named tests of OIDC, SAML, static tokens and basic auth exist at the tag, and the CI test job is green. |
| Format specification `v1` | A `v1` tag in `tobby-fetch/recipe-spec`. On a release candidate this is expected to be `KO` (**expected at 1.0**) and does not fail the run. |

## Honest about what it cannot see

A missing tool is never an `OK`. Without `slsa-verifier`, `cosign`, `crane`
or `gh`, the affected lines read `NON VÉRIFIABLE ICI` and carry the exact
command to run elsewhere. The website build runs only when the working
tree is at the tagged commit; otherwise it says so. `--quick` verifies the
linux/amd64 binary alone, for a fast look.

The reports are read from the tagged tree, not from the working tree: the
script vouches for what the release actually contains. See also
[Acceptance reports](../acceptance-reports/).
