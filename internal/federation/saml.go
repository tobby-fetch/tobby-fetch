// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The SAML 2.0 service provider (FR-071, ADR-0009 arbitration 3).
//
// Only saml.ServiceProvider is used — never the samlsp middleware: the
// session is Tobby's own, and so is the request tracking. The library
// verifies the response (XML signature against the pinned certificate,
// InResponseTo against the request this instance sent, Audience,
// Destination, Recipient, Issuer, validity window, replay through the
// cache below); this file adds what Tobby requires beyond it:
//
//   - SP-initiated only: AllowIDPInitiated stays false, and a response
//     whose RelayState names no sign-in this instance started is refused
//     before the library even sees it;
//   - a Destination is mandatory, even on a response whose signature
//     covers only the assertion;
//   - the clock tolerance is auth.saml.clockSkew, bounded at five minutes,
//     not the library's package-wide default;
//   - the body is bounded before it is parsed (MaxSAMLBody, B-034);
//   - the identity provider's metadata and signing certificate are pinned
//     in configuration; a metadata URL is contacted only when configured
//     (NFR-019), and only with a pinned certificate beside it.

// MaxSAMLBody bounds the assertion consumer service's request body,
// before any parsing (B-034). A signed response with a few dozen groups
// is a few kilobytes; 256 KiB leaves room for directories with long group
// lists and none for a parser bomb.
const MaxSAMLBody = 256 << 10

// The library's time checks use package-level bounds. They are widened
// here, once, before anything runs, to the largest tolerance Tobby
// accepts (auth.saml.clockSkew is at most five minutes); the configured
// tolerance is then enforced by checkWindow on every accepted assertion.
// Widened, never narrowed: the library's checks still run, Tobby's are
// the binding ones.
const (
	maxSkew = 5 * time.Minute
	// issueDelay is how old an assertion may be when it arrives, before
	// the clock tolerance: the library's default.
	issueDelay = 90 * time.Second
)

func init() {
	saml.MaxClockSkew = maxSkew
	saml.MaxIssueDelay = issueDelay + maxSkew
}

// replayCap bounds the assertion replay cache. When it is full of live
// entries the cache refuses — failing closed: forgetting an ID to make
// room would reopen exactly the replay it exists to stop.
const replayCap = 16384

// replayCache is the anti-replay cache of assertion IDs
// (saml.AssertionReplayCache). Safe for concurrent use.
type replayCache struct {
	mu  sync.Mutex
	ids map[string]time.Time
	now func() time.Time
}

var errReplayCacheFull = errors.New("the replay cache is full of live assertions")

// Consume implements saml.AssertionReplayCache.
func (c *replayCache) Consume(id string, until time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.ids == nil {
		c.ids = make(map[string]time.Time)
	}
	if exp, seen := c.ids[id]; seen && now.Before(exp) {
		return saml.ErrAssertionReplayed
	}
	if len(c.ids) >= replayCap {
		for k, exp := range c.ids {
			if !now.Before(exp) {
				delete(c.ids, k)
			}
		}
		if len(c.ids) >= replayCap {
			return errReplayCacheFull
		}
	}
	// Kept at least as long as Tobby's own window accepts the assertion.
	if floor := now.Add(issueDelay + maxSkew); until.Before(floor) {
		until = floor
	}
	c.ids[id] = until
	return nil
}

type samlProvider struct {
	svc        *Service
	cfg        config.SAML
	name       string
	key        crypto.Signer
	cert       *x509.Certificate
	pinnedCert string // base64 DER, empty when the metadata's certificates apply
	skew       time.Duration
	acs        url.URL
	metadata   url.URL
	replay     *replayCache

	mu  sync.Mutex
	sp  *saml.ServiceProvider // nil until the IdP metadata is known
	idp *saml.EntityDescriptor
}

