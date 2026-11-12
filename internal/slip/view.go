// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package slip

import (
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/ui/format"
)

// The slip's own catalogs. They are separate from the interface's because
// the slip is also rendered where no interface exists (the API, the
// command line), and a document that leaves the machine must not depend on
// the web UI's label set. FR-063 holds all the same: every sentence is a
// complete key with named variables, and the completeness test diffs the
// two languages.
//
//go:embed i18n/en.yaml i18n/fr.yaml
var labelsFS embed.FS

var bundle = func() *i18n.Bundle {
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)
	for _, f := range []string{"i18n/en.yaml", "i18n/fr.yaml"} {
		if _, err := b.LoadMessageFileFS(labelsFS, f); err != nil {
			panic(fmt.Sprintf("slip: loading %s: %v", f, err))
		}
	}
	return b
}()

// Languages lists the languages a slip can be drawn in, English first.
func Languages() []string { return []string{"en", "fr"} }

// NormalizeLang maps any tag to a language the slip is drawn in; unknown
// ones fall back to English, as the rest of the product does.
func NormalizeLang(lang string) string {
	if strings.HasPrefix(strings.ToLower(lang), "fr") {
		return "fr"
	}
	return "en"
}

// translator resolves catalog keys in one language.
type translator struct{ loc *i18n.Localizer }

// t resolves key with named variables given as alternating name, value.
// A missing key renders as the bracketed key — loud, and kept out of
// releases by the completeness test.
func (t translator) t(key string, kv ...any) string {
	data := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		data[fmt.Sprint(kv[i])] = kv[i+1]
	}
	s, err := t.loc.Localize(&i18n.LocalizeConfig{MessageID: key, TemplateData: data})
	if err != nil {
		return "[" + key + "]"
	}
	return s
}

// messageIDs lists every message id of one embedded catalog.
func messageIDs(lang string) ([]string, error) {
	raw, err := labelsFS.ReadFile("i18n/" + lang + ".yaml")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(m))
	for k := range m {
		ids = append(ids, k)
	}
	return ids, nil
}

// The intermediate view. Both renderers walk it, which is what keeps the
// HTML and the text saying the same thing: a sentence exists once, here,
// localized, and the formats only decide how to lay it out.

// Tones colour a cell in HTML; the text carries the same information in
// the words themselves (UPPERCASE for what needs attention), so a slip
// printed in black and white loses nothing.
const (
	toneNone = ""
	toneOK   = "ok"
	toneWarn = "warn"
	toneBad  = "bad"
	toneMute = "mute"
)

type cell struct{ Text, Tone string }

type kvRow struct {
	Label string
	Val   cell
}

type kvBlock struct{ Rows []kvRow }

type tableBlock struct {
	Caption string
	Head    []string
	Rows    [][]cell
}

type paraBlock struct {
	Text string
	Tone string
}

type listBlock struct{ Items []cell }

type section struct {
	Title string
	// Level is 2 for a section, 3 for a sub-section.
	Level  int
	Blocks []any
}

type doc struct {
	Lang        string
	Title       string
	Subtitle    string
	Generated   string
	NoticeTitle string
	NoticeBody  string
	Sections    []*section
	Footer      string
}

func (s *section) kv(rows ...kvRow) {
	if len(rows) > 0 {
		s.Blocks = append(s.Blocks, &kvBlock{Rows: rows})
	}
}
func (s *section) para(text, tone string) {
	s.Blocks = append(s.Blocks, &paraBlock{Text: text, Tone: tone})
}
func (s *section) list(items ...cell) {
	if len(items) > 0 {
		s.Blocks = append(s.Blocks, &listBlock{Items: items})
	}
}
func (s *section) table(caption string, head []string, rows [][]cell) {
	if len(rows) > 0 {
		s.Blocks = append(s.Blocks, &tableBlock{Caption: caption, Head: head, Rows: rows})
	}
}

func row(label, text string) kvRow { return kvRow{Label: label, Val: cell{Text: text}} }
func rowTone(label, text, tone string) kvRow {
	return kvRow{Label: label, Val: cell{Text: text, Tone: tone}}
}
func c(text string) cell        { return cell{Text: text} }
func ct(text, tone string) cell { return cell{Text: text, Tone: tone} }

