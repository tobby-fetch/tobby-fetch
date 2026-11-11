// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func f64(v float64) *float64 { return &v }

func combined(l *Levels) *Policy { p := Combine(l); return &p }

// TestCombineEveryLevelCombination walks the whole table of the FR-031
// amendment and its 2026-11-11 correction: instance mode × Retriever
// threshold (absent, lower, higher) × recipe annotation (absent, lower,
// equal, higher than what the two others produce).
func TestCombineEveryLevelCombination(t *testing.T) {
	type want struct {
		mode      Mode
		modeLevel Level
		threshold float64
		level     Level
		ignored   *float64
	}
	retrievers := []struct {
		name string
		v    *float64
	}{{"no retriever", nil}, {"retriever lower", f64(5)}, {"retriever higher", f64(8.5)}}
	recipes := []struct {
		name string
		// rel is the recipe value relative to what instance+retriever give.
		rel string
	}{{"no recipe", ""}, {"recipe lower", "lower"}, {"recipe equal", "equal"}, {"recipe higher", "higher"}}

	for _, mode := range []Mode{ModeWarn, ModeBlock} {
		for _, rt := range retrievers {
			for _, rc := range recipes {
				name := fmt.Sprintf("%s/%s/%s", mode, rt.name, rc.name)
				t.Run(name, func(t *testing.T) {
					l := Levels{InstanceMode: mode, InstanceThreshold: 7, RetrieverThreshold: rt.v}
					base, baseLevel := 7.0, LevelInstance
					if rt.v != nil {
						base, baseLevel = *rt.v, LevelRetriever
					}
					w := want{mode: mode, modeLevel: LevelInstance, threshold: base, level: baseLevel}
					if mode == ModeWarn && rt.v != nil {
						w.mode, w.modeLevel = ModeBlock, LevelRetriever
					}
					switch rc.rel {
					case "lower":
						l.RecipeThreshold = f64(base - 1)
						w.threshold, w.level = base-1, LevelRecipe
					case "equal":
						l.RecipeThreshold = f64(base)
					case "higher":
						l.RecipeThreshold = f64(base + 1)
						w.ignored = f64(base + 1)
					}
					if rc.rel != "" && w.mode == ModeWarn {
						w.mode, w.modeLevel = ModeBlock, LevelRecipe
					}

					p := Combine(&l)
					if p.Mode != w.mode || p.ModeLevel != w.modeLevel {
						t.Errorf("mode = %s set at %s; want %s set at %s", p.Mode, p.ModeLevel, w.mode, w.modeLevel)
					}
					if p.Threshold != w.threshold || p.ThresholdLevel != w.level {
						t.Errorf("threshold = %v set at %s; want %v set at %s", p.Threshold, p.ThresholdLevel, w.threshold, w.level)
					}
					switch {
					case (p.IgnoredRecipeThreshold == nil) != (w.ignored == nil):
						t.Errorf("ignored recipe threshold = %v; want %v", p.IgnoredRecipeThreshold, w.ignored)
					case w.ignored != nil && *p.IgnoredRecipeThreshold != *w.ignored:
						t.Errorf("ignored recipe threshold = %v; want %v", *p.IgnoredRecipeThreshold, *w.ignored)
					}
					// A recipe never relaxes: the threshold in effect is never
					// above what the instance and the Retriever produce.
					if p.Threshold > base {
						t.Errorf("the recipe raised the threshold to %v above %v", p.Threshold, base)
					}
				})
			}
		}
	}
}

func TestCombineUnknownModeFailsClosed(t *testing.T) {
	if p := Combine(&Levels{InstanceMode: "audit", InstanceThreshold: 7}); p.Mode != ModeBlock {
		t.Errorf("an unknown mode combined to %s, not block", p.Mode)
	}
}

func TestCombineExceptionsAddUp(t *testing.T) {
	p := Combine(&Levels{
		InstanceMode: ModeWarn, InstanceThreshold: 7,
		InstanceExceptions:  []Exception{{ID: "CVE-2024-0001"}, {ID: "CVE-2024-0002", Level: LevelRetriever}},
		RetrieverExceptions: []Exception{{ID: "CVE-2024-0003"}, {ID: "CVE-2024-0001"}},
	})
	got := make([]string, 0, len(p.Exceptions))
	for _, ex := range p.Exceptions {
		got = append(got, string(ex.Level)+":"+ex.ID)
	}
	want := []string{"instance:CVE-2024-0001", "instance:CVE-2024-0002", "retriever:CVE-2024-0003", "retriever:CVE-2024-0001"}
	if !slices.Equal(got, want) {
		t.Errorf("exceptions = %v; want %v (instance first, then Retriever, each stamped with its own level)", got, want)
	}
}

