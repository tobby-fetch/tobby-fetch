# ADR-0003 — Repository & licensing split: recipe-spec (Apache-2.0) / tobby (GPL-3.0)

## Status

Accepted — 2026-07-11 · Amended 2026-08-04 (copyright holder and SPDX
conventions)

## Context

Tobby is an open source project with a sponsoring organization funding its
initial development. Two goals are in tension:

1. **Maximize adoption of the Recipe format.** The format only delivers its
   full value if it becomes a lingua franca: CI systems, registries, scanners,
   and in-house tools — including proprietary ones — should be able to parse,
   validate, emit, and sign recipes without legal friction.
2. **Protect the sponsor's investment in the application.** The application
   embodies most of the funded engineering effort. The sponsor's interest is
   that improvements made by third parties who *distribute* modified versions
   flow back to the commons rather than fueling closed derivatives.

A single repository with a single license cannot serve both goals: a
permissive license on everything weakens goal 2; copyleft on everything kills
goal 1, because proprietary tools cannot link a GPL SDK.

## Decision

The project is split into **two repositories** under the
[`tobby-fetch`](https://github.com/tobby-fetch) GitHub organization, with
different licenses:

| Repository | Contents | License |
|---|---|---|
| `tobby-fetch/recipe-spec` | Recipe/Retriever specification, JSON Schemas, Go SDK (parsing, validation, serialization) | **Apache-2.0** |
| `tobby-fetch/tobby-fetch` | The Tobby application (engine, registry, web UI, API) — Go module `github.com/tobby-fetch/tobby-fetch` — plus the project website and the published design documents (`docs/`) | **GPL-3.0** |

Contributions to both repositories are accepted under the **Developer
Certificate of Origin (DCO)** — a `Signed-off-by` line per commit — rather
than a Contributor License Agreement.

### Copyright and SPDX conventions

The initial copyright holder is **infraBuilder SASU**; under the DCO model,
contributors retain the copyright on their own contributions. Licensing is
expressed with SPDX identifiers — the application is `GPL-3.0-only`, the
specification and SDK are `Apache-2.0`. Source files carry an
`SPDX-License-Identifier` header and a
`Copyright © <year> infraBuilder SASU and contributors` line at creation;
third-party assets embedded in the binary are inventoried in
`THIRD-PARTY-NOTICES` (ADR-0010).

### Rationale

- **Apache-2.0 for the spec and SDK** removes every barrier to third-party
  implementation: permissive, patent-granting, and compatible with
  proprietary linking. Any vendor can build recipe support into a commercial
  product, which is precisely what makes the format worth standardizing on.
- **GPL-3.0 for the application** is deliberate copyleft: anyone may use,
  study, and modify Tobby freely, but distributing a modified version
  requires publishing the modified sources. This protects the sponsor's
  investment against closed forks while keeping the software fully open.
  Strong copyleft is not an adoption handicap for an *application* (as
  opposed to a library): Ansible, Git, and Bash are GPL-licensed and among
  the most widely deployed tools in their categories, because organizations
  *run* applications — they do not link against them.
- **DCO over CLA**: a CLA is asymmetric (contributors grant rights the
  project does not reciprocate), requires legal review that deters casual
  contributors, and demands administrative tracking. The DCO is the
  Linux-kernel-proven lightweight alternative and is sufficient because the
  project does not intend to relicense.

## Consequences

- The application's code **cannot be reused by non-GPL projects**. This makes
  the boundary discipline critical: any logic that third parties legitimately
  need — parsing, validation, schema handling, format conversion — must live
  in the Apache-2.0 SDK in `recipe-spec`, not in the application. "Would a
  third-party tool need this?" becomes a standing code-placement question.
- `tobby` depends on `recipe-spec` (license-compatible: Apache-2.0 code may
  be included in a GPL-3.0 work; the reverse would not be true, so no code
  flows from `tobby` into `recipe-spec` without relicensing by its authors).
- The spec can graduate versions (v1alpha1 → v1, ADR-0001) on its own cadence,
  with its own semver, independently of application releases.
- All dependencies of the application must be GPL-3.0-compatible; this is
  checked in CI as part of the supply-chain gates (ADR-0011).
- Community contributions require only a `git commit -s`; no signature
  workflow or CLA bot beyond DCO enforcement.

## Alternatives considered

### Single repository, GPL-3.0 everywhere

Simplest to govern and maximally protective, but it makes the SDK unusable by
proprietary tools: linking a GPL library makes the linking work GPL. The
format would then only ever be implemented by re-reading the prose spec —
guaranteeing divergent implementations or, more likely, no third-party
implementations at all. Rejected because format adoption is a first-class
goal.

### Everything Apache-2.0

Maximally adoption-friendly and the default reflex for cloud-native projects.
Rejected for the application because it permits exactly the scenario the
sponsor funds against: a vendor shipping an improved, closed derivative of
Tobby with no obligation to contribute back. For an end-user application with
a single sponsor rather than a foundation, copyleft is the appropriate
protection.

### MPL-2.0 for the application

File-level copyleft is a genuine middle ground and would allow proprietary
combinations while protecting modified files. Rejected as *weaker than
needed*: an application (not a library) gains nothing from permitting
proprietary linking, so MPL's flexibility only reduces protection without
adding adoption, and its per-file scope invites gradual hollowing-out of the
copyleft core.

### CLA instead of DCO

A CLA would preserve the option of future relicensing or dual-licensing by a
single rights-holder. Rejected: the project has no dual-licensing business
model, the asymmetry discourages contributors, and the administrative cost is
permanent. If relicensing ever becomes necessary, contributor consent can be
sought then — the Linux kernel scale proves DCO governance works.
