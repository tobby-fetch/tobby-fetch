// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The two items of the R-15 acceptance: two images carrying the same CVE.
var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
	itemA   = Subject{Reference: "docker.io/bitnami/wordpress@" + digestA, Recipe: "wordpress@6.5.2", Digest: digestA, Repository: "docker.io/bitnami/wordpress"}
	itemB   = Subject{Reference: "docker.io/bitnami/mariadb@" + digestB, Recipe: "mariadb@11.4.0", Digest: digestB, Repository: "docker.io/bitnami/mariadb"}
)

func scoped(s ExceptionScope, expires time.Time) Exception {
	return Exception{ID: "CVE-2024-0001", Justification: "accepted", Expires: expires, Scope: &s}
}

func evaluateBlock(instance, retriever []Exception, subj Subject, findings ...Finding) *Report {
	p := Combine(&Levels{InstanceMode: ModeBlock, InstanceThreshold: 7, InstanceExceptions: instance, RetrieverExceptions: retriever})
	return Evaluate(&p, subj, result(findings...), Freshness{MaxAge: 168 * time.Hour}, now)
}

func critical(id string) Finding { return finding(id, SeverityCritical, f64(9.8)) }

// TestScopedExceptionCoversOnlyItsItems is the R-15 acceptance: an
// exception scoped to one digest admits that image and still blocks
// another image carrying the same CVE, whose report says the exception is
// out of scope (TBY-SCN-026, with the identifier, the item and the scope).
func TestScopedExceptionCoversOnlyItsItems(t *testing.T) {
	ex := []Exception{scoped(ExceptionScope{Digest: digestA}, now.Add(time.Hour))}

	in := evaluateBlock(ex, nil, itemA, critical("CVE-2024-0001"))
	if in.Policy.Verdict != VerdictPass || len(in.Policy.ExceptionsApplied) != 1 || len(in.Policy.ExceptionsOutOfScope) != 0 {
		t.Errorf("in scope: %+v", in.Policy)
	}

	out := evaluateBlock(ex, nil, itemB, critical("CVE-2024-0001"))
	if out.Policy.Verdict != VerdictBlock || !slices.Equal(out.Policy.Violations, []string{"CVE-2024-0001"}) {
		t.Fatalf("out of scope: verdict %s, violations %v — a scoped exception covered an item outside its scope", out.Policy.Verdict, out.Policy.Violations)
	}
	if len(out.Policy.ExceptionsOutOfScope) != 1 || len(out.Policy.ExceptionsApplied) != 0 {
		t.Errorf("out of scope: %+v", out.Policy)
	}
	var note *Reason
	for i := range out.Policy.Reasons {
		if out.Policy.Reasons[i].Code == taxonomy.CodeScanExceptionOutOfScope {
			note = &out.Policy.Reasons[i]
		}
	}
	if note == nil || note.Params["id"] != "CVE-2024-0001" || note.Params["reference"] != itemB.Reference || note.Params["scope"] != "digest="+digestA {
		t.Errorf("TBY-SCN-026 = %+v in %v", note, codes(out.Policy.Reasons))
	}
	if e := out.Refusal(); e == nil || e.Code() != taxonomy.CodeScanPolicyBlocked {
		t.Errorf("refusal = %v", e)
	}
}

