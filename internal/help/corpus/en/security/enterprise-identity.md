---
title: Enterprise identity (OIDC, SAML)
description: Connect Tobby to your organization's directory with OpenID Connect or SAML 2.0, map directory groups onto roles, bound sessions and tokens, and keep working when the identity provider is down.
sidebar:
  order: 5
---

Tobby speaks both protocols an enterprise directory offers — **OpenID
Connect** (authorization code flow) and **SAML 2.0** (SP-initiated) — and
keeps its two local methods, accounts and static tokens, working beside
them (ADR-0009, FR-070, FR-071, FR-078). Both federated methods are **off
by default**; enabling one never disables anything else.

## How a federated sign-in works

1. The sign-in page shows one button per enabled provider, above the local
   form.
2. The button starts a round trip under `/auth/`: Tobby remembers what it
   sent (OIDC state, nonce and PKCE verifier; the SAML request ID) **on the
   server**, and gives the browser only an opaque binding cookie.
3. The provider sends the browser back; Tobby verifies the answer, maps
   the person's groups onto a role, and opens an ordinary Tobby session —
   same idle timeout, same CSRF protection, same role floors as a local
   one.

An answer that does not come back to the browser that started the
sign-in is refused, whatever it contains. After sign-in, the browser
lands on the page it asked for, through the same anti-open-redirect check
as the local form.

**Identity key and display name.** A federated identity is keyed on what
the provider guarantees stable — the OIDC `sub`, the SAML persistent
`NameID` — as `oidc:<sub>` or `saml:<NameID>`: never confused with a local
account, never reassigned when someone is renamed. The readable name
(`auth.oidc.nameClaim`, default `preferred_username`; `auth.saml.nameAttribute`)
is shown on screen and recorded as `actor_name` beside `actor` in the logs
and the audit trail. It is display only: no role, no session and no
decision ever rests on it — filter the audit trail on `actor`. Federated
identities have no local password to change.

## OpenID Connect

Register Tobby at the provider as a **confidential client**, authorization
code flow, with **PKCE S256** required, and the redirect URI
`https://<tobby-host>/auth/oidc/callback`. Ask the provider to put the
user's groups in the **ID token** (a `groups` claim by default).

```yaml
auth:
  oidc:
    enabled: true
    displayName: Corporate directory      # the button reads "Sign in with …"
    issuer: https://idp.example.com/realms/corp
    clientID: tobby
    clientSecretFile: /etc/tobby/oidc-client-secret   # a path, never the value
    redirectURL: https://tobby.example.com/auth/oidc/callback
    scopes: [profile]                     # "openid" is always requested
    groupsClaim: groups
    nameClaim: preferred_username         # display only; the key is "sub"
    # signingAlgorithms: [RS256, ES256]   # default: every asymmetric algorithm
    roles:
      viewer:   [tobby-viewers]
      operator: [tobby-operators]
      admin:    [tobby-admins]
```

What Tobby checks, and refuses with **TBY-AUTH-014** otherwise (the precise
check is in the instance log, never on the page):

- discovery from the configured issuer only, whose document must name
  exactly that issuer;
- `state`, `nonce` and the PKCE verifier of the sign-in this browser
  started — each used once;
- the ID token signature, against the provider's published keys, with an
  algorithm of the allow-list: `none` and the `HS*` family are refused in
  configuration and in code;
- the issuer and the audience (the client ID; a token minted for several
  clients must name Tobby as its authorized party), and the expiry.

`issuer` must be `https://`; the redirect URL must be the
`/auth/oidc/callback` path of the instance. Every call to the provider
goes through the instance's [outbound network settings](../../passthrough/network/)
— proxy, private certificate authorities — and follows no redirect.

## SAML 2.0

Register Tobby at the identity provider as a service provider: its
descriptor is served at `https://<tobby-host>/auth/saml/metadata` (entity
ID, assertion consumer service, certificate). At the provider, require
**signed assertions** (or responses), the **HTTP-POST** binding for the
response, a stable `NameID`, a multi-valued **groups attribute**, and
disable IdP-initiated sign-in.

```yaml
auth:
  saml:
    enabled: true
    displayName: Corporate SSO
    entityID: https://tobby.example.com/auth/saml/metadata
    acsURL: https://tobby.example.com/auth/saml/acs
    idpMetadataFile: /etc/tobby/idp-metadata.xml   # pinned
    # idpCertificateFile: /etc/tobby/idp-signing.pem  # optional with a file
    certFile: /etc/tobby/saml-sp.crt
    keyFile: /etc/tobby/saml-sp.key                 # secret: outside the store
    groupsAttribute: groups
    nameAttribute: displayName                      # display only; the key is the NameID
    clockSkew: 90s                                  # at most 5m
    roles:
      viewer:   [tobby-viewers]
      operator: [tobby-operators]
      admin:    [tobby-admins]
```

