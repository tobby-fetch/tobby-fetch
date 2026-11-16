// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// destinationTrust is the DESTINATION's real trust policy: a normal key and
// the fixture's signing key configured as an emergency root, reserved to a
// scope that does not cover registry.example.com/cookbook.
func destinationTrust(t *testing.T, f *fixture, scopeRepos ...string) media.Trust {
	t.Helper()
	normal, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	normalPEM, err := normal.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	emergencyPEM, err := f.kp.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	tp, err := engine.LoadTrust(config.Trust{
		Roots: []config.TrustRoot{
			{Name: "normal", Key: string(normalPEM)},
			{Name: "emergency-key", Key: string(emergencyPEM)},
		},
		Scopes: []config.TrustScope{{Name: "emergency", Repositories: scopeRepos, Roots: []string{"emergency-key"}}},
	}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return engine.MediaTrust{Policy: tp}
}

// TestReservedRootBlocksOutsideItsScope is the destination face of B-057:
// a medium's recipe outside every scope, signed by a key the destination
// reserves to its "emergency" scope, is blocked in TBY-SIG-001, and the
// reason names the root and the scope.
func TestReservedRootBlocksOutsideItsScope(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.write(time.Now().UTC())

	rep := f.verify(media.VerifyOptions{Zone: zoneA, Trust: destinationTrust(t, f, "emergency.example.com/**")})
	alpha := verdictOf(t, rep, "alpha")
	if alpha.Pushable {
		t.Fatal("a key reserved to the emergency scope verified a recipe outside it (B-057)")
	}
	if alpha.Reason == nil || alpha.Reason.Code != taxonomy.CodeSignature {
		t.Fatalf("alpha reason = %+v, want %s", alpha.Reason, taxonomy.CodeSignature)
	}
	fp, err := f.kp.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got := alpha.Reason.Params["reserved"]; !strings.Contains(got, "emergency-key") || !strings.Contains(got, fp) {
		t.Errorf("reserved = %q, want emergency-key (%s)", got, fp)
	}
	if got := alpha.Reason.Params["reservedto"]; got != "emergency" {
		t.Errorf("reservedto = %q, want emergency", got)
	}

	// Inside the scope the same medium is pushable: the reservation
	// narrows where the key applies, it does not switch it off.
	rep = f.verify(media.VerifyOptions{Zone: zoneA, Trust: destinationTrust(t, f, "registry.example.com/cookbook/*")})
	if alpha := verdictOf(t, rep, "alpha"); !alpha.Pushable {
		t.Errorf("inside its scope, alpha = %+v, want pushable", alpha.Reason)
	}
}
