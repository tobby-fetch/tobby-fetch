// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// testIDP is a hand-rolled SAML identity provider: it signs exactly the
// response a test asks for — including the ones no conforming IdP would
// send (unsigned, re-signed by a stranger, tampered after signing,
// IdP-initiated, for another audience, out of the clock window). The
// conforming path is proved against Keycloak (test/federation).
type testIDP struct {
	entityID string
	sso      string
	cert     tls.Certificate
	pemCert  []byte
}

func newKeyPair(t testing.TB, cn string) (pair tls.Certificate, certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pair, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair, certPEM, keyPEM
}

func newTestIDP(t testing.TB) *testIDP {
	t.Helper()
	pair, certPEM, _ := newKeyPair(t, "test-idp")
	return &testIDP{entityID: "https://idp.test/realms/tobby", sso: "https://idp.test/sso", cert: pair, pemCert: certPEM}
}

func (idp *testIDP) metadata() []byte {
	b64 := base64.StdEncoding.EncodeToString(idp.cert.Certificate[0])
	return []byte(fmt.Sprintf(`<?xml version="1.0"?>
<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="http://www.w3.org/2000/09/xmldsig#" entityID=%q>
  <md:IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <md:KeyDescriptor use="signing"><ds:KeyInfo><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>
    <md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location=%q/>
  </md:IDPSSODescriptor>
</md:EntityDescriptor>`, idp.entityID, b64, idp.sso))
}

// resp shapes one response.
type resp struct {
	inResponseTo string // "" = unsolicited
	destination  string // "-" = omitted
	recipient    string
	audience     string
	issuer       string
	nameID       string
	groups       []string
	issued       time.Time
	assertionID  string
	unsigned     bool
	signer       *tls.Certificate // nil = the IdP's key
	tamper       func(*etree.Element)
}

const (
	testEntity = "https://tobby.test/auth/saml/metadata"
	testACS    = "https://tobby.test/auth/saml/acs"
)

func (idp *testIDP) response(t testing.TB, r *resp) []byte {
	t.Helper()
	if r.issued.IsZero() {
		r.issued = time.Now()
	}
	if r.destination == "" {
		r.destination = testACS
	}
	if r.recipient == "" {
		r.recipient = testACS
	}
	if r.audience == "" {
		r.audience = testEntity
	}
	if r.issuer == "" {
		r.issuer = idp.entityID
	}
	if r.nameID == "" {
		r.nameID = "alice"
	}
	if r.assertionID == "" {
		r.assertionID = "_a" + strings.ReplaceAll(randomString(), "-", "x")
	}
	ts := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }
	irt := ""
	if r.inResponseTo != "" {
		irt = fmt.Sprintf(` InResponseTo=%q`, r.inResponseTo)
	}
	dest := ""
	if r.destination != "-" {
		dest = fmt.Sprintf(` Destination=%q`, r.destination)
	}
	var attrs strings.Builder
	for _, g := range r.groups {
		fmt.Fprintf(&attrs, `<saml:AttributeValue>%s</saml:AttributeValue>`, g)
	}
	assertion := fmt.Sprintf(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID=%q Version="2.0" IssueInstant=%q>`+
		`<saml:Issuer>%s</saml:Issuer>`+
		`<saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent">%s</saml:NameID>`+
		`<saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData%s NotOnOrAfter=%q Recipient=%q/></saml:SubjectConfirmation></saml:Subject>`+
		`<saml:Conditions NotBefore=%q NotOnOrAfter=%q><saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction></saml:Conditions>`+
		`<saml:AuthnStatement AuthnInstant=%q SessionIndex="s1"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement>`+
		`<saml:AttributeStatement><saml:Attribute Name="groups">%s</saml:Attribute></saml:AttributeStatement>`+
		`</saml:Assertion>`,
		r.assertionID, ts(r.issued), r.issuer, r.nameID, irt, ts(r.issued.Add(5*time.Minute)), r.recipient,
		ts(r.issued.Add(-time.Minute)), ts(r.issued.Add(5*time.Minute)), r.audience, ts(r.issued), attrs.String())

	doc := etree.NewDocument()
	if err := doc.ReadFromString(assertion); err != nil {
		t.Fatal(err)
	}
	el := doc.Root()
	if !r.unsigned {
		signer := idp.cert
		if r.signer != nil {
			signer = *r.signer
		}
		ctx := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(signer))
		ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
		sig, err := ctx.ConstructSignature(el, true)
		if err != nil {
			t.Fatal(err)
		}
		// The schema wants the signature right after the Issuer; the
		// enveloped transform drops it from the digest wherever it sits.
		el.InsertChildAt(1, sig)
	}
	if r.tamper != nil {
		r.tamper(el)
	}
	inner := etree.NewDocument()
	inner.SetRoot(el)
	assertionXML, err := inner.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return []byte(fmt.Sprintf(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_r%d" Version="2.0" IssueInstant=%q%s%s>`+
		`<saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>%s</samlp:Response>`,
		time.Now().UnixNano(), ts(r.issued), dest, irt, r.issuer, assertionXML))
}

