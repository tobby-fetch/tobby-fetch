// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import "testing"

// TestParseReportKeepsThePackageURL (B-070): the package URL Trivy gives
// each finding (PkgIdentifier.PURL) is kept — it is what a VEX
// subcomponent is matched against — and it is part of a finding's
// identity: two packages of the same name and version in two ecosystems
// stay two findings, so an exception covering one cannot hide the other.
func TestParseReportKeepsThePackageURL(t *testing.T) {
	raw := []byte(`{"SchemaVersion":2,"Trivy":{"Version":"0.70.0"},"Results":[
{"Target":"debian","Vulnerabilities":[{"VulnerabilityID":"CVE-X","PkgName":"yaml","PkgIdentifier":{"PURL":"pkg:deb/debian/yaml@0.1?distro=debian-12"},"InstalledVersion":"0.1","Severity":"HIGH"}]},
{"Target":"python","Vulnerabilities":[{"VulnerabilityID":"CVE-X","PkgName":"yaml","PkgIdentifier":{"PURL":"pkg:pypi/yaml@0.1"},"InstalledVersion":"0.1","Severity":"HIGH"}]},
{"Target":"again","Vulnerabilities":[{"VulnerabilityID":"CVE-X","PkgName":"yaml","PkgIdentifier":{"PURL":"pkg:pypi/yaml@0.1"},"InstalledVersion":"0.1","Severity":"HIGH"}]}]}`)
	_, findings, err := parseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want the deb and the pypi package, each once", findings)
	}
	if findings[0].PURL != "pkg:deb/debian/yaml@0.1?distro=debian-12" || findings[1].PURL != "pkg:pypi/yaml@0.1" {
		t.Errorf("package URLs = %q, %q", findings[0].PURL, findings[1].PURL)
	}
}
