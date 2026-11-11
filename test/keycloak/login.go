// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package keycloak

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// maxPage bounds how much of a Keycloak page the helpers read.
const maxPage = 1 << 20

// LoginOIDC plays the browser of an authorization-code flow: it opens
// authURL (an authorization request built by the code under test, PKCE
// challenge included), submits the Keycloak login form as u, and returns
// the redirect the browser would have followed — the redirect URI carrying
// code and state, or carrying error and error_description when Keycloak
// refused the request. The redirect itself is never followed.
func (f *Fixture) LoginOIDC(ctx context.Context, authURL string, u User) (*url.URL, error) {
	c, err := browser()
	if err != nil {
		return nil, err
	}
	resp, cur, err := f.signIn(ctx, c, authURL, u)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil, fmt.Errorf("login as %q ended on HTTP %d with no redirect: %s", u.Username, resp.StatusCode, pageText(resp.Body))
	}
	return cur.Parse(loc)
}

// SAMLPost is the auto-submitted form of the HTTP-POST binding: what the
// browser would POST to the SP's assertion consumer service.
type SAMLPost struct {
	// Action is the form's target, the ACS URL.
	Action string
	// SAMLResponse is the base64 document, as sent on the wire.
	SAMLResponse string
	// RelayState is echoed back from the request, when there was one.
	RelayState string
}

// XML returns the decoded SAMLResponse document.
func (p SAMLPost) XML() ([]byte, error) {
	return base64.StdEncoding.DecodeString(p.SAMLResponse)
}

// LoginSAML plays the browser of an SP-initiated SAML login: it opens
// ssoURL (a redirect-binding AuthnRequest, see [Fixture.SAMLAuthnRequestURL]
// for a ready-made one), submits the Keycloak login form as u, and returns
// the POST the browser would have sent to the ACS. It is never sent.
func (f *Fixture) LoginSAML(ctx context.Context, ssoURL string, u User) (SAMLPost, error) {
	c, err := browser()
	if err != nil {
		return SAMLPost{}, err
	}
	resp, _, err := f.signIn(ctx, c, ssoURL, u)
	if err != nil {
		return SAMLPost{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return SAMLPost{}, err
	}
	form, err := findForm(body, func(v url.Values, _ string) bool { return v.Has("SAMLResponse") })
	if err != nil {
		return SAMLPost{}, fmt.Errorf("login as %q returned no SAML response (HTTP %d): %s", u.Username, resp.StatusCode, pageText(bytes.NewReader(body)))
	}
	return SAMLPost{
		Action:       form.action,
		SAMLResponse: form.values.Get("SAMLResponse"),
		RelayState:   form.values.Get("RelayState"),
	}, nil
}

// SAMLAuthnRequestURL builds a redirect-binding AuthnRequest to the
// realm's SSO endpoint, issued by the fixture's SP identity, asking for a
// persistent NameID. It exists so the helper can be proved on its own; the
// SP under test builds its own request.
func (f *Fixture) SAMLAuthnRequestURL(requestID, relayState string) (string, error) {
	xml := fmt.Sprintf(`<samlp:AuthnRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" `+
		`xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID=%q Version="2.0" IssueInstant=%q `+
		`Destination=%q AssertionConsumerServiceURL=%q ProtocolBinding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST">`+
		`<saml:Issuer>%s</saml:Issuer>`+
		`<samlp:NameIDPolicy Format="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent" AllowCreate="true"/>`+
		`</samlp:AuthnRequest>`,
		requestID, time.Now().UTC().Format(time.RFC3339), f.SAMLSSOURL, f.Options.SAMLACSURL, f.Options.SAMLEntityID)

	var buf bytes.Buffer
	zw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return "", err
	}
	if _, err := zw.Write([]byte(xml)); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	q := url.Values{"SAMLRequest": {base64.StdEncoding.EncodeToString(buf.Bytes())}}
	if relayState != "" {
		q.Set("RelayState", relayState)
	}
	return f.SAMLSSOURL + "?" + q.Encode(), nil
}

// signIn opens startURL, expects the Keycloak login form, submits it as u
// and follows redirects that stay on Keycloak. The response it returns is
// the first that is not a redirect inside the server: either a redirect out
// (OIDC), or a page (SAML auto-post form, or Keycloak's error page).
func (f *Fixture) signIn(ctx context.Context, c *http.Client, startURL string, u User) (*http.Response, *url.URL, error) {
	resp, cur, err := f.follow(ctx, c, startURL)
	if err != nil {
		return nil, nil, err
	}
	if resp.Header.Get("Location") != "" { // already redirected out: nothing to sign in to
		return resp, cur, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	_ = resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	form, err := findForm(body, func(_ url.Values, id string) bool { return id == "kc-form-login" })
	if err != nil {
		return nil, nil, fmt.Errorf("%w (HTTP %d): %s", errNoLoginForm, resp.StatusCode, pageText(bytes.NewReader(body)))
	}
	form.values.Set("username", u.Username)
	form.values.Set("password", u.Password)
	target, err := cur.Parse(form.action)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(form.values.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	loc := post.Header.Get("Location")
	if post.StatusCode < 300 || post.StatusCode > 399 || loc == "" {
		return post, target, nil
	}
	_ = post.Body.Close()
	next, err := target.Parse(loc)
	if err != nil {
		return nil, nil, err
	}
	if !f.sameServer(next) {
		return &http.Response{StatusCode: post.StatusCode, Header: http.Header{"Location": {next.String()}}, Body: http.NoBody}, target, nil
	}
	return f.follow(ctx, c, next.String())
}

type parsedForm struct {
	action string
	values url.Values
}

// findForm returns the first <form> of the page for which match is true,
// with every named <input> of it (hidden fields included) as its values.
func findForm(page []byte, match func(values url.Values, id string) bool) (parsedForm, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return parsedForm{}, err
	}
	var found *parsedForm
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "form" {
			f := parsedForm{values: url.Values{}}
			var id string
			for _, a := range n.Attr {
				switch a.Key {
				case "action":
					f.action = a.Val
				case "id":
					id = a.Val
				}
			}
			collectInputs(n, f.values)
			if match(f.values, id) {
				found = &f
				return
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	if found == nil {
		return parsedForm{}, errors.New("no matching form in the page")
	}
	return *found, nil
}

func collectInputs(n *html.Node, into url.Values) {
	if n.Type == html.ElementNode && n.Data == "input" {
		var name, value string
		for _, a := range n.Attr {
			switch a.Key {
			case "name":
				name = a.Val
			case "value":
				value = a.Val
			}
		}
		if name != "" {
			into.Set(name, value)
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		collectInputs(ch, into)
	}
}

// pageText is the visible text of a page, squeezed and truncated: enough
// to read Keycloak's own explanation of a refusal in a test failure.
func pageText(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, maxPage))
	if err != nil {
		return err.Error()
	}
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return string(b[:min(len(b), 200)])
	}
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
			sb.WriteByte(' ')
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	s := strings.Join(strings.Fields(sb.String()), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
