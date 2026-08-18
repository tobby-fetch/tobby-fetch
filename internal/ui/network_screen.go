// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Network posture screen (FR-082, FR-080, FR-081, UI-SPEC §6): what this
// instance presents to its clients, what it presents itself as on the way
// out, and the one thing an administrator can change here — the listener's
// certificate.
//
// FR-082 asks for certificate replacement "via configuration AND the admin
// UI (FR-062)". The configuration half already exists in internal/netx:
// the configured pair is re-read whenever it changes on disk, so the
// listener picks up a replacement without a restart. This screen is the
// other half, and it works WITH that mechanism rather than beside it — it
// writes the configured pair and lets netx notice. That is why a
// replacement can never take the instance down: netx keeps the previous
// certificate whenever a reload fails.
//
// Secret hygiene (NFR-015) shapes the whole screen. The private key is
// submitted as a FILE, never as a text field: a field is a value the
// server echoes back on re-render, and a re-rendered key is a key on
// screen, in the DOM, and in the browser's back/forward cache. Nothing
// here reads the key back, renders it, logs it, or fingerprints it — the
// only thing that ever comes out of the pair is the certificate's own
// public identity, which the listener hands to every client anyway.

package ui

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/tlsadmin"
)

// expiryWarning is how close to expiry a certificate starts being called
// out on screen. Thirty days is the usual renewal window; the point is
// that an operator learns it here rather than from a client's error.
const expiryWarning = 30 * 24 * time.Hour

// networkData feeds the /admin/network page.
type networkData struct {
	// Serving is false on an instance listening in the clear. A plain-HTTP
	// listener is a posture, and this screen is where it is said out loud.
	Serving bool
	// Cert is the certificate presented right now, nil when the instance
	// serves plain HTTP or the pair could not be read.
	Cert *certView
	// Egress is the outbound posture (FR-080, FR-081), nil when the
	// instance was wired without one.
	Egress *tlsadmin.Outbound
	// Replaceable reports that a configured pair exists to replace. False
	// on the self-signed fallback: the replacement form is then rendered
	// inert with the reason, rather than accepting a file nothing reads.
	Replaceable bool
	// CertFile and KeyFile are the configured paths — configuration, not
	// secrets, and the two settings the inert state has to name. The KEY
	// path is shown; the key is not, and has no accessor anywhere.
	CertFile string
	KeyFile  string
}

// certView decorates the served certificate for the template.
type certView struct {
	*tlsadmin.Certificate
	// Expired and ExpiringSoon are computed against the request clock so
	// the screen states the condition instead of leaving an operator to
	// compare two dates.
	Expired      bool
	ExpiringSoon bool
	// DaysLeft is the coarse countdown shown beside the expiry date.
	DaysLeft int
}

// adminNetwork serves GET /admin/network.
func (u *UI) adminNetwork(w http.ResponseWriter, r *http.Request) {
	d, err := u.networkScreenData()
	if err != nil {
		u.contentError(w, r, "admin-network", d, err)
		return
	}
	u.render.Page(w, r, "admin-network", d)
}

// networkScreenData snapshots the instance's network edges. A certificate
// that cannot be read is reported as an error over an otherwise complete
// screen: the outbound posture is still worth showing.
func (u *UI) networkScreenData() (*networkData, *taxonomy.Error) {
	d := &networkData{
		Serving:     u.serverCert != nil,
		Egress:      tlsadmin.DescribeEgress(u.egress),
		Replaceable: u.serverCert != nil && u.serverCertFile != "" && u.serverKeyFile != "",
		CertFile:    u.serverCertFile,
		KeyFile:     u.serverKeyFile,
	}
	cert, err := tlsadmin.Describe(u.serverCert)
	if err != nil {
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		return d, te
	}
	if cert != nil {
		d.Cert = u.decorateCert(cert)
	}
	return d, nil
}