var (
	now  = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	subj = Subject{Reference: "registry.example/app@sha256:aa", Recipe: "app@1.0.0"}
)

func result(findings ...Finding) *Result {
	return &Result{
		Scanner:   Info{Name: "trivy", Version: "0.76.2"},
		ScannedAt: now.Add(-time.Minute),
		Database:  Database{Version: "2", Digest: "sha256:db", BuiltAt: now.Add(-48 * time.Hour)},
		Findings:  findings,
	}
}

func finding(id string, sev Severity, cvss *float64) Finding {
	return Finding{ID: id, Severity: sev, CVSS: cvss, Package: "pkg", InstalledVersion: "1"}
}

func codes(rs []Reason) []taxonomy.Code {
	out := make([]taxonomy.Code, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

// TestEvaluateScoring locks the scoring rules: inclusive threshold, CVSS
// first, the lower bound of the band without one (LOW is 0.1), and an
// UNKNOWN finding without a score never compared — but never dropped.
func TestEvaluateScoring(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		f         Finding
		violates  bool
		unscored  bool
	}{
		{"cvss above", 7, finding("CVE-1", SeverityMedium, f64(9.8)), true, false},
		{"cvss equal is inclusive", 7, finding("CVE-1", SeverityLow, f64(7)), true, false},
		{"cvss below despite critical severity", 7, finding("CVE-1", SeverityCritical, f64(6.9)), false, false},
		{"no cvss: critical sits at 9.0", 9, finding("CVE-1", SeverityCritical, nil), true, false},
		{"no cvss: high sits at 7.0", 7, finding("CVE-1", SeverityHigh, nil), true, false},
		{"no cvss: high below 7.1", 7.1, finding("CVE-1", SeverityHigh, nil), false, false},
		{"no cvss: medium sits at 4.0", 4, finding("CVE-1", SeverityMedium, nil), true, false},
		{"no cvss: low sits at 0.1", 0.1, finding("CVE-1", SeverityLow, nil), true, false},
		{"no cvss: low below 0.2", 0.2, finding("CVE-1", SeverityLow, nil), false, false},
		{"unknown without cvss is unscored, even at 0", 0, finding("CVE-1", SeverityUnknown, nil), false, true},
		{"unknown with cvss is scored", 7, finding("CVE-1", SeverityUnknown, f64(8)), true, false},
	}
	for _, mode := range []Mode{ModeWarn, ModeBlock} {
		for _, c := range cases {
			t.Run(string(mode)+"/"+c.name, func(t *testing.T) {
				p := Combine(&Levels{InstanceMode: mode, InstanceThreshold: c.threshold})
				rep := Evaluate(&p, subj, result(c.f), Freshness{MaxAge: 168 * time.Hour}, now)
				ev := rep.Policy
				if got := len(ev.Violations) == 1; got != c.violates {
					t.Fatalf("violations = %v; want violating=%v", ev.Violations, c.violates)
				}
				if got := len(ev.Unscored) == 1; got != c.unscored {
					t.Fatalf("unscored = %v; want unscored=%v", ev.Unscored, c.unscored)
				}
				want := VerdictPass
				if c.violates {
					want = VerdictWarn
					if mode == ModeBlock {
						want = VerdictBlock
					}
				}
				if ev.Verdict != want {
					t.Errorf("verdict = %s; want %s", ev.Verdict, want)
				}
				if rep.Blocks() != (want == VerdictBlock) {
					t.Errorf("Blocks() = %v for verdict %s", rep.Blocks(), want)
				}
				if c.unscored && !slices.Contains(codes(ev.Reasons), taxonomy.CodeScanUnscored) {
					t.Errorf("an unscored finding was not reported: %v", codes(ev.Reasons))
				}
				if len(rep.Findings) != 1 {
					t.Errorf("findings dropped from the report: %v", rep.Findings)
				}
			})
		}
	}
}

// TestEvaluateExceptions: an exception in force covers a violation and is
// named; an expired one no longer applies and is reported as expired;
// the instance and Retriever lists add up.
func TestEvaluateExceptions(t *testing.T) {
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		name       string
		instance   []Exception
		retriever  []Exception
		verdict    Verdict
		applied    []string
		expired    []string
		violations []string
	}{
		{"no exception", nil, nil, VerdictBlock, nil, nil, []string{"CVE-2024-0001"}},
		{"instance exception", []Exception{{ID: "CVE-2024-0001"}}, nil, VerdictPass, []string{"instance:CVE-2024-0001"}, nil, nil},
		{"retriever exception", nil, []Exception{{ID: "CVE-2024-0001", Justification: "not reachable"}}, VerdictPass, []string{"retriever:CVE-2024-0001"}, nil, nil},
		{"exception before its expiry", nil, []Exception{{ID: "CVE-2024-0001", Expires: future}}, VerdictPass, []string{"retriever:CVE-2024-0001"}, nil, nil},
		{"expired exception", nil, []Exception{{ID: "CVE-2024-0001", Expires: past}}, VerdictBlock, nil, []string{"retriever:CVE-2024-0001"}, []string{"CVE-2024-0001"}},
		{"expired at one level, in force at the other", []Exception{{ID: "CVE-2024-0001", Expires: past}}, []Exception{{ID: "CVE-2024-0001"}}, VerdictPass, []string{"retriever:CVE-2024-0001"}, nil, nil},
		{"exception for another CVE", []Exception{{ID: "CVE-2024-9999"}}, nil, VerdictBlock, nil, nil, []string{"CVE-2024-0001"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Combine(&Levels{InstanceMode: ModeBlock, InstanceThreshold: 7, InstanceExceptions: c.instance, RetrieverExceptions: c.retriever})
			rep := Evaluate(&p, subj, result(
				finding("CVE-2024-0001", SeverityCritical, f64(9.8)),
				finding("CVE-2024-0001", SeverityCritical, f64(9.8)), // the same CVE in a second package
				finding("CVE-2024-0002", SeverityLow, f64(3.1)),
			), Freshness{MaxAge: 168 * time.Hour}, now)
			ev := rep.Policy
			if ev.Verdict != c.verdict {
				t.Errorf("verdict = %s; want %s", ev.Verdict, c.verdict)
			}
			label := func(xs []Exception) []string {
				var out []string
				for _, x := range xs {
					out = append(out, string(x.Level)+":"+x.ID)
				}
				return out
			}
			if got := label(ev.ExceptionsApplied); !slices.Equal(got, c.applied) {
				t.Errorf("applied = %v; want %v", got, c.applied)
			}
			if got := label(ev.ExceptionsExpired); !slices.Equal(got, c.expired) {
				t.Errorf("expired = %v; want %v", got, c.expired)
			}
			if !slices.Equal(ev.Violations, c.violations) {
				t.Errorf("violations = %v; want %v", ev.Violations, c.violations)
			}
			if len(c.expired) > 0 && !slices.Contains(codes(ev.Reasons), taxonomy.CodeScanExceptionExpired) {
				t.Errorf("an expired exception was not reported: %v", codes(ev.Reasons))
			}
		})
	}
}

