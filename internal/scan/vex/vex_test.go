// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package vex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
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

// digestA and digestB are two manifest digests.
var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
)

// doc wraps statements in a valid OpenVEX v0.2.0 document.
func doc(statements string) string {
	return `{"@context":"https://openvex.dev/ns/v0.2.0","@id":"https://vex.example/site-1","author":"Site security",
"timestamp":"2026-11-16T10:00:00Z","version":1,"statements":[` + statements + `]}`
}

// TestParseAcceptsTheSpecification reads a document using every field
// OpenVEX v0.2.0 defines.
func TestParseAcceptsTheSpecification(t *testing.T) {
	raw := `{"@context":"https://openvex.dev/ns/v0.2.0","@id":"https://vex.example/full","author":"Site security",
"role":"Document creator","timestamp":"2026-11-16T10:00:00Z","last_updated":"2026-11-16T11:00:00Z","version":2,
"tooling":"vexctl","statements":[{"@id":"https://vex.example/full#1","version":1,
"vulnerability":{"@id":"https://nvd.nist.gov/vuln/detail/CVE-2024-12345","name":"CVE-2024-12345","description":"a parser bug","aliases":["GHSA-xxxx-yyyy-zzzz"]},
"timestamp":"2026-11-16T10:00:00Z","last_updated":"2026-11-16T10:30:00Z",
"products":[{"@id":"pkg:oci/nginx@sha256%3A` + strings.Repeat("a", 64) + `?repository_url=docker.io/library/nginx",
"identifiers":{"purl":"pkg:oci/nginx","cpe22":"cpe:/a:nginx:nginx","cpe23":"cpe:2.3:a:nginx:nginx:*:*:*:*:*:*:*:*"},
"hashes":{"sha-256":"` + strings.Repeat("a", 64) + `","md5":"00","sha1":"00","sha-384":"00","sha-512":"00","sha3-224":"00","sha3-256":"00","sha3-384":"00","sha3-512":"00","blake2s-256":"00","blake2b-256":"00","blake2b-512":"00"},
"subcomponents":[{"@id":"pkg:deb/debian/libxml2@2.9.14","identifiers":{"purl":"pkg:deb/debian/libxml2@2.9.14"},"hashes":{"sha-256":"00"}}]}],
"status":"not_affected","supplier":"Site security","status_notes":"reviewed","justification":"vulnerable_code_not_in_execute_path",
"impact_statement":"the parser is never reached"},
{"vulnerability":{"name":"CVE-2024-22222"},"products":[{"@id":"` + digestB + `"}],"status":"affected","action_statement":"upgrade","action_statement_timestamp":"2026-11-16T10:00:00Z"}]}`
	d, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Statements) != 2 || d.Statements[0].Products[0].Subcomponents[0].ID == "" || d.Tooling != "vexctl" {
		t.Errorf("read as %+v", d)
	}
}