// view localizes the slip into the intermediate document.
func (s *Slip) view(lang string) *doc {
	lang = NormalizeLang(lang)
	tr := translator{loc: i18n.NewLocalizer(bundle, lang)}
	v := &viewer{s: s, lang: lang, tr: tr}
	return v.build()
}

type viewer struct {
	s    *Slip
	lang string
	tr   translator
}

func (v *viewer) bytes(n int64) string { return format.Bytes(v.lang, n) }

// when renders a timestamp with how long ago it was, relative to the
// drawing time.
func (v *viewer) when(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return v.tr.t("resolved_ago", "At", format.Time(t), "Ago", format.Ago(v.lang, t, v.s.GeneratedAt))
}

// problem renders a taxonomy entry as one localized sentence.
func (v *viewer) problem(p *Problem) string {
	if p == nil {
		return ""
	}
	code := p.Code
	if _, ok := taxonomy.Lookup(code); !ok {
		code = taxonomy.CodeInternal
	}
	params := taxonomy.Params{}
	for k, val := range p.Params {
		params[k] = val
	}
	m := taxonomy.Localize(v.lang, taxonomy.New(code, params))
	parts := []string{string(p.Code) + " — " + strings.TrimSpace(m.What)}
	for _, extra := range []string{m.Cause, m.Action} {
		if extra = strings.TrimSpace(extra); extra != "" {
			parts = append(parts, extra)
		}
	}
	return strings.Join(parts, " ")
}

// marker renders an assurance marker, or the full-assurance word.
func (v *viewer) marker(m *assurance.Marker) (text, tone string) {
	if !m.Reduced() {
		return v.tr.t("assurance.full"), toneNone
	}
	reasons := make([]string, 0, len(m.Reasons))
	for _, r := range m.Reasons {
		reasons = append(reasons, v.tr.t("assurance.reason_"+string(r)))
	}
	return v.tr.t("assurance.reduced", "Scope", m.Scope, "Reasons", strings.Join(reasons, ", ")), toneWarn
}

func (v *viewer) build() *doc {
	s, t := v.s, v.tr
	d := &doc{
		Lang:        v.lang,
		Title:       t.t("title"),
		NoticeTitle: t.t("notice.title"),
		NoticeBody:  t.t("notice.body"),
		Footer:      t.t("footer.generated", "At", format.Time(s.GeneratedAt), "Version", s.Version),
		Generated:   t.t("generated", "At", format.Time(s.GeneratedAt), "Version", s.Version),
	}
	if s.Side == SideDestination {
		d.Subtitle = t.t("subtitle_destination", "Zone", s.Zone)
	} else {
		d.Subtitle = t.t("subtitle_source")
	}
	d.Sections = append(d.Sections, v.medium(), v.freshness(), v.inventory(), v.verification(), v.scan(), v.assurance())
	return d
}

func (v *viewer) medium() *section {
	s, t := v.s, v.tr
	sec := &section{Title: t.t("section.medium"), Level: 2}
	side := t.t("side.source")
	if s.Side == SideDestination {
		side = t.t("side.destination")
	}
	rows := []kvRow{row(t.t("f.side"), side)}
	if s.Side == SideDestination {
		rows = append(rows, row(t.t("f.zone_served"), s.Zone))
	}
	m := s.Medium
	if m != nil {
		rows = append(rows,
			row(t.t("f.media_id"), m.MediaID),
			row(t.t("f.zone_addressed"), m.Zone),
			row(t.t("f.resolved"), v.when(m.ResolvedAt)),
			row(t.t("f.written"), v.when(m.WrittenAt)),
			row(t.t("f.produced_by"), t.t("produced_by", "Version", m.ProducedBy.Version)),
		)
		if m.ProducedBy.RunID != "" {
			rows = append(rows, row(t.t("f.run"), m.ProducedBy.RunID))
		}
		rows = append(rows, row(t.t("f.volumes"), t.t("volumes", "Files", m.Totals.Files, "Size", v.bytes(m.Totals.Bytes))))
	}
	if s.Root != "" {
		rows = append(rows, row(t.t("f.root"), s.Root))
	}
	sec.kv(rows...)
	switch {
	case m != nil:
		sec.para(t.t("medium.unsigned"), toneMute)
	case s.MediumErr != nil:
		sec.para(v.problem(s.MediumErr), toneBad)
	case s.Side == SideDestination:
		sec.para(t.t("medium.none_destination"), toneBad)
	default:
		sec.para(t.t("medium.none"), toneMute)
	}
	return sec
}

