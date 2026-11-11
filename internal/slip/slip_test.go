// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package slip

import (
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

var t0 = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// The mention R-07 makes mandatory, as the catalogs must state it. They are
// written out here a second time on purpose: the test pins the wording the
// requirement fixes, so softening a catalog entry fails it.
var notices = map[string]string{
	"en": "Unsigned aid document — not proof of authenticity",
	"fr": "Document d'aide non signé — ne vaut pas preuve d'authenticité",
}

func f64(v float64) *float64 { return &v }

// fixture draws a destination-side slip with everything in it: a medium of
// three deliveries (one admitted under a relaxed scope, one blocked on a
// digest), a verdict, and scan reports from two tasks.
func fixture() *Input {
	relaxed := assurance.ForUnsigned("lab-scope")
	manifest := &media.Manifest{
		MediaID: "med_0123456789ab", Zone: "d1", MediaFormat: 1, StoreFormat: 1,
		ProducedBy: media.Producer{Version: "0.6.0", RunID: "run_src_1"},
		ResolvedAt: t0.Add(-26 * time.Hour), WrittenAt: t0.Add(-25 * time.Hour),
		Totals: media.Totals{Files: 321, Bytes: 5 << 30},
		Recipes: []media.Recipe{
			{Name: "wordpress", Version: "6.8.2", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ResolvedAt: t0.Add(-26 * time.Hour),
				Ingredients: []media.Ingredient{{Name: "app", Kind: "ContainerImage", Tag: "6.8.2", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}},
			{Name: "lab-tools", Version: "1.0.0", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ResolvedAt: t0.Add(-26 * time.Hour),
				Ingredients: []media.Ingredient{{Name: "cli", Kind: "ContainerImage", Tag: "1.0.0", Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}},
				Assurance:   relaxed},
			{Name: "broken", Version: "0.1.0", Digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", ResolvedAt: t0.Add(-26 * time.Hour)},
		},
	}
	report := &media.Report{
		Verdict: media.VerdictPartial,
		Zone:    media.ZoneCheck{Expected: "d1", Found: "d1", Match: true},
		Freshness: &media.FreshnessCheck{
			Resolved: manifest.ResolvedAt, Recorded: t0.Add(-72 * time.Hour),
		},
		Checked:   media.Totals{Files: 321, Bytes: 5 << 30},
		StartedAt: t0.Add(-10 * time.Minute), FinishedAt: t0.Add(-5 * time.Minute),
		Recipes: []media.RecipeVerdict{
			{Name: "wordpress", Version: "6.8.2", Pushable: true, KeyFingerprint: "SHA256:zoneKeyA", Files: 100, Bytes: 1 << 30},
			{Name: "lab-tools", Version: "1.0.0", Pushable: true, Unsigned: true, TrustScope: "lab-scope",
				Assurance: relaxed, SourceAssurance: relaxed, Files: 20, Bytes: 1 << 20},
			{Name: "broken", Version: "0.1.0", Pushable: false,
				Reason: &media.Reason{Code: taxonomy.CodeMediaFileDigest, Path: "docker/registry/v2/blobs/sha256/ab/abcd/data"}},
		},
		Findings: []media.Finding{{Code: taxonomy.CodeMediaUncovered, Path: "stray.bin"}},
	}

	scanned := func(findings []scan.Finding, counts scan.Counts, mode scan.Mode, stale bool) *scan.Report {
		return &scan.Report{
			Scanner:   &scan.Info{Name: "trivy", Version: "0.76.2"},
			ScannedAt: t0.Add(-2 * time.Hour),
			Database:  &scan.Database{Version: "2", BuiltAt: t0.Add(-50 * time.Hour), AgeSeconds: 50 * 3600, Stale: stale},
			Counts:    counts, Findings: findings,
			Policy: scan.Evaluation{
				Verdict:    scan.VerdictWarn,
				Policy:     scan.Policy{Mode: mode, Threshold: 7.0, ThresholdLevel: scan.LevelInstance},
				Violations: []string{"CVE-2026-0001"},
			},
		}
	}
	oldTask := &tasks.Task{ID: "tsk_old", Type: tasks.TypeSync, Created: t0.Add(-48 * time.Hour), Items: []tasks.Item{
		{Name: "wordpress@6.8.2/app", Scan: scanned(nil, scan.Counts{Critical: 99}, scan.ModeWarn, false)},
	}}
	newTask := &tasks.Task{ID: "tsk_new", Type: tasks.TypeSync, Created: t0.Add(-3 * time.Hour), Items: []tasks.Item{
		{Name: "wordpress@6.8.2/app", Scan: scanned([]scan.Finding{
			{ID: "CVE-2026-0002", Severity: scan.SeverityMedium, Package: "zlib", InstalledVersion: "1.0"},
			{ID: "CVE-2026-0001", Severity: scan.SeverityCritical, CVSS: f64(9.8), Package: "openssl", InstalledVersion: "3.0.1", FixedVersion: "3.0.2"},
		}, scan.Counts{Critical: 1, Medium: 1}, scan.ModeWarn, false)},
		{Name: "lab-tools@1.0.0/cli", Assurance: relaxed, Scan: scanned(nil, scan.Counts{}, scan.ModeBlock, true)},
		{Name: "push/wordpress@6.8.2/app"}, // not a scanned element
	}}

	return &Input{
		Version: "0.6.0", Now: t0, Zone: "d1", Root: "/srv/tobby/store",
		Manifest: manifest, Report: report,
		LastImport: &media.ImportRecord{MediaID: "med_previous", ResolvedAt: t0.Add(-72 * time.Hour), ImportedAt: t0.Add(-70 * time.Hour), RunID: "run_dst_0"},
		Tasks:      []*tasks.Task{oldTask, newTask},
		Scan: ScanContext{
			Blocking: false, MaxAge: 168 * time.Hour,
			Database: &scan.Database{Version: "2", BuiltAt: t0.Add(-50 * time.Hour)},
		},
	}
}

// TestCatalogsComplete: a key in one language and not the other fails the
// build (FR-063).
func TestCatalogsComplete(t *testing.T) {
	en, err := messageIDs("en")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := messageIDs("fr")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(en)
	sort.Strings(fr)
	if strings.Join(en, "\n") != strings.Join(fr, "\n") {
		inEN := map[string]bool{}
		for _, k := range en {
			inEN[k] = true
		}
		for _, k := range fr {
			if !inEN[k] {
				t.Errorf("key %q is in fr.yaml only", k)
			}
			delete(inEN, k)
		}
		for k := range inEN {
			t.Errorf("key %q is in en.yaml only", k)
		}
	}
}

// TestEveryViewKeyResolves renders the richest slip in both languages and
// fails on any bracketed key: a template key missing from a catalog shows
// as "[key]" and must never reach a printed page.
func TestEveryViewKeyResolves(t *testing.T) {
	s := Build(fixture())
	for _, lang := range Languages() {
		out := string(s.Text(lang))
		if m := regexp.MustCompile(`\[[a-z_]+(\.[a-zA-Z_-]+)+\]`).FindString(out); m != "" {
			t.Errorf("%s: an unresolved catalog key reached the output: %s", lang, m)
		}
	}
}

// TestNoticeOpensAndClosesEveryRendering is the R-07 mention, in both
// formats and both languages: the unsigned-aid notice is the first thing
// and the last thing, so a printed page cannot lose it by being cut.
func TestNoticeOpensAndClosesEveryRendering(t *testing.T) {
	for _, in := range []*Input{fixture(), {Version: "0.6.0", Now: t0, NoMedium: true}} {
		s := Build(in)
		for _, lang := range Languages() {
			want := notices[lang]

			text := string(s.Text(lang))
			if n := strings.Count(text, strings.ToUpper(want)); n != 2 {
				t.Errorf("text/%s: notice appears %d times, want 2 (head and foot)", lang, n)
			}
			first := strings.Index(text, strings.ToUpper(want))
			last := strings.LastIndex(text, strings.ToUpper(want))
			if first > 400 {
				t.Errorf("text/%s: the notice starts at byte %d: it is not at the head", lang, first)
			}
			if tail := text[last:]; len(tail) > 700 {
				t.Errorf("text/%s: %d bytes follow the closing notice: it is not at the foot", lang, len(tail))
			}

			raw, err := s.HTML(lang)
			if err != nil {
				t.Fatal(err)
			}
			page := string(raw)
			if n := strings.Count(page, html.EscapeString(want)); n != 2 {
				t.Errorf("html/%s: notice appears %d times, want 2", lang, n)
			}
			if i, j := strings.Index(page, `id="notice-top"`), strings.Index(page, "<h2>"); i < 0 || j < 0 || i > j {
				t.Errorf("html/%s: the top notice does not precede the first section", lang)
			}
			if i, j := strings.Index(page, `id="notice-bottom"`), strings.LastIndex(page, "</h2>"); i < 0 || j < 0 || i < j {
				t.Errorf("html/%s: the bottom notice does not follow the last section", lang)
			}
		}
	}
}

// TestHTMLIsAutonomous: no external resource, no script, no link; the CSS
// is inline and covered by the document's own policy.
func TestHTMLIsAutonomous(t *testing.T) {
	raw, err := Build(fixture()).HTML("en")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	for _, banned := range []string{"<script", "<link", "<img", "<iframe", "<object", "<embed", "@import", "url(", "src=", "http://", "https://"} {
		if strings.Contains(strings.ToLower(page), banned) {
			t.Errorf("the HTML contains %q: it must load nothing", banned)
		}
	}
	if !strings.Contains(page, "<style>") || !strings.Contains(page, "@media print") {
		t.Error("the print stylesheet is not inline")
	}
	if !strings.Contains(page, `http-equiv="Content-Security-Policy"`) || !strings.Contains(page, "default-src &#39;none&#39;") {
		t.Error("the document does not carry its own policy")
	}
	if !strings.Contains(CSP(), cssHash) {
		t.Error("the header policy does not name the stylesheet's hash")
	}
}

// TestHTMLEscapesHostileValues: a manifest is another zone's unsigned
// claim, and its strings are displayed.
func TestHTMLEscapesHostileValues(t *testing.T) {
	in := fixture()
	in.Manifest.Recipes[0].Name = `<script>alert(1)</script>`
	raw, err := Build(in).HTML("en")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "<script>alert") {
		t.Error("a recipe name reached the document unescaped")
	}
}

// TestContentOfBothFormats checks the facts R-07 lists are in the slip:
// zone identity, freshness, inventory, verification, scan per recipe and
// element with database age and mode, and the assurance markers.
func TestContentOfBothFormats(t *testing.T) {
	s := Build(fixture())
	for _, lang := range Languages() {
		raw, err := s.HTML(lang)
		if err != nil {
			t.Fatal(err)
		}
		pages := map[string]string{"html": plainOf(string(raw)), "text": string(s.Text(lang))}
		common := []string{
			"med_0123456789ab", "d1", "5 ", // identity, zone, volumes
			"med_previous",                                       // freshness: the last import
			"wordpress@6.8.2", "lab-tools@1.0.0", "broken@0.1.0", // inventory and verdicts
			"SHA256:zoneKeyA", // the key that verified a signature
			"stray.bin",       // a finding
			"docker/registry/v2/blobs/sha256/ab/abcd/data", // the offending file
			"CVE-2026-0001", "openssl", "3.0.2", // scan findings
			"2d2h",      // database age, 50h
			"lab-scope", // the assurance scope
			"run_src_1", "0.6.0",
		}
		for kind, page := range pages {
			for _, want := range common {
				if !strings.Contains(page, want) {
					t.Errorf("%s/%s: the slip does not contain %q", kind, lang, want)
				}
			}
		}
		mode := map[string][2]string{"en": {"advisory", "blocking"}, "fr": {"consultatif", "bloquant"}}[lang]
		for kind, page := range pages {
			for _, want := range mode {
				if !strings.Contains(page, want) {
					t.Errorf("%s/%s: scan mode %q is not stated", kind, lang, want)
				}
			}
		}
		reduced := map[string]string{"en": "REDUCED", "fr": "RÉDUITE"}[lang]
		for kind, page := range pages {
			if n := strings.Count(page, reduced); n < 3 {
				t.Errorf("%s/%s: the reduced-assurance marker appears %d times, want it beside every use of the recipe", kind, lang, n)
			}
		}
	}
}

// TestHTMLAndTextCarryTheSameFacts: every table cell and definition value
// of the view is in both formats — they are two layouts of one view.
func TestHTMLAndTextCarryTheSameFacts(t *testing.T) {
	s := Build(fixture())
	for _, lang := range Languages() {
		raw, _ := s.HTML(lang)
		page := plainOf(string(raw))
		text := strings.Join(strings.Fields(string(s.Text(lang))), " ")
		d := s.view(lang)
		var walk func(sec *section)
		walk = func(sec *section) {
			for _, b := range sec.Blocks {
				switch x := b.(type) {
				case *kvBlock:
					for _, r := range x.Rows {
						need(t, lang, page, text, r.Label)
						need(t, lang, page, text, r.Val.Text)
					}
				case *tableBlock:
					for _, r := range x.Rows {
						for _, cl := range r {
							need(t, lang, page, text, cl.Text)
						}
					}
				case *paraBlock:
					need(t, lang, page, text, x.Text)
				case *listBlock:
					for _, it := range x.Items {
						need(t, lang, page, text, it.Text)
					}
				case *section:
					walk(x)
				}
			}
		}
		for _, sec := range d.Sections {
			walk(sec)
		}
	}
}

// need checks a view string is in the HTML (modulo whitespace) and in the
// text (modulo wrapping, which only changes whitespace).
func need(t *testing.T, lang, page, text, s string) {
	t.Helper()
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return
	}
	if !strings.Contains(page, s) {
		t.Errorf("html/%s lacks %q", lang, s)
	}
	if !strings.Contains(text, s) {
		t.Errorf("text/%s lacks %q", lang, s)
	}
}

var tagRe = regexp.MustCompile(`(?s)<style>.*?</style>|<[^>]+>`)

// plainOf reduces an HTML page to its whitespace-normalized text.
func plainOf(page string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(page, " "))), " ")
}

