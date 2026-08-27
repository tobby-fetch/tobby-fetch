<h1 align="center">Tobby</h1>

<p align="center">
  <em>Go fetch your OCI assets — across every zone, all the way to air-gap.</em>
</p>

---

**Tobby** fetches OCI assets — container images, Helm charts, AI models, and
files — and carries them across isolated network zones, from connected,
through restricted, down to fully air-gapped environments. Transfers are
driven by a declarative **Recipe** format, and Tobby ships with a portable OCI
registry so it can seed or stand in for a zone registry when needed.

Two operating modes cover the whole path:

- **Passthrough** — a long-lived containerized service between two connected
  zones: it periodically resolves the zone's desired state (its *Retriever*),
  verifies signatures and policy, and pushes only what is missing.
- **Mirror** — a single binary on a workstation: it synchronizes the selected
  recipes onto a self-contained, transportable store (removable media), which
  is physically carried across the air gap and pushed into the destination
  zone registry by the same application — after full re-verification.

Everything that crosses a boundary is **signed, digest-pinned and
allow-listed**, and re-verified on the far side against that side's own
trust roots — vulnerability scanning joins the list at milestone 6. Tobby's
own releases hold themselves to the same bar:
SLSA Build L3 provenance, reproducible builds, signed SBOMs, zero-known-CVE
images, and an OpenVEX statement for anything a scanner flags that does not
apply.

## Design & documentation

The design is public before the first line of product code — review it, file
issues, disagree with an ADR:

| Document | What it is |
|---|---|
| [Software Requirements Specification](docs/SRS.md) | Every functional and non-functional requirement, numbered and testable |
| [Architecture Decision Records](docs/adr/) | The 16 structuring decisions — context, decision, consequences, alternatives |
| [Roadmap (French)](https://tobby-fetch.github.io/tobby-fetch/roadmap.html) | Milestones and features in three readings: technical, plain-language, business value — rendered on the project site ([source](website/public/roadmap.html)) |
| [Recipe format specification](https://github.com/tobby-fetch/recipe-spec) | The `Recipe`/`Retriever` format, JSON Schemas, examples, and the Go parsing/validation SDK — separate repository, Apache-2.0, tagged `v1alpha1` (draft) |
| [Example recipes](examples/) | Harbor, Keycloak, MetalLB, OpenTelemetry Collector, VictoriaMetrics operator — and the four ways a container image escapes `helm template \| grep image:` before it strands a sealed zone |

Quality is a day-one gate, not a final phase: a five-level test pyramid
(unit → integration → e2e → extended e2e → **crucible**, a disposable replica
of the target environment, air gap included) blocks merges from the first
commit, and every milestone ships its own crucible scenarios — the suite of
completed milestones stays replayable, in whole or per milestone, at any time
([ADR-0014](docs/adr/ADR-0014-crucible-test-infrastructure-incus.md)).

> 🐕 **Released and under active development.** The current release line is
> **v0.5.x**, with the first five milestones delivered:
>
> 1. **Foundations** — the application skeleton (layered configuration,
>    structured JSON logging with run correlation, security audit log,
>    health probes, OpenMetrics, graceful shutdown) and the embedded OCI
>    registry with the relocation layout.
> 2. **Web UI** — a bilingual (English/French) server-rendered interface:
>    local accounts and sessions, content browsing, unit imports with
>    platform selection, live task tracking.
> 3. **Recipe engine** — signed-recipe synchronization: cosign
>    verification, digest-pinned ingredients, FileSets, cascade between
>    zones through source substitution.
> 4. **Passthrough** — the long-lived promotion service between connected
>    zones: allow-list policy, authenticated forward proxy, private PKI
>    trust, and the reference Helm chart.
> 5. **Mirror & air-gap** — the physical transfer end to end: a
>    synchronization onto a transportable store with pre-flight checks and a
>    plan mode, a media manifest and its re-verification on the far side, a
>    guided Media screen, per-recipe blocking, a destination that serves
>    nothing until the medium it holds has been verified, OCI image layout
>    export/import, and documentation embedded in the binary for the zone
>    that has no route to a website.
>
> The design documents above remain the source of truth for what comes
> next — see the [roadmap](https://tobby-fetch.github.io/tobby-fetch/roadmap.html).

## Installing

Every release ships static binaries for linux, windows, and darwin
(amd64/arm64) with SLSA Build L3 provenance, plus `.deb`/`.rpm`/`.apk`
packages and a signed container image:

- **GitHub releases** — binaries, Linux packages, SBOMs, provenance:
  [github.com/tobby-fetch/tobby-fetch/releases](https://github.com/tobby-fetch/tobby-fetch/releases)
- **Homebrew** (macOS): `brew install tobby-fetch/tap/tobby`
- **Container image**: `ghcr.io/tobby-fetch/tobby-fetch`

### Quick start

`tobby quickstart` walks through the first start interactively — store and
state directories, operating mode, first admin account — writes the
configuration file, and can hand over to `serve` directly:

```sh
tobby quickstart
```

The same setup, non-interactive (the password is one line on stdin):

```sh
echo 'choose-a-password' | tobby quickstart --mode mirror --password-stdin
tobby serve --config ./tobby.yaml
```

The instance serves the web UI and the API on `http://localhost:8080`, and
refuses anonymous access by default — sign in with the account quickstart
created.

### Building and running from source

```sh
mise install          # toolchain (Go, golangci-lint, hooks)
mise run build        # → bin/tobby
bin/tobby quickstart
```

`mise run test`, `mise run lint`, `mise run coverage`, and `mise run vuln`
run the same gates CI enforces.

Landing page: **https://tobby-fetch.github.io/tobby-fetch/**

## License

The application is licensed under the [GNU General Public License v3.0](LICENSE)
(SPDX: `GPL-3.0-only`). Copyright © 2026 infraBuilder SASU and contributors.
The Recipe format specification and its Go SDK are published separately under
the Apache-2.0 license.
