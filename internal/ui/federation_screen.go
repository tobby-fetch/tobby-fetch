// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Federated sign-in (FR-070, FR-071, FR-078): the sign-in page's provider
// buttons and the round-trip endpoints under the "/auth/" prefix the
// listener reserves for them. The federation package verifies; this file
// owns everything the browser sees — the binding cookie, the session, the
// redirect through the anti-open-redirect check (B-033), the audit record
// and the page that explains a refusal with its code.
//
// A federated session is an ordinary Tobby session (auth.Sessions), with
// its method recorded: the idle timeout, the CSRF token and the role
// floors apply to it exactly as to a local one.

package ui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/federation"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// loginProvider is one federated button of the sign-in page.
type loginProvider struct {
	Name string
	// Href starts the round trip, carrying the sanitized destination.
	Href string
	// Unavailable is the TBY-AUTH-016 block naming the provider while it
	// is known unreachable (FR-078). The button stays: it retries.
	Unavailable *ErrView
}

// loginProviders lists the enabled federated methods for the page.
func (u *UI) loginProviders(r *http.Request, next string) []loginProvider {
	var out []loginProvider
	for _, p := range u.federation.Providers() {
		lp := loginProvider{Name: p.Name, Href: p.LoginPath + "?next=" + url.QueryEscape(next)}
		if !p.Available {
			lp.Unavailable = errView(requestLang(r), taxonomy.New(taxonomy.CodeIDPUnreachable,
				taxonomy.Params{"provider": p.Name}))
		}
		out = append(out, lp)
	}
	return out
}

// MountFederation registers the federated round-trip routes. They live
// under "/auth/", outside the UI's own namespace (ADR-0015: the prefix is
// reserved on the listener for exactly these callbacks), and are public:
// they are how one signs in. Mounted only when a federated method is
// enabled — an instance without federation answers 404 there, like any
// other unknown machine path.
func (u *UI) MountFederation(rt Router) {
	if !u.federation.Enabled() {
		return
	}
	mux := securedRouter{r: rt}
	mux.HandleFunc("GET "+federation.PathOIDCLogin, u.oidcLogin)
	mux.HandleFunc("GET "+federation.PathOIDCCallback, u.oidcCallback)
	mux.HandleFunc("GET "+federation.PathSAMLLogin, u.samlLogin)
	mux.HandleFunc("POST "+federation.PathSAMLACS, u.samlACS)
	mux.HandleFunc("GET "+federation.PathSAMLFinish, u.samlFinish)
	mux.HandleFunc("GET "+federation.PathSAMLMetadata, u.samlMetadata)
}

// setBindingCookie hands the browser the binding secret of its sign-in
// in flight: HttpOnly, SameSite=Lax — sent on the provider's top-level
// redirect back, never readable by script — scoped to /auth/.
func (u *UI) setBindingCookie(w http.ResponseWriter, r *http.Request, secret string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite are set; Secure follows cookieSecure, as for the session cookie
		Name: federation.BindingCookie, Value: secret, Path: "/auth/",
		MaxAge: federation.BindingMaxAge, HttpOnly: true,
		Secure: u.cookieSecure(r), SameSite: http.SameSiteLaxMode,
	})
}

func (u *UI) clearBindingCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: same attributes as setBindingCookie; this one only expires it
		Name: federation.BindingCookie, Value: "", Path: "/auth/", MaxAge: -1,
		HttpOnly: true, Secure: u.cookieSecure(r), SameSite: http.SameSiteLaxMode,
	})
}

