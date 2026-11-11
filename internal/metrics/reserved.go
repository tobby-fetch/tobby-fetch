// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

// Reserved metric families of milestone 6 (FR-091, ADR-0012).
//
// The names, types and labels below are reserved at the opening of the
// milestone so that its three tracks plug into one documented surface
// instead of inventing three. They are deliberately NOT registered yet:
// this registry's rule is that a family exists from the first scrape with
// a TRUE value, and a database-age gauge reading 0 before anything
// measures it would say "perfectly fresh" — the opposite of the truth.
// Each track registers its families in New, fed, when its feature lands,
// and adds them to the published table (reference/metrics-logs).
//
// Labels are closed sets, never an attacker-supplied string: the
// cardinality rule of PolicyRejections holds for every family here. The
// one exception, "scope", takes the names of the operator's own declared
// trust scopes — bounded by configuration.
const (
	// Track A — vulnerability scanning (6.1, 6.2, R-13).

	// MetricScanItems counts scanned items by verdict:
	// counter, label verdict = pass | warn | block | not-scanned.
	MetricScanItems = "tobby_scan_items_total"
	// MetricScanFindings counts findings by severity:
	// counter, label severity = UNKNOWN | LOW | MEDIUM | HIGH | CRITICAL.
	MetricScanFindings = "tobby_scan_findings_total"
	// MetricScanPolicyBlocking is 1 when scan.mode is block, 0 when the
	// policy is advisory — the advisory banner, as a metric: gauge.
	MetricScanPolicyBlocking = "tobby_scan_policy_blocking"
	// MetricScanScannerAvailable is 1 when a verified scanner binary is
	// usable, 0 in the degraded mode: gauge.
	MetricScanScannerAvailable = "tobby_scan_scanner_available"
	// MetricScanDBPresent is 1 when a vulnerability database is present,
	// 0 in the absent-database degraded mode: gauge.
	MetricScanDBPresent = "tobby_scan_db_present"
	// MetricScanDBAge is the database age, from the build timestamp it
	// records (R-13): gauge, seconds. Registered only with a database to
	// measure; MetricScanDBPresent tells "absent" from "fresh".
	MetricScanDBAge = "tobby_scan_db_age_seconds"
	// MetricScanDBStale is 1 while the age exceeds scan.maxDbAge: gauge,
	// so clearing it is as visible as raising it (the R-33 rule).
	MetricScanDBStale = "tobby_scan_db_stale"

	// Track B — authentication hardening and federation (R-14, 6.3, R-20).

	// MetricAuthAttempts counts sign-in and credential checks:
	// counter, labels method = basic | token | oidc | saml and
	// outcome = success | failure | expired.
	MetricAuthAttempts = "tobby_auth_attempts_total"
	// MetricAuthSessionsExpired counts UI sessions ended by expiry:
	// counter, label reason = idle | lifetime.
	MetricAuthSessionsExpired = "tobby_auth_sessions_expired_total"
	// MetricAuthIDPReachable is 1 while the configured identity provider
	// answers, 0 while it does not: gauge, label method = oidc | saml.
	MetricAuthIDPReachable = "tobby_auth_idp_reachable"
	// MetricAuthLocalUnderFederation counts local sign-ins while a
	// federated method is enabled — the break-glass signal of FR-078:
	// counter.
	MetricAuthLocalUnderFederation = "tobby_auth_local_logins_under_federation_total"

	// Track C — on-demand integrity and reduced assurance (R-31, R-22).

	// MetricIntegrityVerifications counts on-demand store verifications:
	// counter, label verdict = clean | defects.
	MetricIntegrityVerifications = "tobby_integrity_verifications_total"
	// MetricIntegrityLastVerification is the end time of the last
	// completed on-demand verification: gauge, Unix seconds.
	MetricIntegrityLastVerification = "tobby_integrity_last_verification_timestamp_seconds"
	// MetricAssuranceReduced is the number of recipes currently held
	// under reduced assurance (FR-037): gauge, label scope = the declared
	// trust scope that admitted them.
	MetricAssuranceReduced = "tobby_assurance_reduced_recipes"
)

// ReservedFamilies lists every reserved name, for the test that keeps
// them distinct from what is registered and the tracks honest about
// moving a name from here to New.
func ReservedFamilies() []string {
	return []string{
		MetricScanItems, MetricScanFindings, MetricScanPolicyBlocking,
		MetricScanScannerAvailable, MetricScanDBPresent, MetricScanDBAge, MetricScanDBStale,
		MetricAuthAttempts, MetricAuthSessionsExpired, MetricAuthIDPReachable, MetricAuthLocalUnderFederation,
		MetricIntegrityVerifications, MetricIntegrityLastVerification, MetricAssuranceReduced,
	}
}
