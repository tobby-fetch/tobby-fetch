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