// TestLatestTaskWinsPerElement: the report of the most recent task that
// scanned an element is the one shown; reports of elements that are not
// on the medium are not.
func TestLatestTaskWinsPerElement(t *testing.T) {
	s := Build(fixture())
	var wp *ScanRecipe
	for i := range s.Scan.Recipes {
		if s.Scan.Recipes[i].ID == "wordpress@6.8.2" {
			wp = &s.Scan.Recipes[i]
		}
	}
	if wp == nil || len(wp.Items) != 1 {
		t.Fatalf("wordpress scan section = %+v", wp)
	}
	it := wp.Items[0]
	if it.TaskID != "tsk_new" || it.Counts.Critical != 1 {
		t.Errorf("the element shows task %s with %d critical, want the newer task's report", it.TaskID, it.Counts.Critical)
	}
	if len(it.Findings) != 2 || it.Findings[0].ID != "CVE-2026-0001" {
		t.Errorf("findings are not worst-first: %+v", it.Findings)
	}
	// "broken" is on the medium and was never scanned: listed, empty.
	var broken *ScanRecipe
	for i := range s.Scan.Recipes {
		if s.Scan.Recipes[i].ID == "broken@0.1.0" {
			broken = &s.Scan.Recipes[i]
		}
	}
	if broken == nil || len(broken.Items) != 0 {
		t.Errorf("a recipe without a scan report must be listed, empty: %+v", broken)
	}
	for _, r := range s.Scan.Recipes {
		for _, it := range r.Items {
			if strings.HasPrefix(it.Element, "app") && r.ID != "wordpress@6.8.2" {
				t.Errorf("element %s attributed to %s", it.Element, r.ID)
			}
		}
	}
}