// decorateCert computes the validity verdicts against the injected clock.
func (u *UI) decorateCert(c *tlsadmin.Certificate) *certView {
	now := u.now()
	left := c.NotAfter.Sub(now)
	return &certView{
		Certificate:  c,
		Expired:      c.ExpiredAt(now),
		ExpiringSoon: left > 0 && left <= expiryWarning,
		DaysLeft:     int(left / (24 * time.Hour)),
	}
}

// adminNetworkCertificate serves POST /admin/network/certificate
// (FR-082): install an administrator-supplied pair on a running instance.
//
// The two files are read into memory and handed to tlsadmin, which
// validates everything BEFORE touching the disk and installs each file by
// rename. A refused pair therefore leaves the listener exactly as it was,
// which is the property this screen must never lose. Audited either way
// (FR-094): what certificate an instance presents is a security-relevant
// setting, and a refused attempt is as much part of the trail as an
// accepted one.
func (u *UI) adminNetworkCertificate(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	certPEM, keyPEM, err := readCertPair(r)
	if err != nil {
		u.networkRefusal(w, r, id.Name, "", err)
		return
	}
	newCert, err := tlsadmin.Replace(u.serverCertFile, u.serverKeyFile, certPEM, keyPEM, u.now())
	if err != nil {
		u.networkRefusal(w, r, id.Name, "", err)
		return
	}
	// The target is the fingerprint now served: the one identifier that
	// says which certificate this instance presents, and the only thing
	// derived from the submitted material that is safe to record.
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionServerCertReplace, Target: newCert.Fingerprint,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})

	// Re-read through netx rather than trusting the bytes just written:
	// that read is what proves the listener actually adopted the pair,
	// and it is the same call every handshake makes.
	d, derr := u.networkScreenData()
	v := u.render.view(r, d)
	if derr != nil {
		v.Err = errView(v.Lang, derr)
	}
	v.Toasts = append(v.Toasts, v.T("network.toast_replaced", "Fingerprint", newCert.Fingerprint))
	u.render.render(w, r, "admin-network", http.StatusOK, v)
}

// networkRefusal audits the refusal and re-renders the screen with the
// taxonomized block. Nothing was written, so the screen still describes a
// serving instance.
func (u *UI) networkRefusal(w http.ResponseWriter, r *http.Request, actor, target string, err error) {
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, Action: audit.ActionServerCertReplace, Target: target,
		Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
	})
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	d, derr := u.networkScreenData()
	if derr != nil {
		te = derr
	}
	v := u.render.view(r, d)
	v.Err = errView(v.Lang, te)
	u.render.render(w, r, "admin-network", v.Err.Status, v)
}

// readCertPair reads the two uploaded documents.
//
// Files rather than text fields, on purpose: a submitted key must not
// travel through a value the server can echo back into the page.
//
// The anti-forgery check in the session gate has already parsed the
// multipart body by the time this runs, so the call below is what gives
// this handler access rather than what bounds it; the bound that matters
// is the per-document one in readUpload, which is what caps the material
// this handler actually holds.
func readCertPair(r *http.Request) (certPEM, keyPEM []byte, err error) {
	if err := r.ParseMultipartForm(2 * tlsadmin.MaxPEMBytes); err != nil {
		return nil, nil, taxonomy.New(taxonomy.CodeServerCertReplace, taxonomy.Params{
			"detail": "the submission is not a certificate/key upload: " + err.Error(),
		})
	}
	defer func() {
		// The parser may have spilled the upload to a temporary file:
		// leaving a copy of a private key in the system temp directory
		// would defeat everything else this handler is careful about.
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	certPEM, err = readUpload(r, "certificate")
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = readUpload(r, "key")
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// readUpload reads one bounded uploaded document.
func readUpload(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeServerCertReplace, taxonomy.Params{
			"detail": "no " + field + " file was submitted",
		})
	}
	defer func() { _ = f.Close() }()
	// One byte over the bound is read on purpose: it is what tells an
	// oversized document apart from one that exactly fills the budget.
	raw, err := io.ReadAll(io.LimitReader(f, tlsadmin.MaxPEMBytes+1))
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeServerCertReplace, taxonomy.Params{
			"detail": "the " + field + " file could not be read: " + err.Error(),
		})
	}
	return raw, nil
}
