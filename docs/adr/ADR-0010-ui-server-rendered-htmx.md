# ADR-0010 — Web UI: Go server-rendered + htmx, no Node toolchain, i18n, themable CSS

## Status

Accepted — 2026-07-11

## Context

Tobby needs a real web UI: operators configure registries and Retriever settings
(the operating mode itself is selected by startup configuration and displayed in
the UI — see SRS FR-001/FR-062), browse Recipes and Ingredient statuses, trigger
synchronizations, review scan and signature results. Two forces shape the technology choice, and they pull in the
same direction once stated plainly:

1. **Tobby is a supply-chain security tool destined for hardened environments.**
   Its own build must be defensible to the same security reviewers who will use
   it to gate artifacts. A modern JavaScript SPA drags in a Node/npm toolchain
   and a `node_modules` tree of hundreds to thousands of transitive packages —
   each one an unauditable supply-chain liability of exactly the species Tobby
   exists to control. Shipping that in this product would be self-refuting.
   A **single Go toolchain** also keeps the SLSA L3 reproducible-build story
   (ADR-0011) simple: one compiler, one module proxy, one lockfile (`go.sum`),
   no second package ecosystem to pin, vendor, and attest.
2. **Aesthetics are a first-rank objective, not a nice-to-have.** Adoption of
   internal tooling lives and dies on whether operators *want* to use it. "Server
   rendered" must not mean "1998". The UI gets a deliberate design investment.

Two hard lessons and requirements also apply:

- The proof-of-concept served UI assets from a relative filesystem path; the UI
  was **broken inside the container image**. Assets must be compiled into the
  binary.
- A **configurable stylesheet** is an explicit requirement: deployments must be
  able to re-theme the UI (colors, logo) without rebuilding.
- The product is **i18n from day one** (English and French), with externalized
  labels — hardcoded strings are a defect.

## Decision

### Stack

- **Server-rendered HTML in Go** — `html/template` from the standard library, or
  [`templ`](https://templ.guide) if the milestone-0.3 UI work shows its
  compile-time-checked components pay for themselves. Both are pure-Go; the
  choice between them is an implementation detail that does not alter this ADR.
- **htmx** for interactivity: partial page updates, polling of transfer statuses,
  form submissions without full reloads. htmx is a single dependency-free
  JavaScript file, vendored into the repository at a pinned version with its
  integrity recorded — it is reviewed and shipped like any other static asset,
  not pulled from a CDN or a package manager.
- **Zero Node/npm toolchain.** No `package.json`, no bundler, no transpiler
  anywhere in the build. `go build` produces the complete application, UI
  included. Any contribution introducing a JavaScript build step is rejected by
  policy.

### Assets compiled in

All static assets (CSS, htmx, icons, fonts, logo) are embedded with `go:embed`
and served from the binary. This directly encodes the POC lesson: the container
image cannot ship a broken UI because there is no external asset directory to
forget.

Third-party assets embedded this way carry their own licenses: htmx is
BSD Zero-Clause; fonts and icon sets are chosen at implementation time with
GPL-3.0-compatible licenses only, and every embedded asset's license and
attribution ships in a `THIRD-PARTY-NOTICES` file embedded and served
alongside them.

```go
//go:embed static templates
var uiFS embed.FS // the UI travels inside the binary — nothing to mount, nothing to lose
```

### Design system, theming, dark mode

- A small **dedicated design system**: documented components (tables, status
  badges, forms, progress indicators, empty states) with consistent spacing and
  typography scales. Visual quality is reviewed as part of the definition of
  done for UI-facing milestones.
- All colors, radii, and fonts are expressed as **CSS design tokens** (custom
  properties). The default theme includes **dark mode** via
  `prefers-color-scheme` plus a manual toggle.
- **Operator theming without rebuild**: Tobby serves an optional override
  stylesheet from a configured path, layered after the embedded one — satisfying
  the configurable-CSS requirement while keeping the default assets immutable
  inside the binary.

  ```yaml
  # tobby config excerpt — rebrand without rebuilding
  ui:
    themeOverride: /etc/tobby/theme/custom.css   # optional; served after built-in tokens
  ```

  ```css
  /* custom.css — a theme override only touches tokens */
  :root {
    --tobby-color-accent: #0a5c36;
    --tobby-logo-url: url("/ui/theme/logo.svg");
  }
  ```

### Internationalization

English and French ship in v1. All user-facing strings are externalized into
message catalogs handled by a Go i18n library (`go-i18n`); the active language
follows `Accept-Language` with a per-user override. Adding a language is a
translation task, not a code change.

## Consequences

### Positive

- The entire product — engine, API, registry, UI — builds with one toolchain and
  one dependency graph, all attested by the same SLSA provenance. Security review
  of the UI is a review of Go modules plus a handful of pinned, vendored static
  files.
- No JavaScript dependency tree to patch: the recurring npm-advisory churn that
  dominates SPA maintenance simply does not exist here.
- `go:embed` makes the binary genuinely self-contained; UI works identically on a
  laptop, in a container, and on an air-gapped mirror workstation.
- htmx keeps state on the server, which is where Tobby's state already lives
  (task queue, statuses) — no client/server state synchronization layer.
- Theming via token overrides gives deployments visual ownership cheaply, and
  dark mode falls out of the token architecture rather than being bolted on.

### Negative

- Some interactions are harder without a client framework: rich client-side
  validation, drag-and-drop, or offline UI state need deliberate design or
  graceful omission. Accepted: Tobby's UI is forms, tables, and live statuses —
  htmx's home turf.
- htmx polling for status updates is chattier than a WebSocket push; acceptable
  at Tobby's scale, and SSE remains available within the same stack if needed.
- Developers accustomed to component SPAs face a paradigm adjustment; the design
  system documentation exists partly to keep server-rendered contributions
  consistent.

### Neutral

- If `templ` is adopted it introduces a code-generation step, but one written in
  Go, versioned in `go.mod`, and fully compatible with reproducible builds.

## Alternatives considered

### Embedded SPA (React or Svelte, compiled and embedded into the binary)

The mainstream choice, and Svelte was genuinely attractive for its output size.
**Rejected on supply-chain grounds**, which in this product are architectural,
not aesthetic:

- a `node_modules` tree is indefensible in front of the security reviewers of
  hardened environments — hundreds of transitive maintainers, install scripts,
  and advisories, for a tool whose pitch is controlling exactly that class of
  risk;
- a second toolchain (Node, bundler, lockfile) breaks the single-toolchain
  reproducibility story and roughly doubles the CI attestation surface for SLSA
  L3 (ADR-0011);
- the steady stream of JS-ecosystem CVEs would dominate Tobby's own scan reports
  — awkward for a product that gates artifacts on scan results (ADR-0008).

The interactivity a Recipe-transfer dashboard needs does not justify that cost.

### Terminal UI only (TUI), no web UI

Cheapest to build and trivially air-gap friendly. Rejected because a web
interface for administrators is an explicit product requirement, and because the
adoption goal ("operators want to use it") is served by a polished browser UI
accessible without shell access to the host. A CLI remains part of the product
for automation; it complements rather than replaces the web UI.
