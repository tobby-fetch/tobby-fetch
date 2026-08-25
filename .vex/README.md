# OpenVEX statements for Tobby's own releases

ADR-0011 commits this project to one rule about scanner findings: **a
finding that does not apply to Tobby is answered with a published,
signed OpenVEX statement — never a silent ignore rule.** This directory
is where that rule becomes mechanical.

## Current state

There are **no statements**: no scanner finding is currently waived.
The vulnerability gates (`ci.yml` `vulnerabilities`, the release image
scan, the weekly rebuild) block on any CRITICAL or HIGH finding with no
ignore mechanism of any kind — no `.trivyignore`, no severity filter
tricks, no VEX. The absence of `tobby.openvex.json` from a release's
assets therefore means exactly what it looks like: nothing was waived
for that release.

## When the first non-applicable finding appears

1. Author the statement in `.vex/tobby.openvex.json` (create the file on
   first use) with [`vexctl`](https://github.com/openvex/vexctl):

   ```sh
   vexctl create --product="pkg:golang/github.com/tobby-fetch/tobby-fetch@vX.Y.Z" \
     --vuln="CVE-XXXX-NNNNN" \
     --status="not_affected" \
     --justification="vulnerable_code_not_in_execute_path"
   ```

   One statement per finding; the `justification` field is mandatory in
   spirit even where the spec makes it optional — a statement nobody can
   audit is an ignore rule with better clothes. The document is reviewed
   like code.

2. Wire the document into the three Trivy gates in the same PR, so the
   scanners and the published statement can never disagree: add
   `TRIVY_VEX: .vex/tobby.openvex.json` to the environment of the Trivy
   steps in `.github/workflows/ci.yml` (`vulnerabilities` job),
   `.github/workflows/release.yml` (image scan) and
   `.github/workflows/weekly-rebuild.yml` (rebuild gate).

3. Publication needs no wiring: the release workflow already signs and
   attaches `.vex/tobby.openvex.json` (cosign keyless, same `.bundle`
   format as the SBOMs) whenever the file exists.

4. Re-examine every statement at each release; a statement whose
   `product` no longer matches the released version is dead weight and
   must be refreshed or removed.

Time-bounded, config-scoped waivers for content that Tobby *scans* (as
opposed to Tobby's own releases) are a different feature — R-15,
milestone 7 — and do not live here.
