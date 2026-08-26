---
title: API reference
description: The /api/v1 principles, the OpenAPI contract served by the instance, authentication for API calls, strict UI parity and the reserved path prefixes.
sidebar:
  order: 3
---

A Tobby instance serves one versioned REST API under `/api/v1`, on the same
single listener as the UI, the embedded registry and the probes. This page
states the principles and the surfaces; the normative endpoint-by-endpoint
contract is the OpenAPI document the instance itself serves.

## Principles

- **Versioned path.** Everything lives under `/api/v1`. The path version is
  the API major; the binary's build version is on `/about` and in the
  `tobby_build_info` metric.
- **Machine payloads.** Responses carry raw values and RFC 3339 timestamps
  exclusively. Localization is a UI concern: the API never returns a
  translated sentence where a stable value belongs.
- **One error taxonomy.** Every error is an entry of the
  [TBY-\* taxonomy](../../reference/errors/) rendered as an RFC 9457 problem
  document. The extension members `code`, `probable_cause`, `action` and
  `correlation_id` come from the same catalog the UI and the CLI render;
  the `type` member is the in-instance troubleshooting anchor
  `/help#<code>`, resolvable offline. Problem-document language follows the
  `Accept-Language` header.
- **Unknown API paths answer a problem document**, never HTML.

## The OpenAPI contract, served by the instance

The OpenAPI 3.1 document is embedded in the binary and served by the
instance itself:

```
GET /api/v1/openapi.yaml            # the raw document (role: viewer)
GET /api/v1/cli-output.schema.json  # the CLI's --output json schemas (role: viewer)
GET /api-docs                       # the built-in viewer page (any signed-in role)
```

The second document is the other half of the machine contract: the JSON
Schema of what `tobby <command> --output json` writes (SRS FR-066,
amendment R-08), one entry per reporting command. It is published beside
the OpenAPI one, and served by the same instance, because an automation
that drives Tobby uses both — see the
[CLI reference](../../reference/cli/#report-format).

A build-time test cross-checks the document against the registered routes:
an endpoint cannot ship undocumented, and the document cannot describe an
endpoint that does not exist. What your instance serves is therefore the
authoritative contract for the exact version you run — including offline.

## Authentication

Nothing on the API is anonymous (only the probes are). Two credential
schemes, against the same accounts and tokens as every other surface:

- **Basic** — `account:password`. A token secret is also accepted as the
  Basic password, so `docker login` and `helm registry login` work with
  tokens.
- **Bearer** — a static API token (`Authorization: Bearer <secret>`).
  Tokens are role-scoped, revocable, and stored hashed. They are managed on
  `/api/v1/tokens` and the matching UI screen.

A valid UI session cookie also works for **read** endpoints — this is what
makes "copy the URL you are looking at" work from a browser. Mutating calls
always require Basic or Bearer. Every endpoint sits behind a documented
minimum role (viewer, operator or admin); the published matrix and the
anti-drift test that enforces it are covered in
[Authentication, accounts and RBAC](../../security/auth-rbac/). An origin that
keeps failing authentication is throttled with `429`
([`TBY-AUTH-012`](../../reference/errors/#tby-auth-012)).

## UI ↔ API parity is a feature

The API is the strict mirror of the web UI (SRS FR-061): every UI action
has its mirror endpoint, and **filters and search share the exact same
parameters**. The content screen's search box, kind filter and path prefix
are literally `q`, `kind` and `prefix` on `GET /api/v1/content` — copying
the URL of the screen you are looking at *is* the API call, minus the
rendering. The same taxonomy errors come back, as problem documents instead
of HTML partials.

The surface today (from the served OpenAPI document):

| Area | Endpoints |
|---|---|
| Contract | `GET /api/v1/openapi.yaml`, `GET /api/v1/cli-output.schema.json` |
| Content | `GET /api/v1/content` (search, filters, pagination), `GET /api/v1/content/{repo}`, `GET /api/v1/content/{repo}/-/tags/{tag}` |
| Unit import | `POST /api/v1/import/inspect`, `POST /api/v1/import` |
| Tasks | `GET /api/v1/tasks`, `GET /api/v1/tasks/{id}`, `GET /api/v1/tasks/{id}/logs` |
| Recipes and sync | `GET /api/v1/recipes`, `GET /api/v1/recipes/{recipe}/mapping`, `POST /api/v1/recipes/publish`, `POST /api/v1/sync`, `GET /api/v1/retriever`, `PUT /api/v1/retriever/interval` |
| Accounts and tokens | `PUT /api/v1/account/password`, `GET/POST /api/v1/accounts`, `PATCH/DELETE /api/v1/accounts/{name}`, `GET/POST /api/v1/tokens`, `POST /api/v1/tokens/{name}/revoke` |
| Network | `GET /api/v1/network`, `PUT /api/v1/network/certificate` |

The `/-/` segment is the deterministic separator between a repository path
— which may itself contain slashes — and its sub-resource (`tags`,
`delete`). It appears in UI URLs and API paths alike.

## Reserved path prefixes

The web UI owns the root of the listener; every machine surface keeps a
reserved prefix. The list is part of the product contract (shown on
`/about`, enforced by a collision test):

| Prefix | Surface |
|---|---|
| `/v2/` | The embedded OCI registry — standard Distribution API for `docker`, `helm`, `oras`, with the same accounts and tokens. |
| `/api/` | The REST API described above. |
| `/metrics` | OpenMetrics endpoint — see [Metrics and logs](../../reference/metrics-logs/). |
| `/healthz`, `/readyz` | Liveness and readiness probes, the only anonymous surfaces. |
| `/files/` | FileSet HTTP serving (OS package repositories) — Basic auth so `apt`/`dnf` URL credentials work; per-FileSet anonymous opt-in. |
| `/auth/` | Reserved ahead of the milestone-6 OIDC/SAML callbacks. |
| `/static/` | Embedded UI assets. |

Anything outside these prefixes is a UI route and may evolve freely; the
prefixes themselves are stable.