func (v *viewer) freshness() *section {
	s, t := v.s, v.tr
	sec := &section{Title: t.t("section.freshness"), Level: 2}
	var rows []kvRow
	if s.Side == SideDestination {
		if s.LastImport == nil {
			rows = append(rows, row(t.t("f.last_import_media"), t.t("freshness.no_import")))
		} else {
			li := s.LastImport
			rows = append(rows,
				row(t.t("f.last_import_media"), li.MediaID),
				row(t.t("f.last_import_resolved"), v.when(li.ResolvedAt)),
				row(t.t("f.last_import_at"), v.when(li.ImportedAt)))
			if li.RunID != "" {
				rows = append(rows, row(t.t("f.last_import_run"), li.RunID))
			}
		}
		vf := s.Verification
		if vf == nil {
			rows = append(rows,
				rowTone(t.t("f.zone_check"), t.t("zone.not_checked"), toneMute),
				rowTone(t.t("f.freshness_check"), t.t("freshness.not_checked"), toneMute))
		} else {
			if vf.Zone.Match {
				rows = append(rows, rowTone(t.t("f.zone_check"), t.t("zone.match"), toneOK))
			} else {
				rows = append(rows, rowTone(t.t("f.zone_check"),
					t.t("zone.mismatch", "Found", vf.Zone.Found, "Expected", vf.Zone.Expected), toneBad))
			}
			switch {
			case vf.Freshness == nil:
				rows = append(rows, rowTone(t.t("f.freshness_check"), t.t("freshness.no_import"), toneNone))
			case vf.Freshness.Stale:
				rows = append(rows, rowTone(t.t("f.freshness_check"),
					t.t("freshness.stale", "Recorded", format.Time(vf.Freshness.Resolved)), toneBad))
			default:
				rows = append(rows, rowTone(t.t("f.freshness_check"), t.t("freshness.fresh"), toneOK))
			}
		}
	}
	if s.Medium != nil {
		rows = append([]kvRow{row(t.t("f.resolved"), v.when(s.Medium.ResolvedAt))}, rows...)
	}
	sec.kv(rows...)
	return sec
}

func shortDigest(d string) string { return format.TruncateMiddle(d, 28) }

func (v *viewer) inventory() *section {
	s, t := v.s, v.tr
	sec := &section{Title: t.t("section.inventory"), Level: 2}
	if s.Medium == nil {
		return sec
	}
	if len(s.Medium.Recipes) == 0 {
		sec.para(t.t("inventory.empty"), toneMute)
		return sec
	}
	var rows [][]cell
	for i := range s.Medium.Recipes {
		rc := &s.Medium.Recipes[i]
		text, tone := v.marker(rc.Assurance)
		rows = append(rows, []cell{
			c(rc.ID), c(shortDigest(rc.Digest)), c(format.Time(rc.ResolvedAt)),
			c(fmt.Sprint(len(rc.Ingredients))), ct(text, tone),
		})
	}
	sec.table(t.t("inventory.recipes"),
		[]string{t.t("inventory.col_recipe"), t.t("inventory.col_digest"), t.t("inventory.col_resolved"),
			t.t("inventory.col_ingredients"), t.t("inventory.col_assurance")}, rows)

	var ings [][]cell
	for i := range s.Medium.Recipes {
		rc := &s.Medium.Recipes[i]
		for j := range rc.Ingredients {
			ing := &rc.Ingredients[j]
			ings = append(ings, []cell{c(rc.ID), c(ing.Name), c(ing.Kind), c(ing.Tag), c(shortDigest(ing.Digest))})
		}
	}
	sec.table(t.t("inventory.ingredients"),
		[]string{t.t("inventory.col_recipe"), t.t("inventory.col_ingredient"), t.t("inventory.col_kind"),
			t.t("inventory.col_tag"), t.t("inventory.col_digest")}, ings)
	return sec
}