// TestScopeCriteria walks every criterion, the glob grammar, the recipe
// name and name@version forms, and their AND combination.
func TestScopeCriteria(t *testing.T) {
	cases := []struct {
		name  string
		scope ExceptionScope
		subj  Subject
		want  bool
	}{
		{"digest match", ExceptionScope{Digest: digestA}, itemA, true},
		{"digest mismatch", ExceptionScope{Digest: digestA}, itemB, false},
		{"repository exact", ExceptionScope{Repository: "docker.io/bitnami/wordpress"}, itemA, true},
		{"repository glob", ExceptionScope{Repository: "docker.io/bitnami/*"}, itemB, true},
		{"repository globstar", ExceptionScope{Repository: "docker.io/**"}, itemA, true},
		{"repository other", ExceptionScope{Repository: "docker.io/library/*"}, itemA, false},
		{"repository star stays in its segment", ExceptionScope{Repository: "docker.io/*"}, itemA, false},
		{"recipe name", ExceptionScope{Recipe: "wordpress"}, itemA, true},
		{"recipe name, other recipe", ExceptionScope{Recipe: "wordpress"}, itemB, false},
		{"recipe name is not a prefix", ExceptionScope{Recipe: "word"}, itemA, false},
		{"recipe version", ExceptionScope{Recipe: "wordpress@6.5.2"}, itemA, true},
		{"recipe other version", ExceptionScope{Recipe: "wordpress@6.5.3"}, itemA, false},
		{"all criteria hold", ExceptionScope{Digest: digestA, Repository: "docker.io/bitnami/*", Recipe: "wordpress"}, itemA, true},
		{"one criterion fails", ExceptionScope{Digest: digestA, Repository: "docker.io/bitnami/*", Recipe: "mariadb"}, itemA, false},
		{"unknown digest never matches", ExceptionScope{Digest: digestA}, Subject{Reference: "x", Recipe: "wordpress@1"}, false},
		{"unknown repository never matches", ExceptionScope{Repository: "**"}, Subject{Reference: "x"}, false},
		{"unknown recipe never matches", ExceptionScope{Recipe: "wordpress"}, Subject{Reference: "x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := evaluateBlock([]Exception{scoped(c.scope, now.Add(time.Hour))}, nil, c.subj, critical("CVE-2024-0001"))
			if got := rep.Policy.Verdict == VerdictPass; got != c.want {
				t.Errorf("covered = %v; want %v (%+v)", got, c.want, rep.Policy)
			}
		})
	}
}

// TestAnApplicableExceptionIsNeverHidden: an out-of-scope or expired
// exception for an identifier, listed first, never hides a later one that
// is in scope and in force — matching picks any applicable exception, not
// the first by identifier.
func TestAnApplicableExceptionIsNeverHidden(t *testing.T) {
	for name, exs := range map[string][]Exception{
		"out of scope first": {
			scoped(ExceptionScope{Digest: digestB}, now.Add(time.Hour)),
			scoped(ExceptionScope{Digest: digestA}, now.Add(time.Hour)),
		},
		"expired first": {
			scoped(ExceptionScope{Digest: digestA}, now.Add(-time.Hour)),
			scoped(ExceptionScope{Recipe: "wordpress"}, now.Add(time.Hour)),
		},
	} {
		t.Run(name, func(t *testing.T) {
			rep := evaluateBlock(exs, nil, itemA, critical("CVE-2024-0001"))
			if rep.Policy.Verdict != VerdictPass || len(rep.Policy.ExceptionsApplied) != 1 {
				t.Errorf("verdict %s, applied %+v", rep.Policy.Verdict, rep.Policy.ExceptionsApplied)
			}
			if slices.Contains(codes(rep.Policy.Reasons), taxonomy.CodeScanExceptionOutOfScope) || slices.Contains(codes(rep.Policy.Reasons), taxonomy.CodeScanExceptionExpired) {
				t.Errorf("a covered finding carries an exception note: %v", codes(rep.Policy.Reasons))
			}
		})
	}
	// Out of scope AND expired: reported as out of scope only — it was
	// never meant for this item, so its expiry re-blocks nothing here.
	rep := evaluateBlock([]Exception{scoped(ExceptionScope{Digest: digestB}, now.Add(-time.Hour))}, nil, itemA, critical("CVE-2024-0001"))
	if len(rep.Policy.ExceptionsOutOfScope) != 1 || len(rep.Policy.ExceptionsExpired) != 0 || rep.Reblocked() != nil {
		t.Errorf("%+v", rep.Policy)
	}
}

