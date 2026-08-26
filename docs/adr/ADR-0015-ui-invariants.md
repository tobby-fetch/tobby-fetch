# ADR-0015 — Web UI invariants: canonical URLs, reserved prefixes, htmx doctrine

## Status

Accepted — 2026-08-12 (UI design pass for milestone 2)

## Context

ADR-0010 fixed the UI stack: Go server-rendered templates plus a single
vendored htmx file, embedded assets, design tokens, EN/FR from day one.
Milestone 2 builds the first real screens on that stack. A UX design pass
(2026-08-12) identified a small set of architectural rules that are cheap on
day one and expensive to retrofit; they are recorded here so every UI
contribution — this milestone and later ones — applies them uniformly.
The full screen inventory lives in the UI specification working document;
this ADR holds only the invariants.

## Decision

### 1. Fragments live on canonical URLs (no shadow API)

There is no `/fragments/*` route namespace. A handler serves the full page
or an htmx partial for the *same URL*, negotiated on the `HX-Request`
header. Search, filters, sort, and pagination use `hx-push-url` so the
address bar always carries exactly the parameters of the equivalent
`/api/v1` endpoint: UI/API parity (FR-061, R-06) is verifiable by copying
the URL, and any UI state survives refresh and is shareable between
operators. Tolerated exception: dashboard tiles loaded asynchronously are
internal fragments and claim no API equivalent.

### 2. Reserved prefixes, locked by a test

The UI owns the root of the single listener. Everything else lives under a
reserved prefix, materialized as one Go slice (`server.ReservedPrefixes`):
`/v2/`, `/api/`, `/metrics`, `/healthz`, `/readyz`, `/auth/`, `/static/`.
`/auth/` is reserved now for the OIDC/SAML callbacks of milestone 6 —
reserving it costs nothing today and prevents a route migration later. A
unit test fails on any UI route colliding with a reserved prefix, and
`/about` documents the list.

### 3. Deterministic sub-resource routing: `/-/`

`tags` is a legal path segment of an OCI repository name, so
`/content/{repo...}/tags/{tag}` cannot be parsed reliably. Sub-resources of
a repository are separated by `/-/` (`/content/{repo...}/-/tags/{tag}`),
GitLab's convention: a lone `-` is invalid in repository names, making the
split deterministic. API mirror routes use the same separator (FR-061).

### 4. Errors persist as code + parameters

Task items and any stored failure persist the taxonomy code and its typed
parameters (package `taxonomy`), never a localized sentence: history must
re-render in the viewer's language (FR-063) long after the fact, and the
API serves the same entry as RFC 9457. The principal code of a multi-item
task is computed by `taxonomy.Principal` — one rule, never re-derived in
templates or API handlers.

### 5. htmx security configuration

Set globally in the layout: `historyCacheSize: 0` (no authenticated DOM
snapshot in localStorage after logout — NFR-015), `selfRequestsOnly: true`,
`allowEval: false` (strict CSP without `unsafe-eval`). Screens handling
secrets (`/admin/accounts`) additionally set `hx-history="false"`.

### 6. Transport failures carry their real status

Fragment errors return their true HTTP status with the taxonomy error block
as body; htmx is configured to swap non-2xx responses into the target. An
expired session on an `HX-Request` returns `401` plus
`HX-Redirect: /login?next=<page>` — never a fragment filled with the login
page. A single `htmx:sendError`/`htmx:timeout` listener renders the
"instance unreachable" taxonomy block into a reserved layout zone. All
three paths are covered by e2e tests.

### 7. i18n mechanics

Every visible string is a complete go-i18n key with plural forms and named
variables; concatenation of translated fragments is forbidden. The five
ingredient kinds are never translated; isolated technical terms carry
`lang="en"`. Switching language performs a full page reload (a half-EN
half-FR DOM with a wrong `html lang` is worse than a reload). Dates,
durations, and binary sizes are formatted by one package
(`internal/ui/format`); the API serves raw bytes and RFC 3339 exclusively —
localization is a template concern, or FR-061 parity becomes untestable.

### 8. Accessibility structure (not deferrable to the 7.1 polish pass)

Native landmarks, one `h1` per page, skip link, `:focus-visible` ring
token. Never `aria-live` on a polled zone: one visually-hidden live region
in the shell, fed by `hx-swap-oob` on state transitions only. Stable DOM
ids (also required by the crucible e2e suites). Focus moves to
`[data-focus-target]` after `htmx:afterSettle` via a ~15-line vanilla
helper.

## Consequences

- UI and API stay in lockstep by construction; parity tests reduce to URL
  comparison.
- The route space is future-proof: milestones 3–6 mount `/recipes`,
  `/media`, `/auth/` callbacks without touching existing screens.
- Polled screens keep focus, scroll, and selection through updates
  (idiomorph morphing), which is what makes server-rendered feel modern.
- The rules are enforceable mechanically (prefix-collision test, i18n
  completeness test, e2e error-path tests) rather than by review vigilance.

## Alternatives considered

**A dedicated fragment namespace** (`/fragments/*`) — rejected: it
duplicates every filtered/paginated route, drifts from the API contract,
and turns parity into a documentation promise instead of a property.

**Basic auth challenge for the UI** — rejected in the design pass (see
UI-SPEC §9): not stylable, not bilingual, no reliable logout; the Basic
scheme remains the API and registry mechanism (FR-076).