func newSAMLProvider(c *config.SAML, svc *Service) (*samlProvider, error) {
	pair, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("auth.saml.certFile / keyFile: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("auth.saml.certFile: %w", err)
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("auth.saml.keyFile: the key cannot sign")
	}
	acs, err := url.Parse(c.ACSURL)
	if err != nil {
		return nil, fmt.Errorf("auth.saml.acsURL: %w", err)
	}
	md := *acs
	md.Path = PathSAMLMetadata
	name := c.DisplayName
	if name == "" {
		name = "SAML"
	}
	skew := time.Duration(c.ClockSkew)
	if skew < 0 || skew > maxSkew {
		return nil, fmt.Errorf("auth.saml.clockSkew must lie between 0 and %s", maxSkew)
	}
	p := &samlProvider{svc: svc, cfg: *c, name: name, key: key, cert: cert, skew: skew,
		acs: *acs, metadata: md, replay: &replayCache{now: svc.now}}
	if c.IDPCertificateFile != "" {
		raw, err := os.ReadFile(c.IDPCertificateFile)
		if err != nil {
			return nil, fmt.Errorf("auth.saml.idpCertificateFile: %w", err)
		}
		blk, _ := pem.Decode(raw)
		if blk == nil || blk.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("auth.saml.idpCertificateFile: %s holds no PEM certificate", c.IDPCertificateFile)
		}
		if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
			return nil, fmt.Errorf("auth.saml.idpCertificateFile: %w", err)
		}
		p.pinnedCert = base64.StdEncoding.EncodeToString(blk.Bytes)
	}
	if c.IDPMetadataFile != "" {
		raw, err := os.ReadFile(c.IDPMetadataFile)
		if err != nil {
			return nil, fmt.Errorf("auth.saml.idpMetadataFile: %w", err)
		}
		ed, err := parseIDPMetadata(raw)
		if err != nil {
			return nil, fmt.Errorf("auth.saml.idpMetadataFile: %w", err)
		}
		if err := p.install(ed); err != nil {
			return nil, fmt.Errorf("auth.saml.idpMetadataFile: %w", err)
		}
	}
	return p, nil
}

// maxMetadata bounds a metadata document, from a file or the network.
const maxMetadata = 1 << 20

// parseIDPMetadata reads an EntityDescriptor — or the first entity of an
// EntitiesDescriptor that has an IdP role — from pinned metadata.
func parseIDPMetadata(raw []byte) (*saml.EntityDescriptor, error) {
	if len(raw) > maxMetadata {
		return nil, fmt.Errorf("metadata larger than %d bytes", maxMetadata)
	}
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(raw, &ed); err == nil && len(ed.IDPSSODescriptors) > 0 {
		return &ed, nil
	}
	var eds saml.EntitiesDescriptor
	if err := xml.Unmarshal(raw, &eds); err != nil {
		return nil, fmt.Errorf("not SAML metadata: %w", err)
	}
	for i := range eds.EntityDescriptors {
		if len(eds.EntityDescriptors[i].IDPSSODescriptors) > 0 {
			return &eds.EntityDescriptors[i], nil
		}
	}
	return nil, errors.New("no identity provider in the metadata")
}

// install builds the service provider over the IdP metadata.
func (p *samlProvider) install(ed *saml.EntityDescriptor) error {
	if ed.EntityID == "" {
		return errors.New("the identity provider has no entityID")
	}
	sp := &saml.ServiceProvider{
		EntityID:          p.cfg.EntityID,
		Key:               p.key,
		Certificate:       p.cert,
		AcsURL:            p.acs,
		MetadataURL:       p.metadata,
		IDPMetadata:       ed,
		AuthnNameIDFormat: saml.PersistentNameIDFormat,
		// SP-initiated only (FR-071): an unsolicited response has no
		// InResponseTo and is refused by the library as well as here.
		AllowIDPInitiated: false,
		ReplayCache:       p.replay,
		// The HTTP-POST binding only: advertising the artifact binding
		// would invite a back-channel SOAP call this instance never needs.
		AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		RejectSHA1Signatures:             true,
		LimitRelayStateLength:            true,
		HTTPClient:                       p.svc.client,
	}
	if _, ok := p.key.Public().(*rsa.PublicKey); ok {
		sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	} else {
		sp.SignatureMethod = dsig.ECDSASHA256SignatureMethod
	}
	if p.pinnedCert != "" {
		pinned := p.pinnedCert
		sp.IDPCertificate = &pinned
	} else if !hasSigningCert(ed) {
		return errors.New("the identity provider's metadata carries no signing certificate to pin, and auth.saml.idpCertificateFile is not set")
	}
	if sp.GetSSOBindingLocation(saml.HTTPRedirectBinding) == "" {
		return errors.New("the identity provider offers no HTTP-Redirect single sign-on endpoint")
	}
	p.mu.Lock()
	p.sp, p.idp = sp, ed
	p.mu.Unlock()
	return nil
}