// TestParseIsStrict refuses everything the specification does not define
// and everything Tobby bounds (NFR-023).
func TestParseIsStrict(t *testing.T) {
	good := `{"vulnerability":{"name":"CVE-2024-12345"},"products":[{"@id":"` + digestA + `"}],"status":"not_affected","justification":"component_not_present"}`
	if _, err := Parse([]byte(doc(good))); err != nil {
		t.Fatalf("the reference document is refused: %v", err)
	}
	cases := map[string]struct{ raw, want string }{
		"unknown document field":  {strings.Replace(doc(good), `"version":1`, `"version":1,"signature":"x"`, 1), `unknown field "signature"`},
		"unknown statement field": {doc(strings.Replace(good, `"status"`, `"severity":"low","status"`, 1)), `unknown field "severity"`},
		"case-folded field":       {doc(strings.Replace(good, `"status"`, `"Status"`, 1)), `unknown field "Status"`},
		"known name, wrong place": {doc(strings.Replace(good, `"status"`, `"author":"x","status"`, 1)), "OpenVEX v0.2.0 shape"},
		"duplicate field":         {doc(strings.Replace(good, `"status":"not_affected"`, `"status":"affected","status":"not_affected"`, 1)), `duplicate field "status"`},
		"trailing data":           {doc(good) + `{}`, "trailing data"},
		"trailing garbage":        {doc(good) + ` x`, "not well-formed"},
		"not an object":           {`[1]`, "not a JSON object"},
		"empty":                   {``, "empty"},
		"invalid UTF-8":           {doc(strings.Replace(good, "CVE-2024-12345", "CVE-\xff", 1)), "UTF-8"},
		"older context":           {strings.Replace(doc(good), "v0.2.0", "v0.0.1", 1), "@context"},
		"no @id":                  {strings.Replace(doc(good), `"@id":"https://vex.example/site-1",`, ``, 1), "@id is required"},
		"no author":               {strings.Replace(doc(good), `"author":"Site security",`, ``, 1), "author is required"},
		"no timestamp":            {strings.Replace(doc(good), `"timestamp":"2026-11-16T10:00:00Z",`, ``, 1), "timestamp is required"},
		"bad timestamp":           {strings.Replace(doc(good), `2026-11-16T10:00:00Z`, `yesterday`, 1), "shape"},
		"no version":              {strings.Replace(doc(good), `"version":1,`, ``, 1), "version is required"},
		"no vulnerability name":   {doc(strings.Replace(good, `"name":"CVE-2024-12345"`, `"@id":"x"`, 1)), "vulnerability.name is required"},
		"space in name":           {doc(strings.Replace(good, `CVE-2024-12345`, `CVE 2024`, 1)), "space"},
		"unknown status":          {doc(strings.Replace(good, `"not_affected"`, `"ignored"`, 1)), `status "ignored"`},
		"unknown justification":   {doc(strings.Replace(good, `component_not_present`, `trust_me`, 1)), `justification "trust_me"`},
		"justification elsewhere": {doc(strings.Replace(good, `"not_affected"`, `"fixed"`, 1)), "only defined for status not_affected"},
		"no products":             {doc(strings.Replace(good, `[{"@id":"`+digestA+`"}]`, `[]`, 1)), "products is required"},
		"empty product":           {doc(strings.Replace(good, `{"@id":"`+digestA+`"}`, `{}`, 1)), "an @id or an identifier is required"},
		"bare not_affected":       {doc(strings.Replace(good, `,"justification":"component_not_present"`, ``, 1)), "requires a justification or an impact_statement"},
		"affected, no action":     {doc(`{"vulnerability":{"name":"CVE-2024-1"},"products":[{"@id":"` + digestA + `"}],"status":"affected"}`), "action_statement"},
		"vulnerability as string": {doc(strings.Replace(good, `{"name":"CVE-2024-12345"}`, `"CVE-2024-12345"`, 1)), "shape"},
		"too deep":                {doc(strings.Replace(good, `"products"`, `"tooling":`+strings.Repeat("[", 20)+strings.Repeat("]", 20)+`,"products"`, 1)), "deeper"},
		"oversized":               {doc(good) + strings.Repeat(" ", MaxDocumentSize), "larger than"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.raw))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Parse = %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}

// TestParseBoundsCounts refuses a document carrying more statements than
// the bound, whatever its size.
func TestParseBoundsCounts(t *testing.T) {
	one := `{"vulnerability":{"name":"CVE-2024-1"},"products":[{"@id":"` + digestA + `"}],"status":"fixed"}`
	many := strings.TrimSuffix(strings.Repeat(one+",", MaxStatements+1), ",")
	if _, err := Parse([]byte(doc(many))); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("Parse = %v, want the statement bound", err)
	}
}

var expires = time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)