**Pin, do not fetch.** Download the provider's metadata once, check it,
and point `idpMetadataFile` at it: the signing certificate it carries is
the only key assertions are verified with. Some providers — Keycloak among
them — do not sign their own descriptor, so a descriptor fetched over the
network proves nothing about that key. `idpMetadataURL` exists for
providers that rotate endpoints, but it is an outbound connection you
choose explicitly (NFR-019), and it **requires** `idpCertificateFile`: the
fetched document can then update endpoints, never the key.

What Tobby checks, and refuses with **TBY-AUTH-015** otherwise:

- **SP-initiated only**: the response must answer a request this instance
  sent (`InResponseTo`) and come back with its opaque `RelayState`; an
  unsolicited, IdP-initiated response is refused before any XML is read;
- the XML signature, against the pinned certificate (SHA-1 refused);
- `Audience` (the entity ID), `Destination` (always required) and
  `Recipient` (the ACS URL), `Issuer` (the provider's entity ID);
- the validity window, with `clockSkew` of tolerance in both directions;
- **replay**: every assertion ID is remembered for as long as it could be
  accepted, and a second use is refused;
- the body is bounded (256 KiB) before anything parses it.

The response is a cross-site POST, which carries no `SameSite=Lax`
cookie. Tobby verifies it at `/auth/saml/acs`, then sends the browser to
`/auth/saml/finish`, a plain navigation that does carry the binding
cookie: the session opens only there.

## Mapping groups onto roles

`roles` lists, per role, the group names (OIDC claim values, SAML
attribute values) that grant it — exact match. A person in several mapped
groups gets the **highest** role. A person in **no** mapped group is
refused with **TBY-AUTH-017** and gets no session: there is no default
role. A provider section whose mapping is empty is refused at startup.

The role is evaluated at each sign-in; a group change at the provider
applies at the next one. Sessions in progress keep their role until they
end.

## Bounded sessions and tokens

These apply to every method (R-14):

| Key | Default | Effect |
| --- | --- | --- |
| `auth.session.idleTimeout` | `30m` | A session without user activity for this long ends. Navigations and actions renew it; the requests a page sends on its own (task polling, log tails) do not, so a tab left open still expires. The next page — or the poll that finds the session dead — shows **TBY-AUTH-005** and returns to the page after sign-in. |
| `auth.sessionTTL` | `12h` | Absolute session lifetime: activity never extends it. |
| `auth.tokens.defaultLifetime` | none | Expiry given to API tokens created without one. A token can also get its own (lifetime in days on `/admin/accounts`, `expires` in `POST /api/v1/tokens`). |

An expired token is refused with **TBY-AUTH-013** — distinct from a wrong
or revoked one. The audit log records `session.expire` (target `idle` or
`lifetime`) and, on each refusal of an expired token, `token.expire`
under the token's name. Rotate tokens by overlap: create the replacement,
roll the clients, revoke or let expire the old one.

## When the identity provider is down

Federation never becomes a single point of failure (R-20, FR-078):

- The instance **starts** whether the provider answers or not: nothing at
  startup contacts it.
- A background probe watches the provider (OIDC discovery; the metadata
  URL for SAML when one is configured). While it does not answer, the
  sign-in page states it with **TBY-AUTH-016** naming the provider, and
  keeps the local form. The button stays: it retries.
- **Existing sessions** continue; **static tokens** keep authorizing
  `/api/v1`, `/v2/` and `/files/`; **local accounts** sign in.
- Every local-account sign-in while a federated method is enabled is
  recorded as `auth.local_under_federation` — on the sign-in form and on
  the machine surfaces — beside the ordinary record, so break-glass use is
  one filter away. `tobby_auth_idp_reachable` and
  `tobby_auth_local_logins_under_federation_total` give the same picture
  to your monitoring ([metrics](../../reference/metrics-logs/)).
- Federated sign-in comes back **without a restart** as soon as the
  provider answers.

With SAML and pinned metadata, Tobby itself never calls the provider: an
outage is between the browser and the provider, and the instance is
untouched.

**Keep a local administrator.** On a federated instance, the local `admin`
account is the break-glass credential: store its password like one, and
watch the `auth.local_under_federation` events.

## Secrets

`auth.oidc.clientSecretFile` and `auth.saml.keyFile` are credentials of
the instance at the provider. Like every secret path, they are refused at
startup if they resolve inside the transportable store
([secrets](../secrets/)).

## Troubleshooting

| Code | Meaning | First look |
| --- | --- | --- |
| TBY-AUTH-014 | The OIDC answer failed a check | the `check` field of the "federated sign-in refused" log line: `state` or `binding` (sign-in started in another tab or browser), `nonce`, `id-token` (signature, issuer, audience, expiry), `token-exchange` |
| TBY-AUTH-015 | The SAML response was refused | same log line: `in-response-to`, `signature`, `audience`, `destination`, `issuer`, `clock`, `replay` |
| TBY-AUTH-016 | The provider cannot be reached | proxy and private CA settings, the provider's availability; local sign-in works meanwhile |
| TBY-AUTH-017 | Signed in, but no role | the person's groups against `roles`; group names are case-sensitive |

See the [error reference](../../reference/errors/) for every code.