func hasSigningCert(ed *saml.EntityDescriptor) bool {
	for i := range ed.IDPSSODescriptors {
		kds := ed.IDPSSODescriptors[i].KeyDescriptors
		for j := range kds {
			if kd := &kds[j]; (kd.Use == "" || kd.Use == "signing") && len(kd.KeyInfo.X509Data.X509Certificates) > 0 {
				return true
			}
		}
	}
	return false
}

func (p *samlProvider) current() *saml.ServiceProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sp
}

// probes reports whether the SAML side has a server-to-server dependency
// to watch. With pinned metadata it has none: the browser reaches the
// IdP, Tobby never does, and an IdP outage cannot touch the instance.
func (p *samlProvider) probes() bool { return p.cfg.IDPMetadataURL != "" }

// probe fetches the configured metadata URL and, when it parses,
// refreshes the endpoints (the signing certificate stays the pinned one).
func (p *samlProvider) probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.IDPMetadataURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := p.svc.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &statusError{url: p.cfg.IDPMetadataURL, code: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return err
	}
	ed, err := parseIDPMetadata(raw)
	if err != nil {
		return err
	}
	return p.install(ed)
}

// ready returns the service provider, fetching the metadata first when it
// comes from a URL and has not been fetched yet.
func (s *Service) samlReady(ctx context.Context) (*saml.ServiceProvider, error) {
	p := s.saml
	if sp := p.current(); sp != nil && (!p.probes() || s.isReachable(auth.MethodSAML)) {
		return sp, nil
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := p.probe(pctx); err != nil {
		return nil, s.unreachableFailure(auth.MethodSAML, p.name, err)
	}
	s.setReachable(auth.MethodSAML, true, nil)
	return p.current(), nil
}

func (p *samlProvider) fail(check string, err error) *Failure {
	return &Failure{Code: taxonomy.CodeSAMLFailed, Provider: p.name, Check: check, Cause: err}
}

// SAMLBodyFailure refuses an unreadable or oversized ACS body.
func (s *Service) SAMLBodyFailure(err error) error {
	name := "SAML"
	if s.saml != nil {
		name = s.saml.name
	}
	return &Failure{Code: taxonomy.CodeSAMLFailed, Provider: name, Check: "body", Cause: err}
}

// BeginSAML builds an AuthnRequest (HTTP-Redirect binding, response by
// HTTP-POST), remembers its ID for InResponseTo, and returns the IdP URL
// to send the browser to with the binding secret for BindingCookie. The
// RelayState is the pending key — an opaque random value, never a URL.
func (s *Service) BeginSAML(ctx context.Context, next string) (redirect, binding string, err error) {
	p := s.saml
	if p == nil {
		return "", "", errors.New("federation: SAML is not enabled")
	}
	sp, err := s.samlReady(ctx)
	if err != nil {
		return "", "", err
	}
	req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", "", p.fail("authn-request", err)
	}
	binding = randomString()
	relay := s.pending.put(&pending{
		method: auth.MethodSAML, binding: bindingOf(binding), next: next, requestID: req.ID,
	}, s.now())
	u, err := req.Redirect(relay, sp)
	if err != nil {
		return "", "", p.fail("authn-request", err)
	}
	return u.String(), binding, nil
}