func (v *viewer) stateCell(state string) cell {
	tone := toneNone
	switch state {
	case StatePassed:
		tone = toneOK
	case StateFailed:
		tone = toneBad
	case StatePartial:
		tone = toneWarn
	case StateNotReached:
		tone = toneMute
	}
	return ct(v.tr.t("state."+state), tone)
}

func (v *viewer) verification() *section {
	s, t := v.s, v.tr
	sec := &section{Title: t.t("section.verification"), Level: 2}
	vf := s.Verification
	if vf == nil {
		if s.Side == SideDestination {
			sec.para(t.t("verification.none"), toneMute)
		} else {
			sec.para(t.t("verification.none_source"), toneMute)
		}
		return sec
	}
	tone := toneOK
	switch vf.Verdict {
	case "partial":
		tone = toneWarn
	case "blocked":
		tone = toneBad
	}
	sec.kv(
		rowTone(t.t("f.verdict"), t.t("verdict."+string(vf.Verdict)), tone),
		row(t.t("f.started"), format.Time(vf.StartedAt)),
		row(t.t("f.finished"), format.Time(vf.FinishedAt)),
		row(t.t("f.checked"), t.t("checked", "Files", vf.Checked.Files, "Size", v.bytes(vf.Checked.Bytes))),
	)

	if len(vf.Blocks) == 0 {
		sec.para(t.t("verification.no_blocks"), toneMute)
	} else {
		sec.para(t.t("verification.blocks"), toneNone)
		items := make([]cell, 0, len(vf.Blocks))
		for i := range vf.Blocks {
			b := &vf.Blocks[i]
			text := v.problem(&b.Problem)
			switch {
			case b.Overridden:
				text += " [" + t.t("verification.overridden") + "]"
			case b.Overridable:
				text += " [" + t.t("verification.override_possible") + "]"
			}
			bt := toneBad
			if b.Overridden {
				bt = toneWarn
			}
			items = append(items, ct(text, bt))
		}
		sec.list(items...)
	}

	var stages [][]cell
	for i := range vf.Stages {
		st := &vf.Stages[i]
		stg := v.stateCell(st.State)
		if st.Key != StageManifest {
			stg.Text = t.t("stage.tally", "State", stg.Text, "Passed", st.Passed, "Failed", st.Failed)
		}
		stages = append(stages, []cell{c(t.t("stage." + st.Key)), stg})
	}
	sec.table(t.t("verification.stages"), []string{t.t("verification.col_stage"), t.t("verification.col_result")}, stages)

	var rows [][]cell
	for i := range vf.Recipes {
		d := &vf.Recipes[i]
		result := ct(t.t("verification.pushable"), toneOK)
		if !d.Pushable {
			text := t.t("verification.blocked")
			if d.Reason != nil {
				text += " — " + v.problem(&Problem{Code: d.Reason.Code, Params: d.Reason.Params})
				if d.Reason.Path != "" {
					text += " (" + t.t("verification.in_file", "Path", d.Reason.Path) + ")"
				}
			}
			result = ct(text, toneBad)
		}
		scope := d.TrustScope
		if d.Unsigned {
			scope = strings.TrimSpace(scope + " " + t.t("verification.unsigned"))
		}
		rows = append(rows, []cell{
			c(d.ID), v.stateCell(d.Content), v.stateCell(d.Signature),
			c(d.KeyFingerprint), c(scope),
			c(t.t("checked", "Files", d.Files, "Size", v.bytes(d.Bytes))), v.deliveryAssurance(d), result,
		})
	}
	sec.table(t.t("verification.deliveries"),
		[]string{t.t("verification.col_recipe"), t.t("verification.col_content"), t.t("verification.col_signature"),
			t.t("verification.col_key"), t.t("verification.col_scope"), t.t("verification.col_size"),
			t.t("verification.col_assurance"), t.t("verification.col_result")}, rows)

	if len(vf.Findings) > 0 {
		items := make([]cell, 0, len(vf.Findings)+1)
		for i := range vf.Findings {
			f := &vf.Findings[i]
			text := v.problem(&Problem{Code: f.Code, Params: f.Params})
			if f.Path != "" {
				text += " (" + f.Path + ")"
			}
			items = append(items, c(text))
		}
		if vf.FindingsMore > 0 {
			items = append(items, ct(t.t("verification.findings_more", "Count", vf.FindingsMore), toneMute))
		}
		sec.para(t.t("verification.findings"), toneNone)
		sec.list(items...)
	}
	return sec
}