// samlService builds a SAML-only service over idp's pinned metadata.
func samlService(t testing.TB, idp *testIDP, skew time.Duration) *Service {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	_, spCert, spKey := newKeyPair(t, "tobby-sp")
	svc, err := New(&Options{
		SAML: config.SAML{
			Enabled: true, EntityID: testEntity, ACSURL: testACS,
			IDPMetadataFile: write("idp.xml", idp.metadata()),
			CertFile:        write("sp.crt", spCert), KeyFile: write("sp.key", spKey),
			GroupsAttribute: "groups", ClockSkew: config.Duration(skew),
			Roles: config.RoleMapping{Viewer: []string{"staff"}, Admin: []string{"root"}},
		},
		Client: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// begin starts a sign-in and returns the request ID the IdP must answer,
// the RelayState, and the browser binding.
func begin(t testing.TB, svc *Service) (requestID, relay, binding string) {
	t.Helper()
	target, binding, err := svc.BeginSAML(context.Background(), "/recipes")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(target, "https://idp.test/sso?") {
		t.Fatalf("AuthnRequest sent to %s", target)
	}
	raw, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	inflated, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		ID  string `xml:"ID,attr"`
		ACS string `xml:"AssertionConsumerServiceURL,attr"`
	}
	if err := xml.Unmarshal(inflated, &req); err != nil {
		t.Fatal(err)
	}
	if req.ID == "" || req.ACS != testACS {
		t.Fatalf("AuthnRequest = %s", inflated)
	}
	if u.Query().Get("Signature") == "" {
		t.Error("the AuthnRequest is not signed")
	}
	return req.ID, u.Query().Get("RelayState"), binding
}

func post(svc *Service, xmlDoc []byte, relay string) error {
	return svc.ConsumeSAML(context.Background(), base64.StdEncoding.EncodeToString(xmlDoc), relay)
}

// TestSAMLHappyPath: SP-initiated, signed, mapped — and the identity is
// only released to the browser holding the binding.
func TestSAMLHappyPath(t *testing.T) {
	idp := newTestIDP(t)
	svc := samlService(t, idp, 90*time.Second)
	id, relay, _ := begin(t, svc)
	if err := post(svc, idp.response(t, &resp{inResponseTo: id, groups: []string{"staff", "root"}}), relay); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishSAML(relay, randomString()); err == nil {
		t.Fatal("another browser collected the sign-in")
	}
	// The wrong-browser attempt spent the sign-in.
	id, relay, binding := begin(t, svc)
	if err := post(svc, idp.response(t, &resp{inResponseTo: id, groups: []string{"staff", "root"}}), relay); err != nil {
		t.Fatal(err)
	}
	res, err := svc.FinishSAML(relay, binding)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "saml:alice" || res.Role != auth.RoleAdmin || res.Next != "/recipes" || res.Method != auth.MethodSAML {
		t.Errorf("result = %+v", res)
	}
	md, err := svc.SAMLMetadata()
	if err != nil || !bytes.Contains(md, []byte(testACS)) || !bytes.Contains(md, []byte(testEntity)) {
		t.Errorf("SP metadata = %s (%v)", md, err)
	}
}

func wantSAML(t *testing.T, err error, check string) {
	const code = taxonomy.CodeSAMLFailed
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want %s/%s", err, code, check)
	}
	if f.Code != code || f.Check != check {
		t.Fatalf("failure = %s/%s (%v), want %s/%s", f.Code, f.Check, f.Cause, code, check)
	}
}

// TestSAMLAttacks is the refusal catalogue of the service provider.
func TestSAMLAttacks(t *testing.T) {
	idp := newTestIDP(t)
	svc := samlService(t, idp, 90*time.Second)
	stranger, _, _ := newKeyPair(t, "stranger")
	staff := []string{"staff"}

	cases := []struct {
		name  string
		shape func(id string) *resp
		check string
	}{
		{"unsigned", func(id string) *resp { return &resp{inResponseTo: id, groups: staff, unsigned: true} }, "signature"},
		{"signed by a stranger", func(id string) *resp { return &resp{inResponseTo: id, groups: staff, signer: &stranger} }, "signature"},
		{"tampered after signing", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, tamper: func(el *etree.Element) {
				el.FindElement("//NameID").SetText("admin")
			}}
		}, "signature"},
		{"wrong audience", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, audience: "https://other-sp.test"}
		}, "audience"},
		{"wrong destination", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, destination: "https://other-sp.test/acs"}
		}, "destination"},
		{"no destination", func(id string) *resp { return &resp{inResponseTo: id, groups: staff, destination: "-"} }, "destination"},
		{"wrong recipient", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, recipient: "https://other-sp.test/acs"}
		}, "destination"},
		{"wrong issuer", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, issuer: "https://evil.test"}
		}, "issuer"},
		{"answers another request", func(string) *resp { return &resp{inResponseTo: "_not-mine", groups: staff} }, "in-response-to"},
		{"unsolicited inside a started sign-in", func(string) *resp { return &resp{groups: staff} }, "in-response-to"},
		{"from the future beyond the tolerance", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, issued: time.Now().Add(3 * time.Minute)}
		}, "clock"},
		{"stale beyond the tolerance", func(id string) *resp {
			return &resp{inResponseTo: id, groups: staff, issued: time.Now().Add(-4 * time.Minute)}
		}, "clock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, relay, _ := begin(t, svc)
			wantSAML(t, post(svc, idp.response(t, tc.shape(id)), relay), tc.check)
		})
	}

	t.Run("within the tolerance", func(t *testing.T) {
		id, relay, _ := begin(t, svc)
		if err := post(svc, idp.response(t, &resp{inResponseTo: id, groups: staff, issued: time.Now().Add(time.Minute)}), relay); err != nil {
			t.Errorf("a response one minute ahead was refused with a 90s tolerance: %v", err)
		}
	})
	t.Run("idp-initiated", func(t *testing.T) {
		// No RelayState, or one this instance never issued: refused before
		// the XML is parsed.
		doc := idp.response(t, &resp{groups: staff})
		wantSAML(t, post(svc, doc, ""), "in-response-to")
		wantSAML(t, post(svc, doc, randomString()), "in-response-to")
	})
	t.Run("replayed assertion", func(t *testing.T) {
		id, relay, _ := begin(t, svc)
		first := &resp{inResponseTo: id, groups: staff}
		if err := post(svc, idp.response(t, first), relay); err != nil {
			t.Fatal(err)
		}
		// The same response again on the same sign-in.
		wantSAML(t, post(svc, idp.response(t, &resp{inResponseTo: id, groups: staff, assertionID: first.assertionID}), relay),
			"in-response-to")
		// The same assertion, rewrapped for a fresh sign-in: the replay
		// cache catches the assertion ID.
		id2, relay2, _ := begin(t, svc)
		wantSAML(t, post(svc, idp.response(t, &resp{inResponseTo: id2, groups: staff, assertionID: first.assertionID}), relay2),
			"replay")
	})
	t.Run("no mapped group", func(t *testing.T) {
		id, relay, _ := begin(t, svc)
		err := post(svc, idp.response(t, &resp{inResponseTo: id, groups: []string{"marketing"}}), relay)
		var f *Failure
		if !errors.As(err, &f) || f.Code != taxonomy.CodeFederatedNoRole || f.Identity != "saml:alice" {
			t.Errorf("err = %v, want TBY-AUTH-017 for saml:alice", err)
		}
	})
	t.Run("not base64", func(t *testing.T) {
		_, relay, _ := begin(t, svc)
		wantSAML(t, svc.ConsumeSAML(context.Background(), "%%%", relay), "encoding")
	})
	t.Run("refused response spends the sign-in", func(t *testing.T) {
		id, relay, _ := begin(t, svc)
		_ = post(svc, idp.response(t, &resp{inResponseTo: id, groups: staff, unsigned: true}), relay)
		wantSAML(t, post(svc, idp.response(t, &resp{inResponseTo: id, groups: staff}), relay), "in-response-to")
	})
}