// ConsumeSAML verifies a response received at the ACS. relay must name a
// sign-in this instance started (SP-initiated only); the verified
// identity is held on that sign-in until FinishSAML binds it to the
// browser. Every refusal is TBY-AUTH-015 with the check named for the
// log, or TBY-AUTH-017 for a verified identity with no mapped group.
func (s *Service) ConsumeSAML(_ context.Context, samlResponse, relay string) error {
	p := s.saml
	if p == nil {
		return errors.New("federation: SAML is not enabled")
	}
	now := s.now()
	e, ok := s.pending.peek(relay, now)
	if !ok || e.method != auth.MethodSAML || e.asserted != nil {
		// No RelayState, an unknown one, or one already answered: an
		// unsolicited (IdP-initiated) or replayed response. Refused before
		// any XML is parsed.
		return p.fail("in-response-to", errors.New("the response answers no sign-in this instance started"))
	}
	sp := p.current()
	if sp == nil {
		return p.fail("metadata", errors.New("the identity provider metadata is not loaded"))
	}
	raw, err := base64.StdEncoding.DecodeString(samlResponse)
	if err != nil || len(raw) == 0 {
		return p.fail("encoding", fmt.Errorf("the SAMLResponse is not base64: %w", err))
	}
	if len(raw) > MaxSAMLBody {
		return p.fail("body", errors.New("the decoded response exceeds the bound"))
	}
	res, err := p.verify(sp, raw, e.requestID, now)
	if err != nil {
		// The sign-in is spent: a refused response is not retried on it.
		s.pending.take(relay, now)
		return err
	}
	res.Next = e.next
	if !s.pending.assert(relay, res, now) {
		return p.fail("in-response-to", errors.New("the sign-in was answered twice"))
	}
	return nil
}

// verify runs the library's checks and Tobby's own on one response.
func (p *samlProvider) verify(sp *saml.ServiceProvider, raw []byte, requestID string, now time.Time) (*Result, error) {
	assertion, err := sp.ParseXMLResponse(raw, []string{requestID}, p.acs)
	if err != nil {
		// The library's public message is deliberately vague; the private
		// cause is what the log needs (never shown to the person).
		var ire *saml.InvalidResponseError
		if errors.As(err, &ire) && ire.PrivateErr != nil {
			err = ire.PrivateErr
		}
		return nil, p.fail(samlCheck(err), err)
	}
	if err := requireDestination(raw, p.acs.String()); err != nil {
		return nil, p.fail("destination", err)
	}
	if err := p.checkWindow(assertion, now); err != nil {
		return nil, p.fail("clock", err)
	}
	if assertion.Subject == nil || assertion.Subject.NameID == nil || assertion.Subject.NameID.Value == "" {
		return nil, p.fail("subject", errors.New("the assertion names nobody"))
	}
	name := "saml:" + assertion.Subject.NameID.Value
	groups := samlGroups(assertion, p.cfg.GroupsAttribute)
	role := mapRoles(&p.cfg.Roles, groups)
	if role == "" {
		return nil, noRole(p.name, name, groups)
	}
	return &Result{Name: name, Role: role, Method: auth.MethodSAML, Groups: groups}, nil
}

// samlCheck names the refused check from the library's classification.
func samlCheck(err error) string {
	for _, c := range []struct {
		kind error
		name string
	}{
		{saml.ErrAssertionReplayed, "replay"},
		{errReplayCacheFull, "replay"},
		{saml.ErrRequestIDMismatch, "in-response-to"},
		{saml.ErrAudienceMismatch, "audience"},
		{saml.ErrDestinationMismatch, "destination"},
		{saml.ErrIssuerMismatch, "issuer"},
		{saml.ErrExpired, "clock"},
		{saml.ErrNotYetValid, "clock"},
		{saml.ErrInvalidSignature, "signature"},
		{saml.ErrUntrustedCertificate, "signature"},
		{saml.ErrMalformedMessage, "malformed"},
		{saml.ErrMissingElement, "malformed"},
	} {
		if errors.Is(err, c.kind) {
			return c.name
		}
	}
	// An unsigned response surfaces as goxmldsig's unexported "signature
	// element not present", which no sentinel classifies.
	if strings.Contains(err.Error(), "signature") {
		return "signature"
	}
	return "response"
}