// deliveryAssurance is the assurance cell of one delivery: this
// instance's own verdict first, the source zone's claim beside it as a
// claim. Reduced assurance is never left to the reader to infer from a
// missing word (R-22).
func (v *viewer) deliveryAssurance(d *Delivery) cell {
	t := v.tr
	own, tone := v.marker(d.Assurance)
	if !d.SourceAssurance.Reduced() {
		return ct(own, tone)
	}
	claim, _ := v.marker(d.SourceAssurance)
	return ct(t.t("assurance.own_and_claim", "Own", own, "Claim", claim), toneWarn)
}

func (v *viewer) scan() *section {
	s, t := v.s, v.tr
	sec := &section{Title: t.t("section.scan"), Level: 2}
	sc := &s.Scan

	policy, ptone := t.t("scan.policy_warn"), toneWarn
	if sc.Blocking {
		policy, ptone = t.t("scan.policy_block"), toneOK
	}
	rows := []kvRow{rowTone(t.t("f.policy"), policy, ptone)}
	switch {
	case sc.StateUnknown:
		rows = append(rows, rowTone(t.t("f.scan_degraded"), t.t("scan.state_unknown"), toneMute))
	case sc.Degraded != nil:
		rows = append(rows, rowTone(t.t("f.scan_degraded"), t.t("scan.degraded", "Reason", v.problem(sc.Degraded)), toneBad))
	}
	// With the state unknown there is nothing to say about the database in
	// use: the reports below each state the age of the one THEY ran
	// against.
	switch {
	case sc.StateUnknown:
	case sc.Database == nil:
		rows = append(rows, rowTone(t.t("f.database"), t.t("scan.database_none"), toneBad))
	default:
		age := time.Duration(sc.DatabaseAgeSeconds) * time.Second
		text := t.t("scan.database", "Version", sc.Database.Version,
			"Built", format.Time(sc.Database.BuiltAt), "Age", scan.FormatAge(age))
		dtone := toneNone
		if sc.MaxAgeSeconds > 0 && sc.DatabaseAgeSeconds > sc.MaxAgeSeconds {
			text += " — " + t.t("scan.database_stale")
			dtone = toneWarn
		}
		rows = append(rows, rowTone(t.t("f.database"), text, dtone))
	}
	if sc.MaxAgeSeconds > 0 {
		rows = append(rows, row(t.t("f.max_age"), scan.FormatAge(time.Duration(sc.MaxAgeSeconds)*time.Second)))
	}
	sec.kv(rows...)

	if len(sc.Recipes) == 0 {
		sec.para(t.t("scan.none_recipes"), toneMute)
		return sec
	}
	for i := range sc.Recipes {
		rc := &sc.Recipes[i]
		sub := &section{Title: t.t("scan.recipe", "ID", rc.ID), Level: 3}
		if rc.Assurance.Reduced() {
			text, _ := v.marker(rc.Assurance)
			sub.para(t.t("scan.assurance", "Marker", text), toneWarn)
		}
		if len(rc.Items) == 0 {
			sub.para(t.t("scan.recipe_empty"), toneMute)
			sec.Blocks = append(sec.Blocks, sub)
			continue
		}
		var trows [][]cell
		for j := range rc.Items {
			it := &rc.Items[j]
			trows = append(trows, v.scanRow(it))
		}
		sub.table("", []string{t.t("scan.col_element"), t.t("scan.col_verdict"), t.t("scan.col_mode"),
			t.t("scan.col_threshold"), t.t("scan.col_db_age"), t.t("scan.col_counts"), t.t("scan.col_scanned")}, trows)
		for j := range rc.Items {
			it := &rc.Items[j]
			if len(it.Violations) > 0 {
				sub.para(t.t("scan.violations", "Element", it.Element, "IDs", strings.Join(it.Violations, ", ")), toneBad)
			}
			if it.ExceptionsApplied+it.ExceptionsExpired > 0 {
				sub.para(t.t("scan.exceptions", "Element", it.Element, "Applied", it.ExceptionsApplied, "Expired", it.ExceptionsExpired), toneNone)
			}
			for k := range it.Reasons {
				sub.para(t.t("scan.reason", "Element", it.Element, "Text", v.problem(&it.Reasons[k])), toneWarn)
			}
			if len(it.Findings) > 0 {
				var frows [][]cell
				for k := range it.Findings {
					f := &it.Findings[k]
					score := "—"
					if f.CVSS != nil {
						score = scan.FormatScore(*f.CVSS)
					}
					frows = append(frows, []cell{c(f.ID), c(string(f.Severity)), c(score), c(f.Package), c(f.InstalledVersion), c(f.FixedVersion)})
				}
				sub.table(t.t("scan.findings", "Element", it.Element),
					[]string{t.t("scan.col_id"), t.t("scan.col_severity"), t.t("scan.col_score"), t.t("scan.col_package"),
						t.t("scan.col_installed"), t.t("scan.col_fixed")}, frows)
				if it.FindingsMore > 0 {
					sub.para(t.t("scan.findings_more", "Count", it.FindingsMore), toneMute)
				}
			}
		}
		sec.Blocks = append(sec.Blocks, sub)
	}
	return sec
}