// TestEvaluateFreshness: the database age is in every report, recomputed
// at evaluation time; a stale database warns or refuses per the action.
func TestEvaluateFreshness(t *testing.T) {
	clean := result(finding("CVE-1", SeverityLow, f64(2)))
	for _, c := range []struct {
		name    string
		maxAge  time.Duration
		block   bool
		mode    Mode
		verdict Verdict
		stale   bool
	}{
		{"fresh", 72 * time.Hour, false, ModeWarn, VerdictPass, false},
		{"stale, warn action", 24 * time.Hour, false, ModeWarn, VerdictPass, true},
		{"stale, block action, advisory policy", 24 * time.Hour, true, ModeWarn, VerdictBlock, true},
		{"stale, block action, blocking policy", 24 * time.Hour, true, ModeBlock, VerdictBlock, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Combine(&Levels{InstanceMode: c.mode, InstanceThreshold: 7})
			rep := Evaluate(&p, subj, clean, Freshness{MaxAge: c.maxAge, Block: c.block}, now)
			if rep.Database == nil || rep.Database.AgeSeconds != int64(48*time.Hour/time.Second) {
				t.Fatalf("report database = %+v; want an age of 48h", rep.Database)
			}
			if rep.Database.Stale != c.stale {
				t.Errorf("stale = %v; want %v", rep.Database.Stale, c.stale)
			}
			if rep.Policy.Verdict != c.verdict {
				t.Errorf("verdict = %s; want %s", rep.Policy.Verdict, c.verdict)
			}
			if c.stale != slices.Contains(codes(rep.Policy.Reasons), taxonomy.CodeScanDBStale) {
				t.Errorf("stale note = %v; want present=%v", codes(rep.Policy.Reasons), c.stale)
			}
			if c.verdict == VerdictBlock {
				if e := rep.Refusal(); e == nil || e.Code() != taxonomy.CodeScanDBStale {
					t.Errorf("refusal = %v; want %s", e, taxonomy.CodeScanDBStale)
				}
			}
		})
	}
	// The cached Result is not mutated by an evaluation.
	if clean.Database.AgeSeconds != 0 || clean.Database.Stale {
		t.Error("Evaluate wrote into the Result it was given")
	}
}

