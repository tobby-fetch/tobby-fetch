// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package vex

import (
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
)

// FuzzParse is the native fuzz target of the OpenVEX parser (NFR-024).
// The committed seed corpus lives in testdata/fuzz/FuzzParse; CI runs the
// target for a bounded time on every change (.github/workflows/ci.yml).
//
// Properties: Parse never panics and never accepts more than the bounds;
// what it accepts translates without panicking; and no exception it
// yields is ever unbounded — every one carries the declared expiry and a
// scope that narrows to a digest or a repository (a product that cannot
// be bounded is skipped, never widened to the whole instance), and narrows
// to packages by whole package URLs only.
func FuzzParse(f *testing.F) {
	f.Add([]byte(doc(`{"vulnerability":{"name":"CVE-2024-12345"},"products":[{"@id":"` + digestA + `"}],"status":"not_affected","justification":"component_not_present"}`)))
	f.Add([]byte(doc(`{"vulnerability":{"name":"CVE-2024-12345"},"products":[{"@id":"pkg:oci/nginx"}],"status":"not_affected","impact_statement":"x"}`)))
	f.Add([]byte(doc(`{"vulnerability":{"name":"CVE-2024-12345"},"products":[{"@id":"` + digestA + `","subcomponents":[{"@id":"pkg:golang/github.com/good/yaml@v1.2.3"}]}],"status":"not_affected","justification":"component_not_present"}`)))
	f.Add([]byte(`{}`))
	f.Add([]byte{})
	expires := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := Parse(data)
		if err != nil {
			return
		}
		if len(data) > MaxDocumentSize || len(d.Statements) > MaxStatements {
			t.Fatalf("a document beyond the bounds was accepted")
		}
		tr, err := Translate(d, "/fuzz.json", expires)
		if err != nil {
			return
		}
		if tr.Admitted+tr.Ignored != tr.Statements {
			t.Fatalf("statements %d != admitted %d + ignored %d", tr.Statements, tr.Admitted, tr.Ignored)
		}
		for _, ex := range tr.Exceptions {
			if ex.Scope.IsZero() || ex.Scope.Digest == "" && ex.Scope.Repository == "" {
				t.Fatalf("an unbounded exception was yielded: %+v", ex)
			}
			// A subcomponent narrows by its whole package URL, never by a
			// bare name or a part of a package (B-070).
			for _, id := range ex.Scope.Packages {
				pu, err := scan.ParsePackageURL(id)
				if err != nil || pu.Subpath != "" {
					t.Fatalf("exception %+v narrows to %q, not a whole package URL", ex, id)
				}
			}
			if !ex.Expires.Equal(expires) || ex.VEX != "/fuzz.json" || ex.ID == "" {
				t.Fatalf("exception %+v does not carry the declared expiry and document", ex)
			}
		}
	})
}