func (v *viewer) scanRow(it *ScanItem) []cell {
	t := v.tr
	vt := toneNone
	switch it.Verdict {
	case string(scan.VerdictPass):
		vt = toneOK
	case string(scan.VerdictWarn):
		vt = toneWarn
	case string(scan.VerdictBlock), string(scan.VerdictNotScanned):
		vt = toneBad
	case string(scan.VerdictOutOfScope):
		vt = toneMute
	}
	verdict := t.t("scan.verdict_" + it.Verdict)
	mode := "—"
	if it.Mode != "" {
		mode = t.t("scan.mode_" + it.Mode)
	}
	dbAge := cell{Text: t.t("scan.db_none")}
	if it.Database != nil {
		age := scan.FormatAge(time.Duration(it.Database.AgeSeconds) * time.Second)
		if it.Database.Stale {
			dbAge = ct(t.t("scan.db_stale", "Age", age), toneWarn)
		} else {
			dbAge = c(age)
		}
	}
	scanned := "—"
	if !it.ScannedAt.IsZero() {
		scanned = format.Time(it.ScannedAt)
	}
	counts := fmt.Sprintf("%d / %d / %d / %d / %d", it.Counts.Critical, it.Counts.High, it.Counts.Medium, it.Counts.Low, it.Counts.Unknown)
	return []cell{c(it.Element), ct(verdict, vt), c(mode), c(scan.FormatScore(it.Threshold)), dbAge, c(counts), c(scanned)}
}

func (v *viewer) assurance() *section {
	s, t := v.s, v.tr
	sec := &section{Title: t.t("section.assurance"), Level: 2}
	if len(s.Assurance) == 0 {
		sec.para(t.t("assurance.none"), toneOK)
		return sec
	}
	sec.para(t.t("assurance.intro"), toneWarn)
	var rows [][]cell
	for i := range s.Assurance {
		a := &s.Assurance[i]
		reasons := make([]string, 0, len(a.Reasons))
		for _, r := range a.Reasons {
			reasons = append(reasons, t.t("assurance.reason_"+string(r)))
		}
		rows = append(rows, []cell{
			ct(a.Recipe, toneWarn), c(t.t("assurance.origin_" + a.Origin)), c(a.Scope), c(strings.Join(reasons, ", ")),
		})
	}
	sec.table("", []string{t.t("assurance.col_recipe"), t.t("assurance.col_origin"),
		t.t("assurance.col_scope"), t.t("assurance.col_waived")}, rows)
	return sec
}