// TestFutureDatabaseIsNeverFresh: a database claiming a build time beyond
// this instance's clock, past the tolerated skew, has no age to state —
// it used to read as 0 s old, fresh forever (B-048). It is stale, with a
// reason of its own, and refused under the block action; a timestamp
// within the skew is still just-built.
func TestFutureDatabaseIsNeverFresh(t *testing.T) {
	clean := result(finding("CVE-1", SeverityLow, f64(2)))
	p := Combine(&Levels{InstanceMode: ModeWarn, InstanceThreshold: 7})
	for _, c := range []struct {
		name  string
		ahead time.Duration
		stale bool
	}{
		{"within the clock skew", DBClockSkew / 2, false},
		{"a year ahead", 365 * 24 * time.Hour, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := *clean
			r.Database.BuiltAt = now.Add(c.ahead)
			for _, block := range []bool{false, true} {
				rep := Evaluate(&p, subj, &r, Freshness{MaxAge: 168 * time.Hour, Block: block}, now)
				if rep.Database.Stale != c.stale {
					t.Errorf("block=%v: stale = %v; want %v", block, rep.Database.Stale, c.stale)
				}
				if got := slices.Contains(codes(rep.Policy.Reasons), taxonomy.CodeScanDBFuture); got != c.stale {
					t.Errorf("block=%v: reasons = %v; want %s present=%v", block, codes(rep.Policy.Reasons), taxonomy.CodeScanDBFuture, c.stale)
				}
				wantVerdict := VerdictPass
				if c.stale && block {
					wantVerdict = VerdictBlock
				}
				if rep.Policy.Verdict != wantVerdict {
					t.Errorf("block=%v: verdict = %s; want %s", block, rep.Policy.Verdict, wantVerdict)
				}
				if wantVerdict == VerdictBlock {
					if e := rep.Refusal(); e == nil || e.Code() != taxonomy.CodeScanDBFuture {
						t.Errorf("refusal = %v; want %s", e, taxonomy.CodeScanDBFuture)
					}
				}
			}
		})
	}
}

// TestNotScannedBlocksOnlyUnderBlock: the degraded mode reports the item
// with its reason, and refuses it when — and only when — the policy in
// effect is blocking, whichever level made it so.
func TestNotScannedBlocksOnlyUnderBlock(t *testing.T) {
	cause := taxonomy.New(taxonomy.CodeScanDBMissing, taxonomy.Params{"repository": "ghcr.io/aquasecurity/trivy-db"})
	for _, c := range []struct {
		name   string
		levels Levels
		blocks bool
	}{
		{"advisory instance", Levels{InstanceMode: ModeWarn, InstanceThreshold: 7}, false},
		{"blocking instance", Levels{InstanceMode: ModeBlock, InstanceThreshold: 7}, true},
		{"advisory instance, Retriever threshold", Levels{InstanceMode: ModeWarn, InstanceThreshold: 7, RetrieverThreshold: f64(7)}, true},
		{"advisory instance, recipe threshold", Levels{InstanceMode: ModeWarn, InstanceThreshold: 7, RecipeThreshold: f64(5)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep := NotScanned(combined(&c.levels), subj, cause)
			if rep.Policy.Verdict != VerdictNotScanned || rep.Scanner != nil || rep.Database != nil {
				t.Fatalf("not-scanned report = %+v", rep)
			}
			if rep.Blocks() != c.blocks {
				t.Errorf("Blocks() = %v; want %v", rep.Blocks(), c.blocks)
			}
			if got := codes(rep.Policy.Reasons); len(got) == 0 || got[0] != taxonomy.CodeScanDBMissing {
				t.Errorf("reasons = %v; want the cause first", got)
			}
			if c.blocks && rep.Refusal().Code() != taxonomy.CodeScanDBMissing {
				t.Errorf("refusal = %v", rep.Refusal())
			}
		})
	}
	if rep := NotScanned(combined(&Levels{InstanceMode: ModeBlock, InstanceThreshold: 7}), subj, nil); rep.Refusal().Code() != taxonomy.CodeScanFailed {
		t.Errorf("a nil cause is not reported as a failed scan: %v", rep.Refusal())
	}
}

