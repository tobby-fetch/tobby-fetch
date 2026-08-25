---
title: Licenses, governance, sustainability
description: GPL-3.0 for the application, Apache-2.0 for the format — why, what it means for your legal team, how vulnerabilities are handled, and why the project stays auditable without its vendor.
sidebar:
  order: 4
  badge:
    text: Partial
    variant: caution
---

:::caution[Partially complete]
The licensing, DCO and security-reporting content below is current. The
vulnerability-handling story deepens with milestone 6, when scanning with
policy ships and gets its own documentation. Track it on the
[project status](../../discover/status/) page.
:::

## Two licenses, one boundary

The project is split across two repositories with two licenses, and the
split is a design decision
([ADR-0003](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0003-repo-and-licensing-split.md)):

| What | License | Where |
|---|---|---|
| The Tobby application (CLI, service, embedded registry, UI, API) | GPL-3.0-only | [tobby-fetch/tobby-fetch](https://github.com/tobby-fetch/tobby-fetch) |
| The Recipe/Retriever format: specification, JSON Schemas, Go SDK, `recipe lint` | Apache-2.0 | [tobby-fetch/recipe-spec](https://github.com/tobby-fetch/recipe-spec) |

The reasoning: the **format** should spread with no friction at all — any
tool, including a proprietary one, can implement it, embed the SDK, and
interoperate. The **application** is protected by copyleft: improvements to
Tobby itself stay open. Copyright is held by infraBuilder SASU and
contributors; there is no CLA — contributions are accepted under the
Developer Certificate of Origin instead (see below).

## The GPL questions your legal team will ask

Short answers first, precedents second. This is not legal advice; it is a
map of the standard analysis.

**Does running Tobby make our software GPL?** No. The GPL's obligations
attach to distributing derivative works of the program, not to running it.
Content that Tobby transports is data the program processes; it is not
linked with Tobby and does not become a derivative work.

**Do our recipes become GPL?** No. Recipes are YAML documents in an
Apache-2.0-specified format. They are inputs to the program, like a
`Makefile` is to Make. The SDK you would parse them with is Apache-2.0.

**Can we script against the CLI and the API?** Yes. Invoking a GPL program
as a separate process, or calling its REST API, is use, not linking. This
is the same posture as the GPL tools most organizations already run in
production: Git, Ansible, Bash. You *execute* them; you do not link them
into your products.

**When would GPL obligations actually apply?** If you modify Tobby's source
and distribute the modified binary to third parties, you must provide the
corresponding source under GPL-3.0. Internal use of a modified build, on
your own infrastructure, triggers no distribution obligation.

**Which GPL version, exactly?** `GPL-3.0-only` (not "or later"), stated in
the [LICENSE](https://github.com/tobby-fetch/tobby-fetch/blob/main/LICENSE)
file. The license terms cannot silently shift under a future FSF revision.

## Contributions and the DCO

Every commit must be signed off (`git commit -s`), asserting the
[Developer Certificate of Origin](https://developercertificate.org/): you
have the right to submit the change under the project license. Enforcement
is automatic in CI on every pull request. The full contribution workflow —
building from source, running the test pyramid, the DCO in practice — lives
in one canonical place: [Contribute](../../project/contribute/).

## Reporting a vulnerability

The project has a published security policy
([SECURITY.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md)).
The essentials:

- **Channel:** private reports through GitHub Security Advisories on the
  repository's Security tab — never a public issue.
- **Response:** acknowledgement within 7 days; coordinated disclosure with
  an agreed date; confirmed vulnerabilities fixed and released as a patch
  version of the current minor line, with an advisory crediting the
  reporter.
- **Supported versions:** pre-1.0, only the latest minor release receives
  security fixes. The supported-versions table will be published when 1.0
  ships.
- **Scope:** the application repository (CLI, service, registry, UI, API).
  The format and SDK have their own policy in the `recipe-spec` repository.

On the outbound side, Tobby's own releases carry an **OpenVEX statement**:
when a scanner flags a dependency CVE that does not apply to Tobby, the VEX
document says so, machine-readably, instead of leaving you to triage the
noise. Release images are rebuilt weekly against the current Wolfi base so
that the zero-known-CVE claim is maintained between releases, and the VEX
statement is regenerated with the quality gates.

## Sustainability: auditable without the vendor

A tool for regulated, isolated environments must answer one uncomfortable
question: *what happens if the vendor disappears?* Tobby's answer does not
rely on trust in the vendor at all:

- **Reproducible builds.** Any party with Git and Go can rebuild any tagged
  release bit-for-bit and compare digests — the strongest verification
  available in an air-gapped zone, with no signature infrastructure
  required. The procedure is in
  [Verify a release](../../project/verify-a-release/).
- **Open specification.** The recipe format is Apache-2.0 with JSON Schemas
  and a Go SDK. Your inventory of what crossed each boundary is readable —
  and re-implementable — without Tobby.
- **Standard storage.** The store is standard OCI content behind a
  conformant registry API; standard tooling (`skopeo`, `oras`, `crane`)
  can extract everything, signatures included.
- **Public design.** The requirements ([SRS](../../reference/srs-adr/)), the
  architecture decisions, and the raw
  [acceptance reports](../../project/acceptance-reports/) are published. An
  audit does not depend on the vendor's cooperation.

GPL-3.0 completes the picture: the code cannot be closed retroactively, and
any successor — commercial or community — inherits the right to continue it.
