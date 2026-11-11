// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// IndicativeThreshold is the threshold a blocking policy uses when no
// level states one (milestone-6 opening: "seuil indicatif 7.0"). The
// instance always states one — config.DefaultScanThreshold is the same
// value — so this only matters to a caller that builds Levels by hand.
const IndicativeThreshold = 7.0

// maxListed bounds the identifiers a reason parameter enumerates: a
// message names the first few and counts the rest, and the complete list
// lives in Violations / Unscored, which no sentence has to carry.
const maxListed = 10

// Combine resolves the three policy levels into the policy one recipe is
// evaluated against (FR-031 amendment 2026-11-11, RECIPE-SPEC §10.4).
//
//   - Threshold: the most specific level wins — recipe, then Retriever,
//     then instance — but the recipe can only LOWER the value the two
//     others produce. A higher recipe value is ignored and recorded in
//     IgnoredRecipeThreshold, so the report says so.
//   - Mode: the instance's, unless a Retriever or a recipe states a
//     threshold. A threshold posed for a perimeter is blocking for that
//     perimeter (opening correction, 2026-11-11: "refuse everything >=
//     7.0" is a refusal). A recipe can switch its own evaluation to
//     blocking, never back to advisory — and that holds even when its
//     value was ignored for being higher: the author asked for a refusal,
//     and the zone's lower threshold is the stricter way to give one.
//   - Exceptions: the instance list then the Retriever list, added up.
//     A recipe carries none.
func Combine(l *Levels) Policy {
	p := Policy{
		Mode:           l.InstanceMode,
		ModeLevel:      LevelInstance,
		Threshold:      l.InstanceThreshold,
		ThresholdLevel: LevelInstance,
	}
	if !p.Mode.Valid() {
		// An unknown mode never weakens the policy.
		p.Mode = ModeBlock
	}
	if l.RetrieverThreshold != nil {
		p.Threshold = *l.RetrieverThreshold
		p.ThresholdLevel = LevelRetriever
		if p.Mode == ModeWarn {
			p.Mode, p.ModeLevel = ModeBlock, LevelRetriever
		}
	}
	if l.RecipeThreshold != nil {
		switch rt := *l.RecipeThreshold; {
		case rt < p.Threshold:
			p.Threshold = rt
			p.ThresholdLevel = LevelRecipe
		case rt > p.Threshold:
			ignored := rt
			p.IgnoredRecipeThreshold = &ignored
		}
		if p.Mode == ModeWarn {
			p.Mode, p.ModeLevel = ModeBlock, LevelRecipe
		}
	}
	for _, ex := range l.InstanceExceptions {
		ex.Level = LevelInstance
		p.Exceptions = append(p.Exceptions, ex)
	}
	for _, ex := range l.RetrieverExceptions {
		ex.Level = LevelRetriever
		p.Exceptions = append(p.Exceptions, ex)
	}
	return p
}

// Blocking reports whether the policy refuses what it does not accept.
func (p *Policy) Blocking() bool { return p.Mode == ModeBlock }

// Subject names what one evaluation is about, for the reason parameters.
type Subject struct {
	// Reference is the item, "repository@digest" or "repository:tag".
	Reference string
	// Recipe is the recipe the item belongs to, "name@version".
	Recipe string
}

// Freshness is the R-13 bound on the database age (scan.maxDbAge and
// scan.maxDbAgeAction).
type Freshness struct {
	// MaxAge is the maximum accepted age; zero disables the check (only
	// ever in tests — the configuration refuses a zero bound).
	MaxAge time.Duration
	// Block refuses items scanned with a stale database, whatever the
	// policy mode says.
	Block bool
}

// Stale reports whether a database built at builtAt is beyond the bound
// at now.
func (f Freshness) Stale(builtAt, now time.Time) bool {
	return f.MaxAge > 0 && now.Sub(builtAt) > f.MaxAge
}