// TestEvaluateIgnoredAnnotationIsReported is the SRS acceptance: a recipe
// annotated "9.0" in a zone at 7.0 keeps 7.0, and the report says so.
func TestEvaluateIgnoredAnnotationIsReported(t *testing.T) {
	p := Combine(&Levels{InstanceMode: ModeBlock, InstanceThreshold: 7, RecipeThreshold: f64(9)})
	rep := Evaluate(&p, subj, result(finding("CVE-2024-0001", SeverityHigh, f64(7.5))), Freshness{}, now)
	if rep.Policy.Verdict != VerdictBlock {
		t.Errorf("verdict = %s; the zone's 7.0 must still apply", rep.Policy.Verdict)
	}
	var found bool
	for _, r := range rep.Policy.Reasons {
		if r.Code == taxonomy.CodeScanRecipeThresholdIgnored {
			found = r.Params["annotation"] == "9.0" && r.Params["threshold"] == "7.0" && r.Params["recipe"] == "app@1.0.0"
		}
	}
	if !found {
		t.Errorf("the ignored annotation is not reported with its parameters: %+v", rep.Policy.Reasons)
	}
	// And a recipe at 5.0 in that zone blocks a finding scored 6.1.
	p = Combine(&Levels{InstanceMode: ModeBlock, InstanceThreshold: 7, RecipeThreshold: f64(5)})
	rep = Evaluate(&p, subj, result(finding("CVE-2024-0002", SeverityMedium, f64(6.1))), Freshness{}, now)
	if rep.Policy.Verdict != VerdictBlock || rep.Policy.Policy.ThresholdLevel != LevelRecipe {
		t.Errorf("verdict = %s at level %s; want block at recipe", rep.Policy.Verdict, rep.Policy.Policy.ThresholdLevel)
	}
	if e := rep.Refusal(); e == nil || e.Code() != taxonomy.CodeScanPolicyBlocked {
		t.Errorf("refusal = %v", e)
	}
}

func TestReasonParametersAreCatalogued(t *testing.T) {
	// Every reason Evaluate and NotScanned emit carries exactly the
	// parameters its catalogue entry declares — the message renders.
	p := Combine(&Levels{InstanceMode: ModeBlock, InstanceThreshold: 7, RecipeThreshold: f64(9),
		RetrieverExceptions: []Exception{{ID: "CVE-2024-0001", Expires: now.Add(-time.Hour)}}})
	rep := Evaluate(&p, subj, result(
		finding("CVE-2024-0001", SeverityCritical, f64(9.8)),
		finding("CVE-2024-0003", SeverityUnknown, nil),
	), Freshness{MaxAge: time.Hour, Block: true}, now)
	for _, r := range rep.Policy.Reasons {
		entry, ok := taxonomy.Lookup(r.Code)
		if !ok {
			t.Fatalf("%s is not catalogued", r.Code)
		}
		for _, name := range entry.Params {
			if _, ok := r.Params[name]; !ok {
				t.Errorf("%s lacks parameter %q: %v", r.Code, name, r.Params)
			}
		}
		if len(r.Params) != len(entry.Params) {
			t.Errorf("%s carries %v; the catalogue declares %v", r.Code, r.Params, entry.Params)
		}
	}
	if len(rep.Policy.Reasons) != 5 {
		t.Errorf("reasons = %v; want blocked, stale, ignored, expired, unscored", codes(rep.Policy.Reasons))
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[float64]string{7: "7.0", 9.8: "9.8", 0.1: "0.1", 6.25: "6.25", 10: "10.0"} {
		if got := FormatScore(in); got != want {
			t.Errorf("FormatScore(%v) = %q; want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{
		-time.Second: "0s", 40 * time.Second: "40s", 5 * time.Minute: "5m",
		5*time.Hour + 12*time.Minute: "5h12m", 76 * time.Hour: "3d4h", 168 * time.Hour: "7d0h",
	} {
		if got := FormatAge(in); got != want {
			t.Errorf("FormatAge(%v) = %q; want %q", in, got, want)
		}
	}
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = fmt.Sprintf("CVE-%d", i)
	}
	if got := listed(ids); got != "CVE-0, CVE-1, CVE-2, CVE-3, CVE-4, CVE-5, CVE-6, CVE-7, CVE-8, CVE-9 (+2)" {
		t.Errorf("listed = %q", got)
	}
}
