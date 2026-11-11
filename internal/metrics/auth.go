// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Auth holds the authentication families of milestone 6 (R-14, 6.3,
// R-20), registered by RegisterAuth on the instance registry. It
// implements auth.Observer structurally — this package does not import
// auth, the dependency runs the other way at wiring time only.
type Auth struct {
	attempts             *prometheus.CounterVec
	sessionsExpired      *prometheus.CounterVec
	idpReachable         *prometheus.GaugeVec
	localUnderFederation prometheus.Counter
}

// The closed label sets of the authentication families (the cardinality
// rule of reserved.go: never an attacker-supplied string).
var (
	authMethods  = []string{"basic", "token", "oidc", "saml"}
	authOutcomes = []string{"success", "failure", "expired"}
	expiryReason = []string{"idle", "lifetime"}
)

// RegisterAuth registers the authentication families on reg and returns
// them. Every counter series of the closed label sets exists from the
// first scrape at zero — "no failure happened" must read differently from
// "this instance does not report failures". The identity-provider gauge
// is the exception, on purpose: a series appears per federated method
// only once the provider has been probed, because 0 before anything was
// measured would claim an outage nobody saw (the reserved.go rule).
func RegisterAuth(reg *Registry) *Auth {
	a := &Auth{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricAuthAttempts,
			Help: "Credential checks and sign-ins by method (basic, token, oidc, saml) and outcome (success, failure, expired) (R-14).",
		}, []string{"method", "outcome"}),
		sessionsExpired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricAuthSessionsExpired,
			Help: "UI sessions ended by expiry, by reason: idle (auth.session.idleTimeout) or lifetime (auth.sessionTTL) (R-14).",
		}, []string{"reason"}),
		idpReachable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: MetricAuthIDPReachable,
			Help: "1 while the configured identity provider answers, 0 while it does not, by federated method; absent until first probed (R-20).",
		}, []string{"method"}),
		localUnderFederation: prometheus.NewCounter(prometheus.CounterOpts{
			Name: MetricAuthLocalUnderFederation,
			Help: "Local-account sign-ins while a federated method is enabled — break-glass use (FR-078).",
		}),
	}
	for _, m := range authMethods {
		for _, o := range authOutcomes {
			a.attempts.WithLabelValues(m, o)
		}
	}
	for _, r := range expiryReason {
		a.sessionsExpired.WithLabelValues(r)
	}
	reg.MustRegister(a.attempts, a.sessionsExpired, a.idpReachable, a.localUnderFederation)
	return a
}

// Attempt counts one credential check or sign-in. Method and outcome come
// from closed sets; anything else is dropped rather than turned into a
// series.
func (a *Auth) Attempt(method, outcome string) {
	if !member(authMethods, method) || !member(authOutcomes, outcome) {
		return
	}
	a.attempts.WithLabelValues(method, outcome).Inc()
}

// SessionExpired counts one session ended by expiry.
func (a *Auth) SessionExpired(reason string) {
	if member(expiryReason, reason) {
		a.sessionsExpired.WithLabelValues(reason).Inc()
	}
}

// LocalUnderFederation counts one local sign-in under federation.
func (a *Auth) LocalUnderFederation() { a.localUnderFederation.Inc() }

// IDPReachable records the last probe of a federated method's provider.
func (a *Auth) IDPReachable(method string, up bool) {
	if method != "oidc" && method != "saml" {
		return
	}
	v := 0.0
	if up {
		v = 1
	}
	a.idpReachable.WithLabelValues(method).Set(v)
}

func member(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
