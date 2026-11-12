// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
)

// maxFindings bounds what one report may contribute. A real image carries
// hundreds; a report claiming millions is not one to keep in a task.
const maxFindings = 50000

// nativeReport is the subset of Trivy's JSON report (schema 2) the adapter
// reads. Native types stop here: nothing downstream sees them (ADR-0008
// decision 5).
type nativeReport struct {
	SchemaVersion int `json:"SchemaVersion"`
	Trivy         struct {
		Version string `json:"Version"`
	} `json:"Trivy"`
	Results []struct {
		Target          string          `json:"Target"`
		Vulnerabilities []nativeFinding `json:"Vulnerabilities"`
	} `json:"Results"`
}

type nativeFinding struct {
	VulnerabilityID  string                `json:"VulnerabilityID"`
	PkgName          string                `json:"PkgName"`
	InstalledVersion string                `json:"InstalledVersion"`
	FixedVersion     string                `json:"FixedVersion"`
	Severity         string                `json:"Severity"`
	CVSS             map[string]nativeCVSS `json:"CVSS"`
	DataSource       *struct{ ID string }  `json:"DataSource"`
}

type nativeCVSS struct {
	V2Score  float64 `json:"V2Score"`
	V3Score  float64 `json:"V3Score"`
	V40Score float64 `json:"V40Score"`
}

// parseReport normalizes a Trivy JSON report into findings, deduplicated
// (one vulnerability in one package version counts once, however many
// targets of the image report it) and sorted for a reproducible task
// document.
func parseReport(raw []byte) (version string, findings []scan.Finding, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var rep nativeReport
	if err := dec.Decode(&rep); err != nil {
		return "", nil, fmt.Errorf("unreadable scanner report: %w", err)
	}
	if rep.SchemaVersion != 2 {
		return "", nil, fmt.Errorf("unsupported scanner report schema %d (this adapter reads schema 2)", rep.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, res := range rep.Results {
		for i := range res.Vulnerabilities {
			v := &res.Vulnerabilities[i]
			if v.VulnerabilityID == "" {
				continue
			}
			key := v.VulnerabilityID + "\x00" + v.PkgName + "\x00" + v.InstalledVersion
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(findings) >= maxFindings {
				return "", nil, fmt.Errorf("the scanner report lists more than %d findings", maxFindings)
			}
			findings = append(findings, normalize(v))
		}
	}
	sort.Slice(findings, func(a, b int) bool {
		fa, fb := &findings[a], &findings[b]
		if fa.ID != fb.ID {
			return fa.ID < fb.ID
		}
		if fa.Package != fb.Package {
			return fa.Package < fb.Package
		}
		return fa.InstalledVersion < fb.InstalledVersion
	})
	return rep.Trivy.Version, findings, nil
}

// normalize maps one native finding onto the neutral model. The CVSS
// score is the highest base score across sources (FR-031 amendment: "when
// several sources score one finding, the highest score counts"), each
// source contributing its most recent CVSS version — v4.0, else v3.x,
// else v2 — so a deprecated v2 score never outbids the v3 score the same
// source publishes beside it (the severity bands of the amendment are
// CVSS v3's). Scores outside 0..10 are not scores.
func normalize(v *nativeFinding) scan.Finding {
	f := scan.Finding{
		ID:               v.VulnerabilityID,
		Severity:         scan.Severity(strings.ToUpper(v.Severity)),
		Package:          v.PkgName,
		InstalledVersion: v.InstalledVersion,
		FixedVersion:     v.FixedVersion,
	}
	if !f.Severity.Valid() {
		f.Severity = scan.SeverityUnknown
	}
	if v.DataSource != nil {
		f.Source = v.DataSource.ID
	}
	var best float64
	var scored bool
	for _, c := range v.CVSS {
		for _, s := range []float64{c.V40Score, c.V3Score, c.V2Score} {
			if s <= 0 || s > 10 {
				continue
			}
			if !scored || s > best {
				best, scored = s, true
			}
			break // this source's most recent version decides for it
		}
	}
	if scored {
		f.CVSS = &best
	}
	return f
}
