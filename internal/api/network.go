// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Network posture endpoints (FR-060): the strict mirror of the
// /admin/network screen (FR-061, FR-082). GET reports what the listener
// presents and how the instance reaches out; PUT replaces the listener's
// certificate pair.
//
// What the API is allowed to expose is a deliberate, narrow decision
// (NFR-015), and it is the reason this file reads the way it does:
//
//   - The CERTIFICATE is public by construction. Its fingerprint, subject,
//     issuer, SANs and validity are handed to every client that completes
//     a handshake, so returning them over an authenticated admin endpoint
//     reveals nothing a port scan would not.
//   - The PRIVATE KEY is never returned, in any form. Not its bytes, not
//     its length, not a digest of it. A digest would be a stable oracle
//     against a candidate key, and "a hash is not the secret" is exactly
//     the reasoning that leaks secrets. There is no accessor for it
//     anywhere in tlsadmin, so this endpoint could not return it even by
//     mistake.
//   - The PROXY is reported by URL and by the FACT that credentials exist
//     — never by their value. netx holds the credential-free URL and the
//     credentials separately for precisely this reason (FR-080).
//   - The configured PATHS are configuration, not secrets: they name what
//     an administrator must edit, and they already appear in the startup
//     log. The key path is reported; the key is not.
//
// PUT consumes key material and returns only the new fingerprint.

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/tlsadmin"
)

// maxCertRequest bounds the PUT body: two PEM documents plus the JSON
// envelope.
const maxCertRequest = 2*tlsadmin.MaxPEMBytes + (8 << 10)

// NetworkOptions is what the network endpoints read from the instance.
type NetworkOptions struct {
	// Cert is the listener's certificate, nil on a plain-HTTP instance.
	Cert tlsadmin.ServerCert
	// CertFile and KeyFile are the configured pair — the only paths netx
	// re-reads, and therefore the only ones a replacement can write to.
	CertFile string
	KeyFile  string
	// Egress is the outbound transport, reported through its printable
	// accessors only.
	Egress tlsadmin.Egress
	// Now injects the clock for the expiry check on a submitted pair.
	Now func() time.Time
}

// RegisterNetwork mounts the posture and replacement endpoints. Admin on
// both, like the screen: the report reveals the instance's own identity
// and its outbound path, and the replacement decides what every client
// authenticates against (ADR-0009).
func RegisterNetwork(a *API, o *NetworkOptions) {
	n := &networkAPI{api: a, opts: *o}
	if n.opts.Now == nil {
		n.opts.Now = time.Now
	}
	a.Handle("GET /api/v1/network", a.RequireRole(auth.RoleAdmin, n.get))
	a.Handle("PUT /api/v1/network/certificate", a.RequireRole(auth.RoleAdmin, n.replace))
}

type networkAPI struct {
	api  *API
	opts NetworkOptions
}

// networkResponse is the posture document.
type networkResponse struct {
	// TLS is false on a plain-HTTP listener — reported rather than
	// implied by a missing certificate.
	TLS bool `json:"tls"`
	// Certificate is the public identity of what is served, absent when
	// the instance serves in the clear.
	Certificate *tlsadmin.Certificate `json:"certificate,omitempty"`
	// CertificateFile and KeyFile are the configured paths. The key PATH
	// is configuration; the key is not exposed anywhere.
	CertificateFile string `json:"certificate_file,omitempty"`
	KeyFile         string `json:"key_file,omitempty"`
	// Replaceable reports whether PUT would be accepted at all: false on
	// the self-signed fallback, which has no configured pair to write.
	Replaceable bool `json:"replaceable"`
	// Egress is the outbound posture (FR-080, FR-081).
	Egress *tlsadmin.Outbound `json:"egress,omitempty"`
}

// get serves GET /api/v1/network.
func (n *networkAPI) get(w http.ResponseWriter, r *http.Request) {
	cert, err := tlsadmin.Describe(n.opts.Cert)
	if err != nil {
		n.problem(w, r, err)
		return
	}
	n.api.JSON(w, http.StatusOK, networkResponse{
		TLS:             n.opts.Cert != nil,
		Certificate:     cert,
		CertificateFile: n.opts.CertFile,
		KeyFile:         n.opts.KeyFile,
		Replaceable:     n.opts.Cert != nil && n.opts.CertFile != "" && n.opts.KeyFile != "",
		Egress:          tlsadmin.DescribeEgress(n.opts.Egress),
	})
}

// certificateRequest carries the replacement pair. Both members are
// consumed and forgotten; neither is echoed by any response.
type certificateRequest struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// certificateResponse reports the certificate now served. Deliberately
// the certificate's identity only — there is nothing about the key to
// report, and inventing a field for it is how one gets reported later.
type certificateResponse struct {
	Certificate *tlsadmin.Certificate `json:"certificate"`
}

// replace serves PUT /api/v1/network/certificate (FR-082): the API mirror
// of the screen's replacement. tlsadmin validates the pair in full before
// writing anything and installs each file atomically, so a refused pair
// leaves the listener untouched. Audited either way (FR-094).
func (n *networkAPI) replace(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	var req certificateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxCertRequest)).Decode(&req); err != nil {
		n.audit(r, id.Name, "", audit.OutcomeFailure)
		n.api.Problem(w, r, taxonomy.New(taxonomy.CodeServerCertReplace, taxonomy.Params{
			"detail": "the request body is not a certificate/key document: " + err.Error(),
		}))
		return
	}
	cert, err := tlsadmin.Replace(n.opts.CertFile, n.opts.KeyFile,
		[]byte(req.Certificate), []byte(req.Key), n.opts.Now())
	if err != nil {
		n.audit(r, id.Name, "", audit.OutcomeFailure)
		n.problem(w, r, err)
		return
	}
	// The fingerprint is the target: the one identifier that says which
	// certificate this instance presents, and the only value derived from
	// the submitted material that is safe to record.
	n.audit(r, id.Name, cert.Fingerprint, audit.OutcomeSuccess)
	n.api.JSON(w, http.StatusOK, certificateResponse{Certificate: cert})
}

func (n *networkAPI) audit(r *http.Request, actor, target, outcome string) {
	audit.Log(r.Context(), n.api.logger, &audit.Event{
		Actor: actor, Action: audit.ActionServerCertReplace, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// problem renders a tlsadmin failure as its catalog entry.
func (n *networkAPI) problem(w http.ResponseWriter, r *http.Request, err error) {
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	n.api.Problem(w, r, te)
}
