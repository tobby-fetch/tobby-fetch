// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package assurance

import (
	"encoding/json"
	"testing"
)

func TestReduced(t *testing.T) {
	var none *Marker
	if none.Reduced() {
		t.Error("a nil marker reads as reduced: absence must mean full assurance")
	}
	if !(&Marker{Level: LevelReduced, Scope: "lab", Reasons: []Reason{ReasonUnsigned}}).Reduced() {
		t.Error("a reduced marker does not read as reduced")
	}
	if (&Marker{Level: "full"}).Reduced() {
		t.Error("a marker of another level reads as reduced")
	}
}

// TestMarkerJSONShape pins the field names shared by the task report, the
// API and the media manifest: single words, so they read the same in the
// snake_case task document and the camelCase manifest.
func TestMarkerJSONShape(t *testing.T) {
	raw, err := json.Marshal(Marker{Level: LevelReduced, Scope: "lab", Reasons: []Reason{ReasonUnsigned}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"level":"reduced","scope":"lab","reasons":["unsigned"]}`; string(raw) != want {
		t.Errorf("marker JSON = %s, want %s", raw, want)
	}
}

func TestForAdmission(t *testing.T) {
	if m := ForAdmission(true, "lab"); m != nil {
		t.Errorf("a recipe verified against a trust root carries %+v, want full assurance (nil)", m)
	}
	if m := ForAdmission(false, ""); m != nil {
		t.Errorf("an admission naming no scope carries %+v: a marker naming nothing is worse than none", m)
	}
	m := ForAdmission(false, "lab")
	if !m.Reduced() || m.Scope != "lab" || len(m.Reasons) != 1 || m.Reasons[0] != ReasonUnsigned {
		t.Errorf("ForAdmission(false, lab) = %+v, want reduced/lab/unsigned", m)
	}
}

func TestLogCarriesOnlyReducedAssurance(t *testing.T) {
	var none *Marker
	if _, ok := none.Log(); ok {
		t.Error("a nil marker produces a log field: the field's presence must mean reduced assurance")
	}
	if v, ok := ForUnsigned("lab").Log(); !ok || v != "reduced" {
		t.Errorf("Log() = %q, %v, want reduced, true", v, ok)
	}
}

// TestValidBoundsAForeignClaim: a marker read off a medium is shaped by
// someone else, so the reader accepts exactly the vocabulary this build
// emits and bounds the rest.
func TestValidBoundsAForeignClaim(t *testing.T) {
	long := make([]byte, MaxScopeLen+1)
	for i := range long {
		long[i] = 'x'
	}
	cases := []struct {
		name string
		m    *Marker
		want bool
	}{
		{"nil", nil, false},
		{"well formed", ForUnsigned("lab"), true},
		{"another level", &Marker{Level: "full", Scope: "lab", Reasons: []Reason{ReasonUnsigned}}, false},
		{"no scope", &Marker{Level: LevelReduced, Reasons: []Reason{ReasonUnsigned}}, false},
		{"no reason", &Marker{Level: LevelReduced, Scope: "lab"}, false},
		{"unknown reason", &Marker{Level: LevelReduced, Scope: "lab", Reasons: []Reason{"vibes"}}, false},
		{"oversized scope", &Marker{Level: LevelReduced, Scope: string(long), Reasons: []Reason{ReasonUnsigned}}, false},
	}
	for _, c := range cases {
		if got := c.m.Valid(); got != c.want {
			t.Errorf("%s: Valid() = %v, want %v", c.name, got, c.want)
		}
	}
}
