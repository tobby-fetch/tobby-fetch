// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package keycloak_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/test/keycloak"
)

// The fixture costs several seconds to boot, so one instance serves every
// test of the package. Start ties the container to the test that asks, so
// the tests run as subtests of this one.
func TestFixture(t *testing.T) {
	f := keycloak.Start(t, keycloak.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Run("oidc discovery", func(t *testing.T) { oidcDiscovery(ctx, t, f) })
	t.Run("saml metadata", func(t *testing.T) { samlMetadata(ctx, t, f) })
	t.Run("oidc login helper", func(t *testing.T) { oidcLogin(ctx, t, f) })
	t.Run("saml login helper", func(t *testing.T) { samlLogin(ctx, t, f) })
}

func get(ctx context.Context, t *testing.T, target string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	return b
}

// Proves only that the fixture answers: the discovery document names the
// issuer the fixture reports, and the endpoints a client needs.
func oidcDiscovery(ctx context.Context, t *testing.T, f *keycloak.Fixture) {
	var doc struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		JWKSURI               string   `json:"jwks_uri"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	if err := json.Unmarshal(get(ctx, t, f.Issuer+"/.well-known/openid-configuration"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Issuer != f.Issuer {
		t.Errorf("issuer = %q, want %q", doc.Issuer, f.Issuer)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		t.Errorf("incomplete discovery document: %+v", doc)
	}
	var s256 bool
	for _, m := range doc.CodeChallengeMethods {
		s256 = s256 || m == "S256"
	}
	if !s256 {
		t.Errorf("S256 missing from code_challenge_methods_supported: %v", doc.CodeChallengeMethods)
	}
}

// Proves only that the fixture answers: the IdP descriptor is well-formed
// SAML metadata that publishes a signing certificate and an HTTP-POST
// single sign-on endpoint — what an SP pins in its configuration.
func samlMetadata(ctx context.Context, t *testing.T, f *keycloak.Fixture) {
	var md struct {
		XMLName xml.Name `xml:"EntityDescriptor"`
		IDP     struct {
			Keys []struct {
				Use  string `xml:"use,attr"`
				Cert string `xml:"KeyInfo>X509Data>X509Certificate"`
			} `xml:"KeyDescriptor"`
			SSO []struct {
				Binding  string `xml:"Binding,attr"`
				Location string `xml:"Location,attr"`
			} `xml:"SingleSignOnService"`
		} `xml:"IDPSSODescriptor"`
	}
	if err := xml.Unmarshal(get(ctx, t, f.SAMLMetadataURL), &md); err != nil {
		t.Fatal(err)
	}
	var signing bool
	for _, k := range md.IDP.Keys {
		signing = signing || (k.Use == "signing" && strings.TrimSpace(k.Cert) != "")
	}
	if !signing {
		t.Error("the IdP descriptor publishes no signing certificate")
	}
	var post bool
	for _, s := range md.IDP.SSO {
		post = post || (strings.HasSuffix(s.Binding, "HTTP-POST") && s.Location == f.SAMLSSOURL)
	}
	if !post {
		t.Errorf("no HTTP-POST SSO endpoint at %s: %+v", f.SAMLSSOURL, md.IDP.SSO)
	}
}

func authURL(f *keycloak.Fixture, challenge string) string {
	q := url.Values{
		"client_id":             {keycloak.ClientID},
		"response_type":         {"code"},
		"scope":                 {"openid"},
		"redirect_uri":          {f.Options.OIDCRedirectURI},
		"state":                 {"st-1"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return f.Issuer + "/protocol/openid-connect/auth?" + q.Encode()
}

// The login helper obtains an authorization code for an account, refuses a
// wrong password, and the realm refuses a request without PKCE.
func oidcLogin(ctx context.Context, t *testing.T, f *keycloak.Fixture) {
	verifier := make([]byte, 32)
	if _, err := rand.Read(verifier); err != nil {
		t.Fatal(err)
	}
	v := base64.RawURLEncoding.EncodeToString(verifier)
	sum := sha256.Sum256([]byte(v))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	for _, u := range []keycloak.User{keycloak.Viewer, keycloak.Operator, keycloak.Admin, keycloak.NoGroup} {
		redir, err := f.LoginOIDC(ctx, authURL(f, challenge), u)
		if err != nil {
			t.Fatalf("%s: %v", u.Username, err)
		}
		if redir.Query().Get("code") == "" || redir.Query().Get("state") != "st-1" {
			t.Errorf("%s: redirect %s carries no code/state", u.Username, redir)
		}
		if !strings.HasPrefix(redir.String(), f.Options.OIDCRedirectURI) {
			t.Errorf("%s: redirect %s is not the registered URI", u.Username, redir)
		}
	}

	bad := keycloak.Viewer
	bad.Password = "not-the-password"
	if _, err := f.LoginOIDC(ctx, authURL(f, challenge), bad); err == nil {
		t.Error("a wrong password obtained a code")
	}

	// PKCE is mandatory on this client: no challenge, no code.
	noPKCE := strings.NewReplacer("&code_challenge="+url.QueryEscape(challenge), "", "&code_challenge_method=S256", "").
		Replace(authURL(f, challenge))
	redir, err := f.LoginOIDC(ctx, noPKCE, keycloak.Viewer)
	if err != nil {
		t.Fatal(err)
	}
	if redir.Query().Get("code") != "" || redir.Query().Get("error") != "invalid_request" {
		t.Errorf("an authorization request without PKCE was not refused: %s", redir)
	}
}

// The SAML login helper obtains a signed response with a persistent NameID
// and the groups attribute, and the realm refuses IdP-initiated SSO.
func samlLogin(ctx context.Context, t *testing.T, f *keycloak.Fixture) {
	reqURL, err := f.SAMLAuthnRequestURL("_tobby-test-1", "relay-1")
	if err != nil {
		t.Fatal(err)
	}
	post, err := f.LoginSAML(ctx, reqURL, keycloak.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if post.Action != f.Options.SAMLACSURL || post.RelayState != "relay-1" {
		t.Errorf("POST to %q with RelayState %q", post.Action, post.RelayState)
	}
	doc, err := post.XML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	for _, want := range []string{
		`InResponseTo="_tobby-test-1"`,
		"nameid-format:persistent",
		`Name="groups"`,
		">" + keycloak.GroupOperator + "<",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SAML response lacks %q", want)
		}
	}
	if n := strings.Count(s, "<dsig:Signature") + strings.Count(s, "<ds:Signature") + strings.Count(s, "<Signature"); n < 2 {
		t.Errorf("want the assertion AND the response signed, found %d signatures", n)
	}

	// IdP-initiated SSO is off: the endpoint does not mint an unsolicited
	// response for the SP.
	idpInit := f.SAMLSSOURL + "/clients/" + url.PathEscape(f.Options.SAMLEntityID)
	if _, err := f.LoginSAML(ctx, idpInit, keycloak.Viewer); err == nil {
		t.Error("IdP-initiated SSO produced a SAML response")
	}
}
