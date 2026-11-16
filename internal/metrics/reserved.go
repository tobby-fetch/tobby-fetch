// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

// Reserved metric families of milestones 6 and 7 (FR-091, ADR-0012).
//
// The names, types and labels below are reserved at the opening of the
// milestone so that its three tracks plug into one documented surface
// instead of inventing three. None was registered before it was fed:
// this registry's rule is that a family exists from the first scrape with
// a TRUE value, and a database-age gauge reading 0 before anything
// measures it would say "perfectly fresh" — the opposite of the truth.
// Each track registered its families, fed, when its feature landed,
// and added them to the published table (reference/metrics-logs).
//
// Labels are closed sets, never an attacker-supplied string: the
// cardinality rule of PolicyRejections holds for every family here. The
// one exception, "scope", takes the names of the operator's own declared
// trust scopes — bounded by configuration.
const (
	// Track A — vulnerability scanning (6.1, 6.2, R-13): registered in
	// New since they are fed — see scan.go.

	// Track B — authentication hardening and federation (R-14, 6.3, R-20).
	// Registered by RegisterAuth (auth.go) and no longer listed by
	// ReservedFamilies.

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

// Reserved metric families of milestone 7 (FR-091 amendment 2026-11-16,
// R-11; FR-096; FR-098; FR-031 amendment R-15). Same rule as above: none
// is registered before it is fed, and a track moves a name out of
// ReservedFamilies in the change that registers it. Timestamp gauges are
// absent until they have measured something — never 0.
const (
	// Track B — supervision kit (R-11): the minimum FR-091 names, and what
	// an alert on staleness needs.

	// MetricSyncRuns counts synchronization runs (mirror trigger or
	// passthrough cycle): counter, label outcome = success | failure.
	MetricSyncRuns = "tobby_sync_runs_total"
	// MetricSyncDuration is the duration of synchronization runs:
	// histogram, seconds, label outcome = success | failure.
	MetricSyncDuration = "tobby_sync_duration_seconds"
	// MetricSyncLastSuccess is the end time of the last successful
	// synchronization: gauge, Unix seconds.
	MetricSyncLastSuccess = "tobby_sync_last_success_timestamp_seconds"
	// MetricSyncIngredients is the per-status ingredient count of the last
	// run (FR-026): gauge, label status = new | outdated | up_to_date.
	MetricSyncIngredients = "tobby_sync_ingredients"
	// MetricServerCertificateExpiry is the NotAfter of the certificate the
	// listener presents (FR-082), self-signed fallback included: gauge,
	// Unix seconds; absent while the listener serves plain HTTP.
	MetricServerCertificateExpiry = "tobby_server_certificate_expiry_timestamp_seconds"

	// Track B — clock plausibility (R-32, FR-096).

	// MetricClockImplausible is 1 while the local clock was found earlier
	// than an anchor beyond the tolerance, 0 otherwise: gauge, label
	// anchor = build | medium.
	MetricClockImplausible = "tobby_clock_implausible"

	// Track B — diagnostic bundle (R-30, FR-095).

	// MetricDiagnosticsBundles counts diagnostic bundles: counter, label
	// outcome = written | refused (redaction failed closed) | failed.
	MetricDiagnosticsBundles = "tobby_diagnostics_bundles_total"

	// Track C — provenance and hardening (R-17, FR-098): registered by
	// RegisterUnmanaged and RegisterHardening (provenance.go) and no
	// longer listed by ReservedFamilies.

	// MetricStoreUnmanaged is the number of repositories not managed by a
	// verified recipe: gauge, label class = unit-import | seed.
	MetricStoreUnmanaged = "tobby_store_unmanaged_repositories"
	// MetricHardeningEnabled is 1 for each hardening switch set, 0
	// otherwise: gauge, label setting = disableUnitImport |
	// disableRegistryPush.
	MetricHardeningEnabled = "tobby_hardening_enabled"

	// Track C — scoped and bounded exceptions (R-15).

	// MetricScanExceptions is the number of exceptions in configuration,
	// VEX-born ones included: gauge, labels state = active | expired and
	// source = instance | retriever | vex.
	MetricScanExceptions = "tobby_scan_exceptions"
	// MetricScanReblocked counts items refused again because the exception
	// that admitted them expired: counter.
	MetricScanReblocked = "tobby_scan_reblocked_items_total"
)

// ReservedFamilies lists every reserved name, for the test that keeps
// them distinct from what is registered and the tracks honest about
// moving a name from here to New.
func ReservedFamilies() []string {
	return []string{
		// All three tracks of milestone 6 register their families now
		// (scan.go, auth.go, integrity.go): nothing of it stays reserved.

		// Milestone 7.
		MetricSyncRuns,
		MetricSyncDuration,
		MetricSyncLastSuccess,
		MetricSyncIngredients,
		MetricServerCertificateExpiry,
		MetricClockImplausible,
		MetricDiagnosticsBundles,
		MetricScanExceptions,
		MetricScanReblocked,
	}
}
