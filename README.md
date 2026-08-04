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

Everything that crosses a boundary is **signed, digest-pinned, scanned, and
allow-listed** — and Tobby's own releases hold themselves to the same bar:
SLSA Build L3 provenance, reproducible builds, signed SBOMs, zero-known-CVE
images, and an OpenVEX statement for anything a scanner flags that does not
apply.

## Design & documentation

The design is public before the first line of product code — review it, file
issues, disagree with an ADR:

| Document | What it is |
|---|---|
| [Software Requirements Specification](docs/SRS.md) | Every functional and non-functional requirement, numbered and testable |
| [Architecture Decision Records](docs/adr/) | The 14 structuring decisions — context, decision, consequences, alternatives |
| [Roadmap (French)](docs/ROADMAP.fr.html) | Milestones and features in three readings: technical, plain-language, business value |
| [Recipe format specification](https://github.com/tobby-fetch/recipe-spec) | The `Recipe`/`Retriever` format, JSON Schemas, and Go SDK — separate repository, Apache-2.0 |

Quality is a day-one gate, not a final phase: a five-level test pyramid
(unit → integration → e2e → extended e2e → **crucible**, a disposable replica
of the target environment, air gap included) blocks merges from the first
commit, and every milestone ships its own crucible scenarios — the suite of
completed milestones stays replayable, in whole or per milestone, at any time
([ADR-0014](docs/adr/ADR-0014-crucible-test-infrastructure-incus.md)).

> 🐕 **Under active development.** The design documents above are the current
> source of truth; source code lands milestone by milestone. Watch this
> repository for the first releases.

Landing page: **https://tobby-fetch.github.io/tobby-fetch/**

## License

The application is licensed under the [GNU General Public License v3.0](LICENSE).
The Recipe format specification and its Go SDK are published separately under
the Apache-2.0 license.
