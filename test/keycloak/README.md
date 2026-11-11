# test/keycloak: the identity-provider fixture

A real Keycloak, in a container, for the OIDC and SAML tests of milestone 6
(ROADMAP, Jalon 6, arbitrages 2 and 3). It exists so that the authentication
code is exercised against the software a deployment would actually meet, over
a real socket — not against a fake that can only agree with its author.

## What it contains

| Piece | Where |
| --- | --- |
| Image, pinned by tag **and** digest | `IMAGE` (the single place; renovate bumps both) |
| Realm `tobby`, imported at start-up | `realm/tobby-realm.json` |
| Go helper | `keycloak.go` (start, wait, URLs, accounts), `login.go` (browserless login) |
| Proof that it answers | `keycloak_test.go` |

The realm holds:

- **OIDC client** `tobby-oidc`: confidential, authorization code only,
  **PKCE S256 required** (a request without a challenge gets no code), a
  `groups` claim in the ID token, access token and userinfo.
- **SAML client** (the Tobby SP): signed assertions **and** responses
  (RSA-SHA256), persistent `NameID`, a multi-valued `groups` attribute,
  HTTP-POST response binding, **IdP-initiated SSO disabled**. AuthnRequests
  are not required to be signed (the SP-initiated request of the code under
  test is checked by InResponseTo, not by signature).
- **Groups** `tobby-viewer`, `tobby-operator`, `tobby-admin`: the IdP side of
  the group-to-role mapping (ADR-0009, `docs/rbac-matrix.md`).
- **Accounts**, one per role, plus one in no group (all values fictitious,
  listed at the top of the realm file):
  `keycloak.Viewer`, `keycloak.Operator`, `keycloak.Admin`, `keycloak.NoGroup`.

The redirect URI of the OIDC client, the SAML entity ID and the ACS URL are
placeholders in the realm file, filled from `keycloak.Options` when the
container starts (defaults are inert `.test` / loopback values).

## Using it in a test

```go
f := keycloak.Start(t, keycloak.Options{OIDCRedirectURI: srv.URL + "/callback"})
// f.Issuer, f.SAMLMetadataURL, keycloak.ClientID / ClientSecret, keycloak.Operator...
redirect, err := f.LoginOIDC(ctx, authCodeURL, keycloak.Operator) // redirect.Query().Get("code")
post, err := f.LoginSAML(ctx, ssoURLWithAuthnRequest, keycloak.Viewer) // post.SAMLResponse
```

`LoginOIDC` and `LoginSAML` submit the Keycloak login form over plain HTTP
(no browser) and stop at the redirect or the auto-post form: the caller
decides what to do with the code or the response. A wrong password, a wrong
redirect URI or a missing PKCE challenge come back as an error that carries
Keycloak's own message.

Boot takes 15-30 s, so start one fixture per package and run the cases as
subtests (as `TestFixture` does). The container is removed when the test ends.

## Running it

```sh
mise run test-keycloak     # go test ./test/keycloak/...
```

- Needs **docker**. Without it the tests skip with a message naming what is
  missing; `TOBBY_KEYCLOAK_REQUIRE_DOCKER=1` (set by CI on Linux) makes that a
  failure instead.
- The only network use is the pull of the pinned image. Keycloak runs in
  `start-dev` mode (embedded H2), which is fine for a throw-away fixture and
  never a deployment model.
- `TOBBY_KEYCLOAK_URL=http://127.0.0.1:8080` attaches to a Keycloak already
  running with the realm imported (the redirect options are then those of
  whoever imported it).

## Known limit

Keycloak's SAML IdP descriptor is **not** itself XML-signed: it publishes the
realm's signing certificate in a `KeyDescriptor`, which is what an SP pins.
The tests assert exactly that. Signed *assertions* are asserted on the login
path.

## Bumping Keycloak

Edit `IMAGE` (tag and digest together; `docker buildx imagetools inspect
<tag>` gives the index digest), run `mise run test-keycloak`.