func bindingCookie(r *http.Request) string {
	c, err := r.Cookie(federation.BindingCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// oidcLogin serves GET /auth/oidc/login: send the browser to the provider.
func (u *UI) oidcLogin(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	target, secret, err := u.federation.BeginOIDC(r.Context(), next)
	if err != nil {
		u.federatedRefusal(w, r, auth.MethodOIDC, next, err)
		return
	}
	u.setBindingCookie(w, r, secret)
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // G710: the target is built by the federation package from the configured provider's own endpoint, never from request input
}

// oidcCallback serves GET /auth/oidc/callback.
func (u *UI) oidcCallback(w http.ResponseWriter, r *http.Request) {
	res, err := u.federation.CompleteOIDC(r.Context(), r.URL.Query(), bindingCookie(r))
	u.clearBindingCookie(w, r)
	if err != nil {
		u.federatedRefusal(w, r, auth.MethodOIDC, "/", err)
		return
	}
	u.openFederatedSession(w, r, res)
}

// openFederatedSession turns a verified identity into an ordinary Tobby
// session and lands on the destination the sign-in started from — which
// went through sanitizeNext at the start AND goes through it again here:
// the value round-tripped through server memory only, but the
// anti-open-redirect check is cheap and must hold on every redirect that
// leaves a sign-in (B-033).
func (u *UI) openFederatedSession(w http.ResponseWriter, r *http.Request, res *federation.Result) {
	sess := u.authn.Sessions.CreateNamed(res.Name, res.Display, res.Role, res.Method, u.now())
	u.setSessionCookie(w, r, sess.ID, sess.Expires)
	u.authn.Observe(res.Method, auth.OutcomeSuccess)
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: res.Name, ActorName: res.Display, Action: audit.ActionLogin, Target: string(res.Method),
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	u.logger.LogAttrs(r.Context(), slog.LevelInfo, "federated sign-in",
		slog.String("method", string(res.Method)), slog.String("identity", res.Name),
		slog.String("name", res.Display),
		slog.String("role", string(res.Role)), slog.Int("groups", len(res.Groups)))
	http.Redirect(w, r, sanitizeNext(res.Next), http.StatusSeeOther) //nolint:gosec // G710: the target went through sanitizeNext, which only keeps a same-host path (B-033)
}

// federatedRefusal answers a refused federated sign-in: the sign-in page
// with the code (TBY-AUTH-014, -015, -016 or -017) and the real status,
// the precise check in the log only, an audit record, and the local form
// still there (FR-078: federation down never blocks local sign-in).
func (u *UI) federatedRefusal(w http.ResponseWriter, r *http.Request, method auth.Method, next string, err error) {
	var f *federation.Failure
	if !errors.As(err, &f) {
		f = &federation.Failure{Code: taxonomy.CodeInternal, Provider: string(method), Check: "internal", Cause: err}
	}
	outcome := audit.OutcomeDenied
	if f.Code == taxonomy.CodeIDPUnreachable {
		outcome = audit.OutcomeFailure
	} else {
		u.authn.Observe(method, auth.OutcomeFailure)
	}
	actor := "unauthenticated"
	if f.Identity != "" {
		actor = f.Identity
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, ActorName: f.Display, Action: audit.ActionLogin, Target: string(method),
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
	u.logger.LogAttrs(r.Context(), slog.LevelWarn, "federated sign-in refused",
		slog.String("method", string(method)), slog.String("code", string(f.Code)),
		slog.String("check", f.Check), slog.String("error", errText(f.Cause)))
	ev := errView(requestLang(r), f.Taxonomy())
	u.renderLogin(w, r, ev.Status, next, ev)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// renderLogin renders the sign-in page with a status and an error block.
func (u *UI) renderLogin(w http.ResponseWriter, r *http.Request, status int, next string, failed *ErrView) {
	v := u.render.view(r, u.loginPageData(r, next, failed))
	u.render.render(w, r, "login", status, v)
}

// samlLogin serves GET /auth/saml/login: send the browser to the IdP with
// an AuthnRequest this instance will remember (SP-initiated only).
func (u *UI) samlLogin(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	target, secret, err := u.federation.BeginSAML(r.Context(), next)
	if err != nil {
		u.federatedRefusal(w, r, auth.MethodSAML, next, err)
		return
	}
	u.setBindingCookie(w, r, secret)
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // G710: the target is built by the federation package from the configured provider's own endpoint, never from request input
}

// samlACS serves POST /auth/saml/acs, the assertion consumer service.
// The POST comes from the IdP's auto-submitted form — cross-site, so the
// SameSite=Lax binding cookie is NOT on it: the response is verified here
// against the request its RelayState names, and the browser is sent on to
// the finish step, a top-level GET that carries the cookie and is where
// the binding is checked. No session is opened before that check.
func (u *UI) samlACS(w http.ResponseWriter, r *http.Request) {
	// Bounded before anything parses it (B-034): a SAML response is a few
	// kilobytes; the bound leaves room for large group lists.
	r.Body = http.MaxBytesReader(w, r.Body, federation.MaxSAMLBody)
	if err := r.ParseForm(); err != nil {
		u.federatedRefusal(w, r, auth.MethodSAML, "/", u.federation.SAMLBodyFailure(err))
		return
	}
	relay := r.PostForm.Get("RelayState")
	if err := u.federation.ConsumeSAML(r.Context(), r.PostForm.Get("SAMLResponse"), relay); err != nil {
		u.federatedRefusal(w, r, auth.MethodSAML, "/", err)
		return
	}
	http.Redirect(w, r, federation.PathSAMLFinish+"?relay="+url.QueryEscape(relay), http.StatusSeeOther)
}

// samlFinish serves GET /auth/saml/finish: bind the verified assertion to
// the browser that started the sign-in, then open the session.
func (u *UI) samlFinish(w http.ResponseWriter, r *http.Request) {
	res, err := u.federation.FinishSAML(r.URL.Query().Get("relay"), bindingCookie(r))
	u.clearBindingCookie(w, r)
	if err != nil {
		u.federatedRefusal(w, r, auth.MethodSAML, "/", err)
		return
	}
	u.openFederatedSession(w, r, res)
}

// samlMetadata serves GET /auth/saml/metadata: this service provider's
// descriptor, for onboarding it at the IdP (ADR-0009). Public material
// only — the entity ID, the ACS URL and the SP certificate.
func (u *UI) samlMetadata(w http.ResponseWriter, r *http.Request) {
	doc, err := u.federation.SAMLMetadata()
	if err != nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil).WithCause(err))
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(doc)
}

// federatedProviderName is the display name of the provider behind a
// federated session, for the account screen.
func (u *UI) federatedProviderName(m auth.Method) string {
	for _, p := range u.federation.Providers() {
		if p.Method == m {
			return p.Name
		}
	}
	return string(m)
}
