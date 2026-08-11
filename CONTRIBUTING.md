# Contributing to Tobby

Thanks for your interest in Tobby. This document covers the workflow,
conventions, and legal requirements for contributing code or documentation to
this repository.

Tobby is developed in the open from the first commit
([DEVELOPMENT-PLAN.md](docs/), [ADRs](docs/adr/)). Design discussion happens
through GitHub issues and pull requests, same as code.

## Workflow

The project is **trunk-based**:

- `main` is always releasable and is protected: no direct pushes, every
  change lands through a pull request.
- Feature branches are short-lived (days, not weeks) and branch off `main`.
- A pull request needs **at least one approving review** and a **green CI
  run** before it can merge. Prefer several small PRs over one large one.

```sh
git checkout -b feat/short-description main
# ... work, committing with sign-off (see DCO below) ...
git push -u origin feat/short-description
# open a pull request against main
```

## Commit messages: Conventional Commits

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org),
which drives the automated changelog. Common types used in this repository:

| Type | Use for |
|---|---|
| `feat` | A new user-visible capability |
| `fix` | A bug fix |
| `docs` | Documentation only (README, ADRs, SRS, `docs/`) |
| `test` | Adding or fixing tests, no production code change |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `perf` | Performance improvement |
| `build` | Build system, dependencies, packaging |
| `ci` | CI/CD workflow changes |
| `chore` | Everything else (repo maintenance, tooling) |

Example: `feat: add per-recipe pre-flight byte estimate`.

A breaking change is flagged with a `!` after the type (`feat!: ...`) or a
`BREAKING CHANGE:` footer, and is reserved for the SemVer major bumps this
project intends to avoid before 1.0.0 stabilizes its APIs — read twice before
using it.

## Developer Certificate of Origin (DCO)

Every commit must carry a **sign-off**: a `Signed-off-by` trailer certifying
that you wrote the change, or otherwise have the right to submit it under the
project's license, per the
[Developer Certificate of Origin](https://developercertificate.org).

Sign off with `-s` (git adds the trailer using your configured name and
email):

```sh
git commit -s -m "feat: add per-recipe pre-flight byte estimate"
```

If you forgot on the last commit: `git commit --amend -s --no-edit`. For a
whole branch: `git rebase --signoff main`.

Tobby does not use a CLA — the DCO is lighter-weight and does not require
signing over any rights; you keep copyright on your own contributions
(see [ADR-0003](docs/adr/ADR-0003-repo-and-licensing-split.md)). CI checks
every commit in a pull request for a valid sign-off (`.github/workflows/dco.yml`);
a PR with an unsigned commit is blocked until it is fixed.

## Setting up a development environment

Tooling is pinned and installed with [mise](https://mise.jdx.dev):

```sh
mise install        # installs the pinned Go, golangci-lint, gitleaks, ...
mise run setup       # one-time: activates the repo's git hooks (secret
                      # scanning on every commit — see .githooks/pre-commit)
```

Day-to-day commands (the same ones CI runs):

```sh
mise run build       # go build ./cmd/tobby
mise run test         # go test -race -count=2 ./... (unit + integration)
mise run lint         # golangci-lint run, strict profile, zero suppressions
mise run coverage     # per-package coverage floor (tools/check-coverage.sh)
```

Run `mise run lint` and `mise run test` before opening a pull request — CI
enforces both, plus a Trivy scan and a dependency-license check, as blocking
merge gates.

## Code style

- Format with `gofmt` (`gofmt -l .` must report nothing).
- Lint with `golangci-lint` against the repository's strict profile
  (`.golangci.yml`): no default exclusions, and every `//nolint` must name
  its linter and carry a written justification.
- Keep exported identifiers documented; prefer small, focused packages under
  `internal/` unless something is genuinely meant for reuse outside this
  module (in which case, check first whether it belongs in
  [`recipe-spec`](https://github.com/tobby-fetch/recipe-spec) instead —
  see ADR-0003).

## SPDX headers

Every new source file carries an SPDX license header and copyright line at
the top:

```go
// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors
```

Use the year of the file's creation. Don't edit the header of a file you are
merely modifying.

## License of contributions

This repository is licensed under the [GNU General Public License v3.0](LICENSE)
(SPDX: `GPL-3.0-only`). By submitting a pull request, you agree that your
contribution is licensed under the same terms.

## Reporting bugs and requesting features

Use the issue templates (`.github/ISSUE_TEMPLATE/`). For anything that looks
like a security vulnerability, do **not** open a public issue — see
[SECURITY.md](SECURITY.md).
