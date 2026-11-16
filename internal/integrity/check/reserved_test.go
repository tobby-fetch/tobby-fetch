// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package check_test

import (
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestReservedRootIsRefusedOutsideItsScope is the integrity-check face of
// B-057: a stored recipe outside every scope, signed by a key this
// instance reserves to its "emergency" scope, no longer verifies — and
// the defect names the root and the scope it is reserved to.
func TestReservedRootIsRefusedOutsideItsScope(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)

	normalPEM, err := newKeyPair(t).PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	emergencyPEM, err := f.kp.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	policy := func(scopeRepo string) check.Options {
		tp, err := engine.LoadTrust(config.Trust{
			Roots: []config.TrustRoot{
				{Name: "normal", Key: string(normalPEM)},
				{Name: "emergency-key", Key: string(emergencyPEM)},
			},
			Scopes: []config.TrustScope{{Name: "emergency", Repositories: []string{scopeRepo}, Roots: []string{"emergency-key"}}},
		}, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return check.Options{Trust: engine.MediaTrust{Policy: tp}}
	}

	rep := f.verify(policy("emergency.example.com/**"))
	sig := find(rep.Defects, taxonomy.CodeSignature)
	if sig == nil || rep.Recipes[0].Signature != integrity.SignatureRefused {
		t.Fatalf("defects %v, row %+v — want TBY-SIG-001: a key reserved to the emergency scope verified a recipe outside it (B-057)",
			rep.Defects, rep.Recipes[0])
	}
	fp, err := f.kp.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got := sig.Params["reserved"]; !strings.Contains(got, "emergency-key") || !strings.Contains(got, fp) {
		t.Errorf("reserved = %q, want emergency-key (%s)", got, fp)
	}
	if got := sig.Params["reservedto"]; got != "emergency" {
		t.Errorf("reservedto = %q, want emergency", got)
	}

	// Inside its scope the key still verifies.
	rep = f.verify(policy(cookbook + "/*"))
	if row := rep.Recipes[0]; row.Signature != integrity.SignatureVerified {
		t.Errorf("inside its scope, row = %+v, want verified", row)
	}
}
