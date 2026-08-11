# Security Policy

Tobby moves trusted software across network zones, up to fully air-gapped
environments. It is held to a high supply-chain bar (signed releases, SLSA
Build L3 provenance, zero known critical/high CVEs at release), and we take
security reports seriously.

## Reporting a vulnerability

**Please do not open a public GitHub issue for a security vulnerability.**

Report privately through GitHub Security Advisories:

1. Go to the [Security tab](https://github.com/tobby-fetch/tobby-fetch/security)
   of this repository.
2. Click **"Report a vulnerability"**.
3. Describe the issue: affected version, reproduction steps, impact, and any
   proof-of-concept you can share.

This opens a private advisory visible only to you and the maintainers, with
its own discussion thread — the right channel for exchanging details,
including a fix, before anything is public.

## Response process

- **Acknowledgement**: within 7 days of the report.
- **Coordinated disclosure**: we work with you on a fix and an advisory,
  and agree on a disclosure date before anything is published. We ask that
  you do not disclose the issue publicly until a fix is released.
- **Fix and release**: confirmed vulnerabilities are fixed and released as a
  **patch version** of the current minor line, accompanied by a GitHub
  Security Advisory that credits the reporter (unless anonymity is
  requested) and, where applicable, a CVE.

## Supported versions

Tobby is pre-1.0.0. Only the **latest minor release** is supported with
security fixes; there is no long-term support branch before 1.0.0. Once
1.0.0 ships, this section will be updated with the supported-versions table
for the stable line.

## Scope

This policy covers the Tobby application in this repository: the CLI, the
service mode, the embedded OCI registry, the web UI, and the API. For the
Recipe/Retriever format and its Go SDK, report to
[`tobby-fetch/recipe-spec`](https://github.com/tobby-fetch/recipe-spec/security)
instead.

Out of scope: vulnerabilities in third-party dependencies that already have a
public advisory and are tracked upstream (please still let us know if Tobby
hasn't picked up the fix yet), and issues that require an attacker to already
control the deployment's configuration or trust roots.

## Acknowledgements

We are grateful to everyone who reports vulnerabilities responsibly. Unless
you ask to stay anonymous, we credit reporters in the published advisory.