// Evaluate judges one scan Result against a combined Policy and returns
// the item's report (FR-031, FR-032 amendments):
//
//   - a finding's score is its CVSS base score, or the lower bound of its
//     severity band when no source scores it; UNKNOWN without a score is
//     never compared with the threshold and is listed as unscored;
//   - a finding at or above the threshold violates the policy unless an
//     exception that has not expired covers it; an expired exception that
//     matched is reported as such;
//   - violations make the verdict "block" under a blocking policy and
//     "warn" otherwise; a stale database adds its note, and refuses the
//     item when the freshness action is block.
//
// The database age is recomputed at now: a cached Result keeps the
// instant it was scanned at, but the age it reports is the age the
// operator reads the verdict at.
func Evaluate(p *Policy, subj Subject, r *Result, fresh Freshness, now time.Time) *Report {
	rep := &Report{
		ScannedAt: r.ScannedAt,
		Findings:  r.Findings,
	}
	scanner := r.Scanner
	rep.Scanner = &scanner
	db := r.Database
	db.AgeSeconds = max(0, int64(now.Sub(db.BuiltAt)/time.Second))
	db.Stale = fresh.Stale(db.BuiltAt, now)
	rep.Database = &db

	ev := Evaluation{Verdict: VerdictPass, Policy: *p}
	seenViolation := map[string]bool{}
	seenUnscored := map[string]bool{}
	seenApplied := map[string]bool{}
	seenExpired := map[string]bool{}
	for i := range r.Findings {
		f := &r.Findings[i]
		rep.Counts.Add(f.Severity)
		score, scored := f.Score()
		if !scored {
			if !seenUnscored[f.ID] {
				seenUnscored[f.ID] = true
				ev.Unscored = append(ev.Unscored, f.ID)
			}
			continue
		}
		if score < p.Threshold {
			continue
		}
		active, expired := p.match(f.ID, now)
		if active != nil {
			key := string(active.Level) + "/" + active.ID
			if !seenApplied[key] {
				seenApplied[key] = true
				ev.ExceptionsApplied = append(ev.ExceptionsApplied, *active)
			}
			continue
		}
		for _, ex := range expired {
			key := string(ex.Level) + "/" + ex.ID
			if !seenExpired[key] {
				seenExpired[key] = true
				ev.ExceptionsExpired = append(ev.ExceptionsExpired, ex)
			}
		}
		if !seenViolation[f.ID] {
			seenViolation[f.ID] = true
			ev.Violations = append(ev.Violations, f.ID)
		}
	}
	sort.Strings(ev.Violations)
	sort.Strings(ev.Unscored)

	if len(ev.Violations) > 0 {
		params := map[string]string{
			"reference":  subj.Reference,
			"violations": listed(ev.Violations),
			"threshold":  FormatScore(p.Threshold),
			"level":      string(p.ThresholdLevel),
		}
		if p.Blocking() {
			ev.Verdict = VerdictBlock
			ev.Reasons = append(ev.Reasons, Reason{Code: taxonomy.CodeScanPolicyBlocked, Params: params})
		} else {
			ev.Verdict = VerdictWarn
			ev.Reasons = append(ev.Reasons, Reason{Code: taxonomy.CodeScanPolicyAdvisory, Params: params})
		}
	}
	if db.Stale {
		ev.Reasons = append(ev.Reasons, Reason{Code: taxonomy.CodeScanDBStale, Params: map[string]string{
			"age": FormatAge(time.Duration(db.AgeSeconds) * time.Second),
			"max": FormatAge(fresh.MaxAge),
		}})
		if fresh.Block {
			ev.Verdict = VerdictBlock
		}
	}
	ev.Reasons = append(ev.Reasons, notes(p, subj, &ev)...)
	rep.Policy = ev
	return rep
}

// NotScanned is the report of an item no Result could be produced for:
// the degraded mode, never a silent one (FR-031). The reason is the
// scanner's taxonomy error; under a blocking policy the item is refused
// all the same (Report.Blocks).
func NotScanned(p *Policy, subj Subject, cause *taxonomy.Error) *Report {
	ev := Evaluation{Verdict: VerdictNotScanned, Policy: *p}
	if cause == nil {
		cause = taxonomy.New(taxonomy.CodeScanFailed, taxonomy.Params{"reference": subj.Reference, "detail": "no reason given"})
	}
	ev.Reasons = append(ev.Reasons, ReasonOf(cause))
	ev.Reasons = append(ev.Reasons, notes(p, subj, &ev)...)
	return &Report{Policy: ev}
}