// requireDestination refuses a response without a Destination naming the
// ACS. The library checks a Destination that is present, and requires one
// on a signed Response; Tobby requires it always.
func requireDestination(raw []byte, acs string) error {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return err
	}
	root := doc.Root()
	if root == nil {
		return errors.New("no root element")
	}
	if got := root.SelectAttrValue("Destination", ""); got != acs {
		return fmt.Errorf("the response Destination %q is not this instance's ACS", got)
	}
	return nil
}

// checkWindow enforces auth.saml.clockSkew on the assertion's times.
func (p *samlProvider) checkWindow(a *saml.Assertion, now time.Time) error {
	skew := p.skew
	if a.IssueInstant.After(now.Add(skew)) {
		return fmt.Errorf("issued %s in the future, beyond the %s tolerance", a.IssueInstant.Sub(now), skew)
	}
	if now.Sub(a.IssueInstant) > issueDelay+skew {
		return fmt.Errorf("issued %s ago, beyond %s plus the %s tolerance", now.Sub(a.IssueInstant), issueDelay, skew)
	}
	if c := a.Conditions; c != nil {
		if !c.NotBefore.IsZero() && c.NotBefore.After(now.Add(skew)) {
			return errors.New("the conditions are not yet valid")
		}
		if !c.NotOnOrAfter.IsZero() && !now.Before(c.NotOnOrAfter.Add(skew)) {
			return errors.New("the conditions have expired")
		}
	}
	if a.Subject != nil {
		for _, sc := range a.Subject.SubjectConfirmations {
			if d := sc.SubjectConfirmationData; d != nil && !d.NotOnOrAfter.IsZero() && now.Before(d.NotOnOrAfter.Add(skew)) {
				return nil
			}
		}
	}
	return errors.New("no subject confirmation is valid now")
}

// samlGroups reads the groups attribute (by Name, else FriendlyName),
// bounded.
func samlGroups(a *saml.Assertion, attr string) []string {
	if attr == "" {
		attr = "groups"
	}
	var out []string
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			if at.Name != attr && at.FriendlyName != attr {
				continue
			}
			for _, v := range at.Values {
				out = append(out, v.Value)
				if len(out) == maxGroups {
					return out
				}
			}
		}
	}
	return out
}

// FinishSAML binds a verified response to the browser: relay is the
// sign-in, binding the BindingCookie value. Only then is the identity
// returned for a session.
func (s *Service) FinishSAML(relay, binding string) (*Result, error) {
	p := s.saml
	if p == nil {
		return nil, errors.New("federation: SAML is not enabled")
	}
	e, ok := s.pending.take(relay, s.now())
	if !ok || e.method != auth.MethodSAML || e.asserted == nil {
		return nil, p.fail("in-response-to", errors.New("no verified response for this sign-in"))
	}
	if !e.bound(binding) {
		return nil, p.fail("binding", errors.New("the response did not come back to the browser that started the sign-in"))
	}
	return e.asserted, nil
}

// SAMLMetadata returns this service provider's descriptor.
func (s *Service) SAMLMetadata() ([]byte, error) {
	if s.saml == nil {
		return nil, errors.New("federation: SAML is not enabled")
	}
	sp := s.saml.current()
	if sp == nil {
		// The descriptor does not depend on the IdP: build it from an SP
		// with no IdP metadata yet (a metadata URL not fetched so far).
		sp = &saml.ServiceProvider{EntityID: s.saml.cfg.EntityID, Key: s.saml.key, Certificate: s.saml.cert,
			AcsURL: s.saml.acs, MetadataURL: s.saml.metadata,
			AuthnNameIDFormat:                saml.PersistentNameIDFormat,
			AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding}}
	}
	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), bytes.TrimSpace(out)...), nil
}
