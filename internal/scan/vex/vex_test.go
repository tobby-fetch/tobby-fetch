// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package vex

import (
	"encoding/json"
	"testing"
)

func TestVocabularies(t *testing.T) {
	for _, s := range []Status{StatusNotAffected, StatusAffected, StatusFixed, StatusUnderInvestigation} {
		if !s.Valid() {
			t.Errorf("%s is not valid", s)
		}
	}
	if Status("ignored").Valid() {
		t.Error("an unknown status is valid")
	}
	for _, j := range []Justification{ComponentNotPresent, VulnerableCodeNotPresent, VulnerableCodeNotInExecutePath,
		VulnerableCodeCannotBeControlledByAdversary, InlineMitigationsAlreadyExist} {
		if !j.Valid() {
			t.Errorf("%s is not valid", j)
		}
	}
	if Justification("trust_me").Valid() {
		t.Error("an unknown justification is valid")
	}
}

func TestAdmits(t *testing.T) {
	cases := map[string]struct {
		s    Statement
		want bool
	}{
		"justified not_affected":    {Statement{Status: StatusNotAffected, Justification: VulnerableCodeNotPresent}, true},
		"impact statement only":     {Statement{Status: StatusNotAffected, ImpactStatement: "we think so"}, false},
		"affected":                  {Statement{Status: StatusAffected, Justification: VulnerableCodeNotPresent}, false},
		"unknown justification":     {Statement{Status: StatusNotAffected, Justification: "trust_me"}, false},
		"fixed adds no exception":   {Statement{Status: StatusFixed}, false},
		"under investigation, none": {Statement{Status: StatusUnderInvestigation}, false},
	}
	for name, c := range cases {
		if got := c.s.Admits(); got != c.want {
			t.Errorf("%s: Admits = %v, want %v", name, got, c.want)
		}
	}
}

// TestDocumentShape pins the JSON names of the subset against a minimal
// OpenVEX document.
func TestDocumentShape(t *testing.T) {
	raw := `{"@context":"https://openvex.dev/ns/v0.2.0","@id":"https://example.com/vex/1","author":"Site security",
"timestamp":"2026-11-16T10:00:00Z","version":1,"statements":[{"vulnerability":{"name":"CVE-2024-12345"},
"products":[{"@id":"pkg:oci/nginx@sha256%3Aabc"}],"status":"not_affected","justification":"vulnerable_code_not_in_execute_path"}]}`
	var d Document
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Statements) != 1 || !d.Statements[0].Admits() || d.Statements[0].Vulnerability.Name != "CVE-2024-12345" ||
		d.Statements[0].Products[0].ID == "" || d.Author == "" || d.Timestamp.IsZero() {
		t.Errorf("document read as %+v", d)
	}
}
