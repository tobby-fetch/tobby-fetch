// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"sort"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// ScanState is what the interface shows about vulnerability scanning
// (FR-031, FR-032, R-13): read on every page for the banner stack, and on
// the Retriever screen for the indicator.
type ScanState struct {
	// Blocking is false when the policy in force is advisory.
	Blocking bool
	// Cause is the degraded mode — no usable scanner, or no database —
	// nil when both are usable.
	Cause *taxonomy.Error
	// Scanner and Origin describe the binary, when one is usable.
	Scanner scan.Info
	Origin  string
	// Database is the database in use, nil when there is none.
	Database *scan.Database
	// MaxAge is scan.maxDbAge.
	MaxAge time.Duration
}

// maxFindingRows bounds the findings table of one item: the worst first,
// the rest counted — the complete list is the API's task document.
const maxFindingRows = 50

// scanBanners is the scanning part of the banner stack: the degraded mode
// (danger — content is not being scanned), the advisory policy and the
// stale database (warnings). Persistent by construction: re-read on every
// page, they go away by themselves when the state does.
func (rd *Renderer) scanBanners(v *View) []Banner {
	if rd.Scan == nil {
		return nil
	}
	s := rd.Scan()
	var out []Banner
	if s.Cause != nil {
		out = append(out, Banner{
			Kind: "danger",
			Code: string(s.Cause.Code()),
			Text: v.T("banner.scan_degraded", "Reason", taxonomy.Localize(v.lang, s.Cause).Cause),
		})
	}
	if !s.Blocking {
		out = append(out, Banner{Kind: "warning", Code: string(taxonomy.CodeScanPolicyAdvisory), Text: v.T("banner.scan_advisory")})
	}
	if s.Database != nil && s.MaxAge > 0 {
		if age := time.Since(s.Database.BuiltAt); age > s.MaxAge {
			out = append(out, Banner{
				Kind: "warning",
				Code: string(taxonomy.CodeScanDBStale),
				Text: v.T("banner.scan_db_stale", "Age", scan.FormatAge(age), "Max", scan.FormatAge(s.MaxAge)),
			})
		}
	}
	return out
}

// scanIndicatorView is the Retriever screen's scanning indicator: the
// policy, the scanner, and the database with its age and staleness — the
// "UI indicator" of R-13, shown whether or not anything is wrong.
type scanIndicatorView struct {
	Blocking  bool
	Available bool
	Scanner   scan.Info
	Origin    string
	HasDB     bool
	DBVersion string
	DBBuiltAt time.Time
	DBAge     string
	DBStale   bool
	MaxAge    string
	// CauseCode names the degraded mode; the banner above carries its
	// sentence.
	CauseCode string
}

func scanIndicatorOf(s *ScanState) *scanIndicatorView {
	iv := &scanIndicatorView{
		Blocking:  s.Blocking,
		Available: s.Scanner.Name != "",
		Scanner:   s.Scanner,
		Origin:    s.Origin,
		MaxAge:    scan.FormatAge(s.MaxAge),
	}
	if s.Database != nil {
		age := time.Since(s.Database.BuiltAt)
		iv.HasDB, iv.DBVersion, iv.DBBuiltAt = true, s.Database.Version, s.Database.BuiltAt
		iv.DBAge = scan.FormatAge(age)
		iv.DBStale = s.MaxAge > 0 && age > s.MaxAge
	}
	if s.Cause != nil {
		iv.CauseCode = string(s.Cause.Code())
	}
	return iv
}

// scanItemView is one item's scan report, localized.
type scanItemView struct {
	Verdict   string
	Mode      string
	ModeLevel string
	Threshold string
	Level     string
	Scanner   *scan.Info
	ScannedAt time.Time
	HasDB     bool
	DBVersion string
	DBAge     string
	DBStale   bool
	Counts    scan.Counts
	Findings  []findingRow
	More      int
	Applied   []scan.Exception
	Expired   []scan.Exception
	Reasons   []*ErrView
}

type findingRow struct {
	ID, Severity, Score, Package, Installed, Fixed string
	Violates, Excepted                             bool
}

func scanItemViewOf(lang string, r *scan.Report) *scanItemView {
	if r == nil {
		return nil
	}
	ev := &r.Policy
	v := &scanItemView{
		Verdict:   string(ev.Verdict),
		Mode:      string(ev.Policy.Mode),
		ModeLevel: string(ev.Policy.ModeLevel),
		Threshold: scan.FormatScore(ev.Policy.Threshold),
		Level:     string(ev.Policy.ThresholdLevel),
		Scanner:   r.Scanner,
		ScannedAt: r.ScannedAt,
		Counts:    r.Counts,
		Applied:   ev.ExceptionsApplied,
		Expired:   ev.ExceptionsExpired,
	}
	if r.Database != nil {
		v.HasDB, v.DBVersion, v.DBStale = true, r.Database.Version, r.Database.Stale
		v.DBAge = scan.FormatAge(time.Duration(r.Database.AgeSeconds) * time.Second)
	}
	violates := map[string]bool{}
	for _, id := range ev.Violations {
		violates[id] = true
	}
	excepted := map[string]bool{}
	for _, ex := range ev.ExceptionsApplied {
		excepted[ex.ID] = true
	}
	rows := make([]findingRow, 0, len(r.Findings))
	scores := make([]float64, 0, len(r.Findings))
	for i := range r.Findings {
		f := &r.Findings[i]
		row := findingRow{
			ID: f.ID, Severity: string(f.Severity), Package: f.Package,
			Installed: f.InstalledVersion, Fixed: f.FixedVersion,
			Violates: violates[f.ID], Excepted: excepted[f.ID],
		}
		score := -1.0
		if f.CVSS != nil {
			row.Score, score = scan.FormatScore(*f.CVSS), *f.CVSS
		} else if lb, ok := f.Severity.LowerBound(); ok {
			score = lb
		}
		rows = append(rows, row)
		scores = append(scores, score)
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	for _, i := range idx {
		if len(v.Findings) == maxFindingRows {
			v.More = len(rows) - maxFindingRows
			break
		}
		v.Findings = append(v.Findings, rows[i])
	}
	for _, rs := range ev.Reasons {
		params := taxonomy.Params{}
		for k, val := range rs.Params {
			params[k] = val
		}
		if _, ok := taxonomy.Lookup(rs.Code); !ok {
			continue
		}
		v.Reasons = append(v.Reasons, errView(lang, taxonomy.New(rs.Code, params)))
	}
	return v
}