// TestSAMLConfigurableSkew: the tolerance is the configured one, in both
// directions — a narrow one refuses what the default accepts.
func TestSAMLConfigurableSkew(t *testing.T) {
	idp := newTestIDP(t)
	tight := samlService(t, idp, 10*time.Second)
	id, relay, _ := begin(t, tight)
	wantSAML(t, post(tight, idp.response(t, &resp{inResponseTo: id, groups: []string{"staff"}, issued: time.Now().Add(time.Minute)}), relay),
		"clock")
	wide := samlService(t, idp, 4*time.Minute)
	id, relay, _ = begin(t, wide)
	if err := post(wide, idp.response(t, &resp{inResponseTo: id, groups: []string{"staff"}, issued: time.Now().Add(3 * time.Minute)}), relay); err != nil {
		t.Errorf("a 4m tolerance refused a response 3m ahead: %v", err)
	}
}

// TestSAMLPinnedCertificateWins: with idpCertificateFile set, a response
// signed by the key in the metadata — but not the pinned one — is refused.
func TestSAMLPinnedCertificateWins(t *testing.T) {
	idp := newTestIDP(t)
	other := newTestIDP(t) // same entity ID and endpoints, another key
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	_, spCert, spKey := newKeyPair(t, "tobby-sp")
	svc, err := New(&Options{
		SAML: config.SAML{
			Enabled: true, EntityID: testEntity, ACSURL: testACS,
			IDPMetadataFile:    write("idp.xml", idp.metadata()),
			IDPCertificateFile: write("pinned.crt", other.pemCert),
			CertFile:           write("sp.crt", spCert), KeyFile: write("sp.key", spKey),
			ClockSkew: config.Duration(90 * time.Second), Roles: config.RoleMapping{Viewer: []string{"staff"}},
		},
		Client: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, relay, _ := begin(t, svc)
	wantSAML(t, post(svc, idp.response(t, &resp{inResponseTo: id, groups: []string{"staff"}}), relay), "signature")
	id, relay, _ = begin(t, svc)
	if err := post(svc, other.response(t, &resp{inResponseTo: id, groups: []string{"staff"}}), relay); err != nil {
		t.Errorf("a response signed by the pinned key was refused: %v", err)
	}
}

// TestSAMLReplayCacheFailsClosed: full of live IDs, the cache refuses
// rather than forgets.
func TestSAMLReplayCacheFailsClosed(t *testing.T) {
	now := time.Now()
	c := &replayCache{now: func() time.Time { return now }}
	for i := range replayCap {
		if err := c.Consume(fmt.Sprintf("id-%d", i), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Consume("one-more", now.Add(time.Hour)); !errors.Is(err, errReplayCacheFull) {
		t.Errorf("full cache: %v, want errReplayCacheFull", err)
	}
	now = now.Add(2 * time.Hour)
	if err := c.Consume("one-more", now.Add(time.Hour)); err != nil {
		t.Errorf("after expiry: %v", err)
	}
}
