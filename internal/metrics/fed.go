// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import "time"

// Fed returns a registry on which every optional family has measured
// something, so that the reference (reference.go) and the tests that hold
// alert expressions to published families can see them all. A real
// instance exposes a family only once it has a true value to show
// (reserved.go); the reference documents all of them.
func Fed() *Registry {
	reg := New()
	now := time.Unix(1_700_000_000, 0)
	reg.SetSyncParallelism(3)
	// Families that only exist once a refusal has been counted.
	reg.PolicyRejections.WithLabelValues("TBY-POL-001")
	reg.PromotionRefusals.WithLabelValues("TBY-DST-001")
	auth := RegisterAuth(reg)
	auth.IDPReachable("oidc", true)
	auth.IDPReachable("saml", true)
	reg.ObserveSync(SyncRun{
		Success: true, Duration: time.Second, Finished: now,
		Ingredients: map[string]int{IngredientNew: 1, IngredientOutdated: 1, IngredientUpToDate: 1},
	})
	reg.ObserveDiagnosticsBundle(BundleWritten)
	reg.SetClock(func() []ClockReading {
		return []ClockReading{{Anchor: "build"}, {Anchor: "medium"}}
	})
	reg.SetServerCertificate(func() (time.Time, bool) { return now, true })
	reg.ObserveIntegrityVerification(VerdictClean, now)
	reg.RegisterReducedRecipes([]string{"scope"}, func() map[string]int { return nil })
	reg.Scan.SetState(func() ScanState {
		return ScanState{DBPresent: true, ScannerAvailable: true}
	})
	return reg
}