// TestReblocked: an expired exception that matched a violating finding of
// an item refused by a blocking policy is the re-blocking FR-031 asks to
// state; under an advisory policy, or for another refusal, it is not.
func TestReblocked(t *testing.T) {
	expired := []Exception{scoped(ExceptionScope{Digest: digestA}, now.Add(-time.Hour))}
	rep := evaluateBlock(expired, nil, itemA, critical("CVE-2024-0001"))
	if got := rep.Reblocked(); len(got) != 1 || got[0].ID != "CVE-2024-0001" || !rep.Blocks() {
		t.Errorf("Reblocked = %+v (verdict %s)", got, rep.Policy.Verdict)
	}
	if !slices.Contains(codes(rep.Policy.Reasons), taxonomy.CodeScanExceptionExpired) {
		t.Errorf("the expired exception is not named: %v", codes(rep.Policy.Reasons))
	}
	// Advisory: the finding violates, the item proceeds — nothing is
	// re-blocked.
	p := Combine(&Levels{InstanceMode: ModeWarn, InstanceThreshold: 7, InstanceExceptions: expired})
	warn := Evaluate(&p, itemA, result(critical("CVE-2024-0001")), Freshness{MaxAge: time.Hour, Block: true}, now)
	if warn.Reblocked() != nil {
		t.Errorf("advisory, refused for a stale database only: Reblocked = %+v", warn.Reblocked())
	}
	// Still in force: nothing re-blocked.
	ok := evaluateBlock([]Exception{scoped(ExceptionScope{Digest: digestA}, now.Add(time.Hour))}, nil, itemA, critical("CVE-2024-0001"))
	if ok.Reblocked() != nil || ok.Blocks() {
		t.Errorf("in force: %+v", ok.Policy)
	}
	var none *Report
	if none.Reblocked() != nil {
		t.Error("a nil report re-blocks")
	}
}

