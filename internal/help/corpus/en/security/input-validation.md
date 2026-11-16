---
title: Input validation
description: Every input that crosses a trust boundary, with its size bound, its decoder and its grammar check — request bodies, path and query parameters, configuration, documents read from a store or a medium, registry and identity-provider responses (NFR-023).
sidebar:
  order: 9
---

Tobby reads input from parties it does not control: browsers and API
clients, the identity provider, upstream registries, and a medium that
another person carried across the air gap. Each input is handled with
three rules (NFR-023):

1. **Bounded before it is parsed.** A size limit applies before any decoder
   sees the input, so no input can make the instance buffer an unbounded
   amount.
2. **Decoded strictly.** Unknown fields are refused wherever the format
   allows it. A misspelt option is an error, not a silent default.
3. **Checked against its grammar** before it reaches a file-system path, a
   process argument or a log record. The grammars are those of OCI
   references and digests, repository paths, identifiers and timestamps.

A refusal carries a [taxonomy code](../../reference/errors/). It never
carries a stack trace.

This page records the review per entry point. "Strict" means unknown
fields are refused.

## The HTTP listener

| Input | Bound | Decoder | Grammar check | Refusal |
|---|---|---|---|---|
| Request line and headers (all surfaces) | 1 MiB of headers, 10 s to send them | net/http | per surface, below | `431` / connection closed |
| UI form bodies (every route) | 16 MiB per request. URL-encoded forms are also capped at 10 MiB by net/http. The certificate upload holds 2 MiB in memory and 1 MiB per PEM document | form / multipart | per screen | the screen's code |
| API JSON bodies (every route) | 16 MiB per request: a declared `Content-Length` above it is refused unread, and a chunked body is cut at the bound. Each endpoint then applies its own bound (table below) | JSON, strict. A body that continues after its document is refused | per endpoint | `TBY-SRV-004` (413) for size, then the endpoint's code |
| Sign-in username, Basic user name | 256 bytes, valid UTF-8, no control character, checked **before** the password | — | the name is never verified or logged as given; the audit actor reads `invalid-name` | `TBY-AUTH-002` |
| `next` / `back` redirect targets | URL | — | must be a local path (`/`, not `//`, no `\`, no control character); otherwise the target is `/` | — |
| State-changing requests authenticated by the session cookie | — | — | anti-forgery token and origin check (NFR-012) | `TBY-AUTH-004` |

Per-endpoint body bounds:

| Endpoint | Bound |
|---|---|
| `POST /api/v1/accounts`, `PATCH /api/v1/accounts/{name}`, `POST /api/v1/tokens`, `POST /api/v1/account/password` | 8 KiB |
| `POST /api/v1/filesets/pack` | 8 KiB |
| `POST /api/v1/sync`, `PUT /api/v1/retriever/interval`, `POST /api/v1/media/verify`, `POST /api/v1/media/import` | 4 KiB |
| `POST /api/v1/import` | 64 KiB |
| `POST /api/v1/oci-layout/{plan,export,import}`, `POST /api/v1/store/reset` | 64 KiB |
| `POST /api/v1/plan` | 256 KiB |
| `PUT /api/v1/network/certificate` | 2 MiB + 8 KiB (two PEM documents) |
| `POST /api/v1/recipes/publish` | 4 MiB + 64 KiB (the cookbook document bound) |

## Path and query parameters

| Input | Grammar check | Refusal |
|---|---|---|
| Repository path (`/content/{repo...}`, API `/content/{repo}`) | OCI repository name (`reference.WithName`); a tag holds no `/` | `TBY-SRV-002` |
| Content removal | the repository must be a recorded unit import before anything is deleted | `TBY-POL-*` |
| Task identifier (`/tasks/{id}`, logs) | looked up in the in-memory queue before it names a file | `TBY-TSK-001` |
| Task log follow (`?after=`) | integer cursor; at most 1 MiB per call, the cursor carries the rest | `TBY-STO-001` |
| Account and token names (path and body) | looked up in the account store; `:` refused at creation | `TBY-AUTH-008`, `TBY-AUTH-010` |
| Recipe name (`/recipes/{recipe}/mapping`) | looked up in the recorded recipes | `TBY-SRV-002` |
| Help page and screenshot names | looked up in the embedded set; never a file path | `TBY-SRV-002` |
| Transfer slip `format`, `lang` (UI and API) | closed sets | `TBY-VAL-001` |
| Import reference (`reference=`, body) | OCI reference grammar (`name.ParseReference`) | `TBY-REG-001` |
| `/files/<set>/<path>` | NUL refused; no empty, `.` or `..` segment; the set name is looked up; the file is opened through `os.Root` | bare `400` / `404` (download clients read statuses, not documents) |
| `/v2/` names and digests | the distribution router's name and digest grammars | OCI error documents |
| OCI layout import and export paths | none: an administrator names a host path on purpose (FR-051); recorded in the audit log | — |

## Identity providers

| Input | Bound | Decoder | Check | Refusal |
|---|---|---|---|---|
| Every IdP response (discovery, JWKS, token, SAML metadata) | 2 MiB, enforced on the transport beneath every library. A larger body fails; it is never truncated | JSON / XML | — | `TBY-AUTH-014`/`015`/`016` |
| OIDC callback `state`, `code` | URL | query | `state` must match a pending sign-in and its binding cookie | `TBY-AUTH-014` |
| OIDC ID token | token endpoint bound | JWT | signature, algorithm allow-list, issuer, audience, `azp`, nonce | `TBY-AUTH-014` |
| SAML `SAMLResponse`, `RelayState` | 256 KiB, before base64 decoding and again after it | XML (signature-verified) | the relay must match a pending sign-in before any XML is parsed; `Destination` required | `TBY-AUTH-015` |
| Asserted identity (OIDC `sub`, SAML NameID) | 255 bytes, no control character | — | before it becomes a session name or an audit actor | `TBY-AUTH-014`/`015` |
| Display name, groups | 128 characters; 1 024 groups | — | control characters removed | — |

## Configuration and local state

| Input | Bound | Decoder | Check |
|---|---|---|---|
| Instance configuration | file size | YAML, strict | full validation before start (`TBY-CFG-001`) |
| Retriever and Recipe documents (file, URL, OCI) | 4 MiB | YAML through the published JSON Schema, then strict JSON | schema plus semantic rules (`TBY-VAL-001`) |
| Cookbook documents | 4 MiB | same parser | publication location |
| Registry credentials (`dockerconfigjson`) | file size | JSON, lenient: the format belongs to the Docker tool chain | host keys |
| Task records (`_tobby/tasks/*.json`) | 4 MiB per record | JSON | the identifier must equal the file name and hold no separator; otherwise the record is skipped with a warning |
| Accounts file | file size | YAML | schema version |

## Store and medium

A medium is a store that changed hands. Everything it says about itself is
a claim until the destination has hashed it again (FR-054).

| Input | Bound | Decoder | Grammar check | Refusal |
|---|---|---|---|---|
| Media manifest (`meta/media.json`) | 64 MiB | JSON, strict. A manifest that continues after its document is refused | inventory paths confined to the store, repository names, tags, digests | `TBY-MED-002` and following |
| Manifests, tag links, signature blobs read during the walk | 1 MiB (128 bytes for a tag link) | JSON, lenient (OCI allows unknown fields) | digests | `TBY-MED-*` |
| OCI image layout import | 1 MiB for markers and index; capped entry count; gzip refused | JSON, lenient (OCI) | repository and tag grammars, entry classification | `TBY-LAY-*` |
| FileSet layer extraction | per-file, file-count and depth budgets | tar, gzip, zstd | cleaned entry paths, no escape, whiteouts checked, set-uid bits stripped, `os.Root` | `TBY-FIL-*` |
| Store integrity re-check | 1 MiB per manifest | JSON, lenient (OCI) | digests | `TBY-INT-*` |
| Signature bundles | 1 MiB payload, on both the declared and the actual size | JSON | digest binding | `TBY-SIG-*` |
| Scanner output | 128 MiB | JSON, lenient (the scanner's own schema), schema version 2 | — | `TBY-SCN-*` |

## Upstream registries

| Input | Bound | Check |
|---|---|---|
| Manifests and indexes | 100 MiB (go-containerregistry) | digest |
| Blobs | the declared size; resumable spool | digest and size |
| Helm repository index, chart archives | 32 MiB, 256 MiB | archive layout |

## Exemptions and open items

These are stated rather than hidden:

- **Blob uploads to `/v2/` have no size bound.** A push carries
  multi-gigabyte layers by design. The `operator` role gates it, and the
  digest is verified when the upload is committed.
- **OCI layout paths are arbitrary host paths.** An administrator chooses
  them on purpose (FR-051), and the choice is recorded in the audit log.
- **Lenient decoders** are kept where the format requires them: OCI
  documents (unknown fields are allowed by the specification),
  `dockerconfigjson`, the scanner's report and Helm indexes.
- **Open items from this review**, tracked for a later release:
  - The configuration file is not size-bounded, and a second YAML document
    in it is ignored.
  - An image configuration blob is bounded only by the size its manifest
    declares.
  - `Chart.yaml` is truncated at 1 MiB instead of refused.
  - Account names refuse only `:`.
  - The accounts file is decoded leniently.
  - Some refusals echo the underlying error text in their detail
    parameter.
