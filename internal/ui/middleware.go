// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// sessionKey scopes the UI session in a request context.
type sessionKey struct{}

func withSession(ctx context.Context, s *auth.Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) (*auth.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(*auth.Session)
	return s, ok
}

// cookieSecure reports whether cookies must carry the Secure attribute:
// TLS on this listener, or TLS terminated by the proxy in front of it,
// which the operator declares with server.secureCookies — the listener
// cannot see that hop, and X-Forwarded-Proto is deliberately not trusted
// (spoofable, like every forwarding header; same stance as FR-094
// origins). NFR-015: a session cookie must never travel in the clear.
//
// The __Host- name prefix was considered and set aside: it would make
// the cookie name vary with the deployment (the prefix requires Secure,
// which a plain-HTTP lab instance cannot honor), and auth.SessionCookie
// is a cross-package constant the API's session fallback reads too. The
// attributes the prefix enforces — Secure, Path=/, no Domain — are all
// set explicitly below, so the prefix would add its name constraint
// without adding protection.
func (u *UI) cookieSecure(r *http.Request) bool {
	return r.TLS != nil || u.secureCookies
}

// setSessionCookie writes the session cookie: HttpOnly, SameSite=Lax,
// Secure per cookieSecure (NFR-015).
func (u *UI) setSessionCookie(w http.ResponseWriter, r *http.Request, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    id,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   u.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the session cookie.
func (u *UI) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   u.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// RequireSession gates every UI route behind a live session (never exposed
// open — R-01). A browser is redirected to /login?next=<page>; an htmx
// request gets 401 + HX-Redirect so the fragment never fills with the
// login page (ADR-0015 §6). Under the FR-075 override everything passes as
// the explicit anonymous identity. Mutating requests must then carry the
// session's CSRF token (NFR-012).
func (u *UI) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u.authn.Disabled {
			ctx := auth.WithIdentity(r.Context(), auth.AnonymousIdentity)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		sess, ok := u.sessionOf(r)
		if !ok {
			u.toLogin(w, r)
			return
		}
		ctx := withSession(r.Context(), &sess)
		ctx = auth.WithIdentity(ctx, auth.Identity{Name: sess.Account, Role: sess.Role})
		r = r.WithContext(ctx)

		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			if !sess.CheckCSRF(r.PostFormValue("csrf")) {
				u.render.Error(w, r, taxonomy.New(taxonomy.CodeCSRF, nil))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRole gates a handler behind a minimum role; the refusal names the
// role (TBY-AUTH-003) and is audited.
func (u *UI) RequireRole(floor auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.IdentityFrom(r.Context())
		if !id.Role.AtLeast(floor) {
			audit.Log(r.Context(), u.logger, &audit.Event{
				Actor:   id.Name,
				Action:  "ui.access",
				Target:  r.URL.Path,
				Outcome: audit.OutcomeDenied,
				Origin:  auth.ClientOrigin(r),
			})
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeRoleDenied,
				taxonomy.Params{"role": string(floor)}))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionOf resolves the request's session cookie.
func (u *UI) sessionOf(r *http.Request) (auth.Session, bool) {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil || c.Value == "" {
		return auth.Session{}, false
	}
	return u.authn.Sessions.Get(c.Value, u.now())
}

// toLogin sends an unauthenticated request to the login page, preserving
// the destination. htmx gets 401 + HX-Redirect (ADR-0015 §6).
func (u *UI) toLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Path
	if r.URL.RawQuery != "" {
		next += "?" + r.URL.RawQuery
	}
	target := "/login?next=" + url.QueryEscape(next)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