func translate(t *testing.T, statements string) *Translation {
	t.Helper()
	d, err := Parse([]byte(doc(statements)))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Translate(d, "/etc/tobby/site.openvex.json", expires)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// TestTranslateAdmitsJustifiedNotAffectedOnly locks the decision of
// 2026-11-16: only a justified not_affected statement yields exceptions;
// an impact statement alone, and every other status, add nothing — and
// say so.
func TestTranslateAdmitsJustifiedNotAffectedOnly(t *testing.T) {
	tr := translate(t, `{"vulnerability":{"name":"CVE-2024-11111"},"products":[{"@id":"`+digestA+`"}],"status":"not_affected","justification":"vulnerable_code_not_present"},
{"vulnerability":{"name":"CVE-2024-22222"},"products":[{"@id":"`+digestA+`"}],"status":"not_affected","impact_statement":"we believe it is fine"},
{"vulnerability":{"name":"CVE-2024-33333"},"products":[{"@id":"`+digestA+`"}],"status":"fixed"},
{"vulnerability":{"name":"CVE-2024-44444"},"products":[{"@id":"`+digestA+`"}],"status":"under_investigation"},
{"vulnerability":{"name":"CVE-2024-55555"},"products":[{"@id":"`+digestA+`"}],"status":"affected","action_statement":"upgrade"}`)
	if tr.Statements != 5 || tr.Admitted != 1 || tr.Ignored != 4 {
		t.Errorf("counts = %d/%d/%d, want 5 statements, 1 admitted, 4 ignored", tr.Statements, tr.Admitted, tr.Ignored)
	}
	if len(tr.Exceptions) != 1 || tr.Exceptions[0].ID != "CVE-2024-11111" {
		t.Fatalf("exceptions = %+v, want CVE-2024-11111 only", tr.Exceptions)
	}
	ex := tr.Exceptions[0]
	if ex.VEX != "/etc/tobby/site.openvex.json" || !ex.Expires.Equal(expires) || ex.Level != scan.LevelInstance ||
		ex.Scope == nil || ex.Scope.Digest != digestA || !strings.Contains(ex.Justification, "vulnerable_code_not_present") ||
		!strings.Contains(ex.Justification, "https://vex.example/site-1") || ex.Source() != scan.SourceVEX {
		t.Errorf("exception = %+v", ex)
	}
	var impact bool
	for _, n := range tr.Notes {
		if n.Vulnerability == "CVE-2024-22222" && n.Reason == NoteImpactStatementOnly {
			impact = true
		}
	}
	if !impact {
		t.Errorf("the impact-statement-only statement is not reported: %+v", tr.Notes)
	}
}

// TestScopeOfProducts maps every product form that can be bounded, and
// refuses — never widens — the others.
func TestScopeOfProducts(t *testing.T) {
	hexA := strings.Repeat("a", 64)
	cases := map[string]struct {
		product Product
		want    *scan.ExceptionScope
		err     string
	}{
		"oci purl":                 {Product{ID: "pkg:oci/nginx@sha256%3A" + hexA}, &scan.ExceptionScope{Digest: digestA}, ""},
		"oci purl with repository": {Product{ID: "pkg:oci/nginx@sha256%3A" + hexA + "?repository_url=index.docker.io/library/nginx&arch=amd64"}, &scan.ExceptionScope{Digest: digestA, Repository: "docker.io/library/nginx"}, ""},
		"oci purl, repository":     {Product{ID: "pkg:oci/nginx?repository_url=registry.example.com:5000/team/nginx"}, &scan.ExceptionScope{Repository: "registry.example.com_5000/team/nginx"}, ""},
		"purl as identifier":       {Product{Identifiers: &Identifiers{PURL: "pkg:oci/nginx@sha256:" + hexA}}, &scan.ExceptionScope{Digest: digestA}, ""},
		"bare digest":              {Product{ID: digestA}, &scan.ExceptionScope{Digest: digestA}, ""},
		"pinned reference":         {Product{ID: "docker.io/library/nginx@" + digestA}, &scan.ExceptionScope{Digest: digestA, Repository: "docker.io/library/nginx"}, ""},
		"repository":               {Product{ID: "ghcr.io/team/app"}, &scan.ExceptionScope{Repository: "ghcr.io/team/app"}, ""},
		"subcomponents":            {Product{ID: digestA, Subcomponents: []Subcomponent{{ID: "pkg:deb/debian/libxml2@2.9.14"}, {Identifiers: &Identifiers{PURL: "pkg:golang/github.com/x/net@v0.1.0"}}}}, &scan.ExceptionScope{Digest: digestA, Packages: []string{"debian/libxml2", "debian:libxml2", "github.com.x:net", "github.com/x/net", "libxml2", "net"}}, ""},
		"oci purl, name only":      {Product{ID: "pkg:oci/nginx"}, nil, "every image called nginx"},
		"oci purl, tag version":    {Product{ID: "pkg:oci/nginx@1.27"}, nil, "manifest digest"},
		"repository mismatch":      {Product{ID: "pkg:oci/nginx@sha256:" + hexA + "?repository_url=docker.io/library/httpd"}, nil, "does not end with"},
		"other purl type":          {Product{ID: "pkg:deb/debian/openssl@3.0"}, nil, "type deb"},
		"tagged reference":         {Product{ID: "docker.io/library/nginx:1.27"}, nil, "tag moves"},
		"no host":                  {Product{ID: "library/nginx"}, nil, "registry host"},
		"short digest":             {Product{ID: "sha256:abc"}, nil, "not a sha256"},
		"URL":                      {Product{ID: "https://example.com/image"}, nil, "URL"},
		"CPE":                      {Product{ID: "cpe:2.3:a:nginx:nginx:1.27:*:*:*:*:*:*:*"}, nil, "registry host"},
		"pattern":                  {Product{ID: "docker.io/library/*"}, nil, "pattern"},
		"subcomponent not a purl":  {Product{ID: digestA, Subcomponents: []Subcomponent{{ID: "libxml2"}}}, nil, "not a package URL"},
		"no identifier":            {Product{Identifiers: &Identifiers{CPE23: "cpe:2.3:a:x"}}, nil, "neither an @id nor a package URL"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ScopeOf(&c.product)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("ScopeOf = %+v, %v; want an error mentioning %q", got, err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Digest != c.want.Digest || got.Repository != c.want.Repository || strings.Join(got.Packages, ",") != strings.Join(c.want.Packages, ",") {
				t.Errorf("ScopeOf = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestTranslateNeverWidens locks the choice for products that cannot be
// bounded: skipped and reported, never turned into an exception for every
// item. A statement none of whose products is bounded is ignored.
func TestTranslateNeverWidens(t *testing.T) {
	tr := translate(t, `{"vulnerability":{"name":"CVE-2024-11111","aliases":["GHSA-aaaa-bbbb-cccc"]},
"products":[{"@id":"pkg:oci/nginx"},{"@id":"docker.io/library/nginx@`+digestA+`"}],"status":"not_affected","justification":"component_not_present"},
{"vulnerability":{"name":"CVE-2024-22222"},"products":[{"@id":"cpe:2.3:a:x:y:1:*:*:*:*:*:*:*"}],"status":"not_affected","justification":"component_not_present"}`)
	if tr.Admitted != 1 || tr.Ignored != 1 {
		t.Errorf("admitted/ignored = %d/%d, want 1/1", tr.Admitted, tr.Ignored)
	}
	if len(tr.Exceptions) != 2 {
		t.Fatalf("exceptions = %+v, want the name and its alias for the one bounded product", tr.Exceptions)
	}
	for _, ex := range tr.Exceptions {
		if ex.Scope.IsZero() || ex.Scope.Digest != digestA || ex.Scope.Repository != "docker.io/library/nginx" {
			t.Errorf("exception %+v is not bounded by its product", ex)
		}
	}
	unbounded := 0
	for _, n := range tr.Notes {
		if n.Reason == NoteUnboundedProduct {
			unbounded++
		}
	}
	if unbounded != 2 {
		t.Errorf("notes = %+v, want the two unbounded products", tr.Notes)
	}
	if _, err := Translate(&Document{}, "/x", time.Time{}); err == nil {
		t.Error("a translation without an expiry was accepted")
	}
}

// TestReadFile reads a local document and refuses what is not a regular
// file.
func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "site.openvex.json")
	if err := os.WriteFile(path, []byte(doc(`{"vulnerability":{"name":"CVE-2024-1"},"products":[{"@id":"`+digestA+`"}],"status":"fixed"}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir); err == nil {
		t.Error("a directory was read as a document")
	}
	if _, err := ReadFile(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("an absent file was read")
	}
}
