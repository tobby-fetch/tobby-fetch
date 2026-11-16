// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"fmt"
	"io"
	"net/http"
	"unicode"
	"unicode/utf8"
)

// maxIDPResponse bounds every response body an identity provider sends
// this instance (NFR-023): discovery documents, JWKS, token answers, SAML
// metadata. The libraries that read them (go-oidc, oauth2, crewjam/saml)
// do not all bound what they read — go-oidc reads discovery and JWKS with
// a bare io.ReadAll and copies a non-200 body into its error — so the
// bound is enforced on the transport, beneath all of them. 2 MiB is an
// order of magnitude over the largest real JWKS or metadata document.
const maxIDPResponse = 2 << 20

// boundedTransport caps the body of every response it returns.
type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &boundedBody{rc: resp.Body, left: maxIDPResponse}
	return resp, nil
}

// boundedBody fails — rather than silently truncates — a body over the
// bound: a truncated JSON or XML document must not parse into something.
type boundedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One byte past the bound decides between "exactly the bound" and
		// "over it".
		var one [1]byte
		if n, _ := b.rc.Read(one[:]); n > 0 {
			return 0, fmt.Errorf("federation: the identity provider's response exceeds %d bytes", maxIDPResponse)
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.rc.Close() }

// validSubject is the grammar of the identity key a provider asserts —
// the OIDC subject, the SAML NameID — before it becomes a session name
// and an audit actor (NFR-023): OpenID Connect Core §2 caps "sub" at 255
// ASCII characters; the same bound, and no control character, is applied
// to both methods.
func validSubject(s string) bool {
	if len(s) > 255 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