// notes are the non-blocking reasons every verdict carries: an ignored
// recipe annotation, expired exceptions, unscored findings.
func notes(p *Policy, subj Subject, ev *Evaluation) []Reason {
	var out []Reason
	if p.IgnoredRecipeThreshold != nil {
		out = append(out, Reason{Code: taxonomy.CodeScanRecipeThresholdIgnored, Params: map[string]string{
			"recipe":     subj.Recipe,
			"annotation": FormatScore(*p.IgnoredRecipeThreshold),
			"threshold":  FormatScore(p.Threshold),
		}})
	}
	for _, ex := range ev.ExceptionsExpired {
		out = append(out, Reason{Code: taxonomy.CodeScanExceptionExpired, Params: map[string]string{
			"id":      ex.ID,
			"expires": ex.Expires.UTC().Format(time.RFC3339),
			"level":   string(ex.Level),
		}})
	}
	if len(ev.Unscored) > 0 {
		out = append(out, Reason{Code: taxonomy.CodeScanUnscored, Params: map[string]string{
			"reference": subj.Reference,
			"ids":       listed(ev.Unscored),
		}})
	}
	return out
}

// match finds the exceptions naming id: the first one still in force, or
// else every one that matched but has expired.
func (p *Policy) match(id string, now time.Time) (active *Exception, expired []Exception) {
	for i := range p.Exceptions {
		ex := &p.Exceptions[i]
		if ex.ID != id {
			continue
		}
		if ex.ExpiredAt(now) {
			expired = append(expired, *ex)
			continue
		}
		return ex, nil
	}
	return nil, expired
}

// Score is the number the policy compares with its threshold: the CVSS
// base score when one is known, the lower bound of the severity band
// otherwise; ok is false for an unscored UNKNOWN finding.
func (f *Finding) Score() (score float64, ok bool) {
	if f.CVSS != nil {
		return *f.CVSS, true
	}
	return f.Severity.LowerBound()
}

// Blocks reports whether the item this report describes must not be
// pushed: a block verdict, or an item that could not be scanned under a
// blocking policy (FR-031: no configuration path relaxes security
// implicitly).
func (r *Report) Blocks() bool {
	if r == nil {
		return false
	}
	switch r.Policy.Verdict {
	case VerdictBlock:
		return true
	case VerdictNotScanned:
		return r.Policy.Policy.Blocking()
	}
	return false
}

// Refusal is the taxonomy error a blocked item fails with: the policy
// refusal when findings violate a blocking policy, the not-scanned reason
// when no result exists, the stale-database refusal when only the
// database age refuses it. Nil when the report does not block.
func (r *Report) Refusal() *taxonomy.Error {
	if !r.Blocks() {
		return nil
	}
	pick := func(code taxonomy.Code) *taxonomy.Error {
		for _, rs := range r.Policy.Reasons {
			if rs.Code == code {
				params := taxonomy.Params{}
				for k, v := range rs.Params {
					params[k] = v
				}
				return taxonomy.New(rs.Code, params)
			}
		}
		return nil
	}
	if r.Policy.Verdict == VerdictNotScanned {
		for _, code := range []taxonomy.Code{taxonomy.CodeScannerUnavailable, taxonomy.CodeScannerUnverified, taxonomy.CodeScanDBMissing, taxonomy.CodeScanFailed} {
			if e := pick(code); e != nil {
				return e
			}
		}
	}
	if e := pick(taxonomy.CodeScanPolicyBlocked); e != nil {
		return e
	}
	if e := pick(taxonomy.CodeScanDBStale); e != nil {
		return e
	}
	return taxonomy.New(taxonomy.CodeScanFailed, taxonomy.Params{"reference": "", "detail": "refused without a recorded reason"})
}

// ReasonOf turns a taxonomy error into a persisted reason: its code and
// its parameters rendered as strings.
func ReasonOf(e *taxonomy.Error) Reason {
	rs := Reason{Code: e.Code()}
	if params := e.ParamsMap(); len(params) > 0 {
		rs.Params = make(map[string]string, len(params))
		for k, v := range params {
			rs.Params[k] = fmt.Sprint(v)
		}
	}
	return rs
}

// FormatScore renders a CVSS score the way an operator writes one: "7.0",
// "9.8", "6.25".
func FormatScore(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// FormatAge renders a database age compactly: "3d4h", "5h12m", "40s".
func FormatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}

// listed renders identifiers for a message: the first few, then a count.
func listed(ids []string) string {
	if len(ids) <= maxListed {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s (+%d)", strings.Join(ids[:maxListed], ", "), len(ids)-maxListed)
}