// TestUnboundedRetrieverException: a Retriever exception without expiry
// applies and the report says it is unbounded (TBY-SCN-028).
func TestUnboundedRetrieverException(t *testing.T) {
	rep := evaluateBlock(nil, []Exception{{ID: "CVE-2024-0001"}}, itemA, critical("CVE-2024-0001"))
	if rep.Policy.Verdict != VerdictPass {
		t.Fatalf("verdict %s", rep.Policy.Verdict)
	}
	var found bool
	for _, r := range rep.Policy.Reasons {
		if r.Code == taxonomy.CodeScanExceptionUnbounded && r.Params["id"] == "CVE-2024-0001" {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons = %v, want TBY-SCN-028", codes(rep.Policy.Reasons))
	}
	bounded := evaluateBlock(nil, []Exception{{ID: "CVE-2024-0001", Expires: now.Add(time.Hour)}}, itemA, critical("CVE-2024-0001"))
	if slices.Contains(codes(bounded.Policy.Reasons), taxonomy.CodeScanExceptionUnbounded) {
		t.Error("a bounded exception is reported unbounded")
	}
}

// TestPackagesNarrowAVEXException: a VEX product naming subcomponents
// covers the finding in those packages only.
func TestPackagesNarrowAVEXException(t *testing.T) {
	ex := Exception{ID: "CVE-2024-0001", Expires: now.Add(time.Hour), VEX: "/etc/tobby/site.openvex.json",
		Scope: &ExceptionScope{Digest: digestA, Packages: []string{"pkg:deb/debian/libxml2"}}}
	inPkg := Finding{ID: "CVE-2024-0001", Severity: SeverityCritical, CVSS: f64(9.8), Package: "libxml2",
		PURL: "pkg:deb/debian/libxml2@2.9.14?arch=amd64&distro=debian-12", InstalledVersion: "2.9.14"}
	other := Finding{ID: "CVE-2024-0001", Severity: SeverityCritical, CVSS: f64(9.8), Package: "libxslt",
		PURL: "pkg:deb/debian/libxslt@1.1.35?arch=amd64&distro=debian-12", InstalledVersion: "1.1.35"}
	if rep := evaluateBlock([]Exception{ex}, nil, itemA, inPkg); rep.Policy.Verdict != VerdictPass || rep.Policy.ExceptionsApplied[0].Source() != SourceVEX {
		t.Errorf("in package: %+v", rep.Policy)
	}
	if rep := evaluateBlock([]Exception{ex}, nil, itemA, inPkg, other); rep.Policy.Verdict != VerdictBlock {
		t.Errorf("another package of the image was covered: %+v", rep.Policy)
	}
}

// TestScopeReasonParametersAreCatalogued: the new notes render.
func TestScopeReasonParametersAreCatalogued(t *testing.T) {
	rep := evaluateBlock(
		[]Exception{scoped(ExceptionScope{Digest: digestB, Recipe: "x"}, now.Add(time.Hour))},
		[]Exception{{ID: "CVE-2024-0002"}},
		itemA, critical("CVE-2024-0001"), critical("CVE-2024-0002"))
	seen := map[taxonomy.Code]bool{}
	for _, r := range rep.Policy.Reasons {
		seen[r.Code] = true
		entry, _ := taxonomy.Lookup(r.Code)
		if len(r.Params) != len(entry.Params) {
			t.Errorf("%s carries %v; the catalogue declares %v", r.Code, r.Params, entry.Params)
		}
	}
	if !seen[taxonomy.CodeScanExceptionOutOfScope] || !seen[taxonomy.CodeScanExceptionUnbounded] {
		t.Errorf("reasons = %v", codes(rep.Policy.Reasons))
	}
}

// TestPackageURLMatchesExactly (B-070): a subcomponent's package URL
// matches the finding's on type, namespace and name, on the version when
// it declares one (epoch included), and on every qualifier it declares;
// nothing else, and never a finding the scanner gave no package URL.
func TestPackageURLMatchesExactly(t *testing.T) {
	for _, c := range []struct {
		pattern, purl, installed string
		want                     bool
	}{
		{"pkg:deb/debian/openssl", "pkg:deb/debian/openssl@3.0.11-1~deb12u2?arch=amd64&distro=debian-12", "3.0.11-1~deb12u2", true},
		{"pkg:deb/debian/openssl@3.0.11-1~deb12u2", "pkg:deb/debian/openssl@3.0.11-1~deb12u2?arch=amd64", "3.0.11-1~deb12u2", true},
		{"pkg:deb/debian/openssl@3.0.11", "pkg:deb/debian/openssl@1.1.1w-0+deb11u1?arch=amd64", "1.1.1w-0+deb11u1", false},
		{"pkg:deb/debian/openssl@3.0.11", "pkg:deb/debian/openssl@3.0.11?epoch=1", "1:3.0.11", false},
		{"pkg:deb/debian/openssl@3.0.11?epoch=1", "pkg:deb/debian/openssl@3.0.11?epoch=1&arch=amd64", "1:3.0.11", true},
		{"pkg:deb/debian/openssl?arch=arm64", "pkg:deb/debian/openssl@3.0.11?arch=amd64", "3.0.11", false},
		{"pkg:deb/debian/openssl", "pkg:deb/ubuntu/openssl@3.0.11", "3.0.11", false},
		{"pkg:deb/debian/openssl", "pkg:rpm/debian/openssl@3.0.11", "3.0.11", false},
		{"pkg:DEB/Debian/OpenSSL", "pkg:deb/debian/openssl@3.0.11", "3.0.11", true},
		{"pkg:golang/github.com/good/yaml@v1.2.3", "pkg:golang/github.com/good/yaml@v1.2.3", "v1.2.3", true},
		{"pkg:golang/github.com/good/yaml", "pkg:golang/github.com/evil/yaml@v1.2.3", "v1.2.3", false},
		{"pkg:golang/github.com/good/yaml", "pkg:deb/debian/yaml@0.1", "0.1", false},
		{"pkg:golang/github.com/good/yaml", "pkg:golang/yaml@v1.2.3", "v1.2.3", false},
		{"pkg:golang/github.com/Good/yaml", "pkg:golang/github.com/good/yaml@v1.2.3", "v1.2.3", false},
		{"pkg:pypi/Django_Rest", "pkg:pypi/django-rest@3.0", "3.0", true},
		{"pkg:deb/debian/openssl@3.0.11", "pkg:deb/debian/openssl", "3.0.11", true},
		{"pkg:deb/debian/openssl@3.0.11", "pkg:deb/debian/openssl", "3.0.12", false},
		{"pkg:deb/debian/openssl", "", "3.0.11", false},
		{"pkg:deb/debian/openssl#usr/lib", "pkg:deb/debian/openssl@3.0.11", "3.0.11", false},
	} {
		p, err := ParsePackageURL(c.pattern)
		if err != nil {
			t.Fatalf("%s: %v", c.pattern, err)
		}
		if got := p.Matches(c.purl, c.installed); got != c.want {
			t.Errorf("%s matches %q (%s) = %v, want %v", c.pattern, c.purl, c.installed, got, c.want)
		}
	}
}
