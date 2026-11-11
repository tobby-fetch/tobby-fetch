// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func family(t *testing.T, reg *Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func series(f *dto.MetricFamily, labels map[string]string) *dto.Metric {
	for _, m := range f.GetMetric() {
		match := true
		for _, l := range m.GetLabel() {
			if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
				match = false
			}
		}
		if match {
			return m
		}
	}
	return nil
}

// TestAuthFamilies locks the R-14/R-20 metric surface: the counters exist
// from the first scrape over their closed label sets, values outside the
// sets are dropped instead of becoming series, and the identity-provider
// gauge appears only once a provider was probed.
func TestAuthFamilies(t *testing.T) {
	reg := New()
	a := RegisterAuth(reg)

	attempts := family(t, reg, MetricAuthAttempts)
	if attempts == nil || len(attempts.GetMetric()) != 12 {
		t.Fatalf("%s: %v, want 4 methods × 3 outcomes from the first scrape", MetricAuthAttempts, attempts)
	}
	if f := family(t, reg, MetricAuthSessionsExpired); f == nil || len(f.GetMetric()) != 2 {
		t.Errorf("%s: want idle and lifetime from the first scrape", MetricAuthSessionsExpired)
	}
	if f := family(t, reg, MetricAuthLocalUnderFederation); f == nil {
		t.Errorf("%s missing from the first scrape", MetricAuthLocalUnderFederation)
	}
	if f := family(t, reg, MetricAuthIDPReachable); f != nil {
		t.Errorf("%s exposed before any probe: %v", MetricAuthIDPReachable, f)
	}

	a.Attempt("token", "expired")
	a.Attempt("token", "expired")
	a.Attempt("ldap", "success")   // not a method: dropped
	a.Attempt("basic", "whatever") // not an outcome: dropped
	a.SessionExpired("idle")
	a.SessionExpired("bogus")
	a.LocalUnderFederation()
	a.IDPReachable("oidc", false)
	a.IDPReachable("kerberos", true)

	m := series(family(t, reg, MetricAuthAttempts), map[string]string{"method": "token", "outcome": "expired"})
	if m.GetCounter().GetValue() != 2 {
		t.Errorf("token/expired = %v, want 2", m.GetCounter().GetValue())
	}
	if n := len(family(t, reg, MetricAuthAttempts).GetMetric()); n != 12 {
		t.Errorf("attempt series = %d after out-of-set values, want still 12", n)
	}
	if n := len(family(t, reg, MetricAuthSessionsExpired).GetMetric()); n != 2 {
		t.Errorf("expiry series = %d, want 2", n)
	}
	idp := family(t, reg, MetricAuthIDPReachable)
	if idp == nil || len(idp.GetMetric()) != 1 || idp.GetMetric()[0].GetGauge().GetValue() != 0 {
		t.Errorf("idp gauge = %v, want one oidc series at 0", idp)
	}
	a.IDPReachable("oidc", true)
	if v := family(t, reg, MetricAuthIDPReachable).GetMetric()[0].GetGauge().GetValue(); v != 1 {
		t.Errorf("idp gauge after recovery = %v, want 1", v)
	}
}