// TestStagesFollowTheMediaScreen: the three stages derive from the report
// the way the Media screen derives them.
func TestStagesFollowTheMediaScreen(t *testing.T) {
	s := Build(fixture())
	got := map[string]string{}
	for _, st := range s.Verification.Stages {
		got[st.Key] = st.State
	}
	want := map[string]string{StageManifest: StatePassed, StageDigests: StatePartial, StageSignatures: StatePassed}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("stage %s = %q, want %q", k, got[k], w)
		}
	}
	var broken *Delivery
	for i := range s.Verification.Recipes {
		if s.Verification.Recipes[i].ID == "broken@0.1.0" {
			broken = &s.Verification.Recipes[i]
		}
	}
	if broken == nil || broken.Content != StateFailed || broken.Signature != StateNotReached {
		t.Errorf("a content failure never reaches its signature: %+v", broken)
	}
}

// TestAssuranceCollected: the relaxed scope is listed once per origin and
// the instance's own verdict is distinct from the source's claim.
func TestAssuranceCollected(t *testing.T) {
	s := Build(fixture())
	origins := map[string]bool{}
	for _, a := range s.Assurance {
		if a.Recipe == "lab-tools@1.0.0" {
			origins[a.Origin] = true
		}
	}
	for _, want := range []string{OriginInstance, OriginSourceClaim, OriginScan} {
		if !origins[want] {
			t.Errorf("assurance origin %q is missing: %+v", want, s.Assurance)
		}
	}
	// Full assurance everywhere: the section says so.
	in := fixture()
	in.Manifest.Recipes[1].Assurance = nil
	in.Report.Recipes[1].Assurance, in.Report.Recipes[1].SourceAssurance = nil, nil
	in.Tasks[1].Items[1].Assurance = nil
	if got := Build(in).Assurance; len(got) != 0 {
		t.Errorf("assurance = %+v, want none", got)
	}
}

// TestSourceSideWithoutMedium: a fresh store is explained, not alarmed
// over, and the slip still carries the notice and a scan policy.
func TestSourceSideWithoutMedium(t *testing.T) {
	s := Build(&Input{Version: "0.6.0", Now: t0, NoMedium: true, Scan: ScanContext{Blocking: true}})
	if s.Side != SideSource || s.Medium != nil || s.Signed {
		t.Fatalf("slip = %+v", s)
	}
	out := string(s.Text("en"))
	if !strings.Contains(out, "never been made into a medium") {
		t.Errorf("a fresh store is not explained:\n%s", out)
	}
}

// TestJSONSaysUnsigned: the machine form cannot be mistaken for evidence.
func TestJSONSaysUnsigned(t *testing.T) {
	raw, err := json.Marshal(Build(fixture()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"signed":false`) {
		t.Errorf("the JSON form does not state it is unsigned: %s", raw)
	}
}
