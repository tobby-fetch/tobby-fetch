// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write creates root/rel with body.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a tiny "ui"-shaped catalog: catalogs en/fr, one Go file,
// one template.
func fixture(t *testing.T, en, fr, goSrc, tmpl string) (string, catalog) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "msg/en.yaml", en)
	write(t, root, "msg/fr.yaml", fr)
	write(t, root, "pkg/a.go", goSrc)
	write(t, root, "pkg/tpl/p.html", tmpl)
	return root, catalog{
		Name: "fixture", Dir: "msg", GoDirs: []string{"pkg"}, TemplateDirs: []string{"pkg/tpl"},
		CallNames: []string{"T", "TN", "t"},
	}
}

const goOK = `package pkg

type v struct{}

func (v) T(key string, kv ...any) string { return key }

func use(x v) {
	_ = x.T("a.one")
	_ = x.T("a.count", "Count", 1)
}
`

const tmplOK = `{{.T "a.tmpl"}} {{.TN "a.plural" .N "Count" .N}}`

const enOK = `"a.one": "One {{.X}}"
"a.count": "Count {{.Count}}"
"a.tmpl": "From a template"
"a.plural":
  one: "1 thing"
  other: "{{.Count}} things"
`

const frOK = `"a.one": "Un {{.X}}"
"a.count": "Compte {{.Count}}"
"a.tmpl": "Depuis un gabarit"
"a.plural":
  one: "1 chose"
  other: "{{.Count}} choses"
`

func problems(t *testing.T, en, fr, goSrc, tmpl string, ledger ...unusedEntry) []string {
	t.Helper()
	root, c := fixture(t, en, fr, goSrc, tmpl)
	got, err := checkCatalog(root, &c, ledger)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustContain(t *testing.T, got []string, want ...string) {
	t.Helper()
	joined := strings.Join(got, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("no finding contains %q; findings:\n%s", w, joined)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d finding(s), want %d:\n%s", len(got), len(want), joined)
	}
}

func TestCompleteCatalogsPass(t *testing.T) {
	if got := problems(t, enOK, frOK, goOK, tmplOK); len(got) != 0 {
		t.Fatalf("unexpected findings: %v", got)
	}
}

func TestKeyMissingFromOneLanguage(t *testing.T) {
	fr := strings.Replace(frOK, `"a.tmpl": "Depuis un gabarit"`+"\n", "", 1)
	mustContain(t, problems(t, enOK, fr, goOK, tmplOK),
		`key "a.tmpl" is missing from fr.yaml`,
		// used by the template and absent from FR: reported as a use, too
		`key "a.tmpl" is used by the code and undefined in fr.yaml`)
}

func TestKeyMissingFromOneLanguageEN(t *testing.T) {
	en := strings.Replace(enOK, `"a.one": "One {{.X}}"`+"\n", "", 1)
	mustContain(t, problems(t, en, frOK, goOK, tmplOK),
		`key "a.one" is missing from en.yaml`,
		`key "a.one" is used by the code and undefined in en.yaml`)
}

func TestKeyUsedButDefinedNowhere(t *testing.T) {
	src := strings.Replace(goOK, `_ = x.T("a.one")`, `_ = x.T("a.one")
	_ = x.T("a.ghost")`, 1)
	mustContain(t, problems(t, enOK, frOK, src, tmplOK),
		`key "a.ghost" is used by the code and undefined in en.yaml`,
		`key "a.ghost" is used by the code and undefined in fr.yaml`)
	tmpl := tmplOK + `{{$.T "t.ghost"}}`
	mustContain(t, problems(t, enOK, frOK, goOK, tmpl),
		`key "t.ghost" is used by the code and undefined in en.yaml`,
		`key "t.ghost" is used by the code and undefined in fr.yaml`)
}

func TestOrphan(t *testing.T) {
	both := `"a.dead": "Never used"` + "\n"
	mustContain(t, problems(t, enOK+both, frOK+both, goOK, tmplOK),
		`key "a.dead" is defined and never used (orphan)`)
}

func TestPlaceholderDivergence(t *testing.T) {
	fr := strings.Replace(frOK, `"Un {{.X}}"`, `"Un {{.Y}}"`, 1)
	mustContain(t, problems(t, enOK, fr, goOK, tmplOK),
		`key "a.one": placeholder {{.X}} is in en only`,
		`key "a.one": placeholder {{.Y}} is in fr only`)
	// a placeholder dropped in one plural form of one language only
	fr = strings.Replace(frOK, `other: "{{.Count}} choses"`, `other: "des choses"`, 1)
	mustContain(t, problems(t, enOK, fr, goOK, tmplOK),
		`key "a.plural": placeholder {{.Count}} is in en only`)
}

func TestPluralAgainstPlain(t *testing.T) {
	fr := strings.Replace(frOK, "\"a.plural\":\n  one: \"1 chose\"\n  other: \"{{.Count}} choses\"\n", `"a.plural": "{{.Count}} choses"`+"\n", 1)
	mustContain(t, problems(t, enOK, fr, goOK, tmplOK),
		`key "a.plural" is a plural entry in one language and a plain message in the other`)
}

func TestKeyHeldInAVariableCountsAsUsed(t *testing.T) {
	src := goOK + `
func table() []string { return []string{"a.held"} }
`
	extra := `"a.held": "Held"` + "\n"
	if got := problems(t, enOK+extra, frOK+extra, src, tmplOK); len(got) != 0 {
		t.Fatalf("a key present as a literal is a use: %v", got)
	}
}

func TestRuntimePrefixes(t *testing.T) {
	src := goOK + `
func dyn(x v, s string) {
	_ = x.T("a.k_" + s)
	_ = x.T(sprintf("a.f_%s", s))
}

func sprintf(string, ...any) string { return "" }
`
	// fmt.Sprintf is recognised by its name, whatever the receiver
	src = strings.Replace(src, `x.T(sprintf("a.f_%s", s))`, `x.T(fmt.Sprintf("a.f_%s", s))`, 1)
	src = strings.Replace(src, "package pkg\n", "package pkg\n\nimport \"fmt\"\n", 1)
	tmpl := tmplOK + `{{.T (printf "a.t_%s" .X)}}`
	extra := `"a.k_x": "K"` + "\n" + `"a.f_x": "F"` + "\n" + `"a.t_x": "T"` + "\n"
	if got := problems(t, enOK+extra, frOK+extra, src, tmpl); len(got) != 0 {
		t.Fatalf("run-time families must count as used: %v", got)
	}
	// ... but a key outside every family stays an orphan
	other := `"b.k_x": "K"` + "\n"
	mustContain(t, problems(t, enOK+extra+other, frOK+extra+other, src, tmpl),
		`key "b.k_x" is defined and never used (orphan)`)
}

func TestKnownUnusedLedger(t *testing.T) {
	both := `"a.later": "Later"` + "\n"
	ledger := []unusedEntry{{Catalog: "fixture", Prefix: "a.later", Reason: "wired next milestone"}}
	if got := problems(t, enOK+both, frOK+both, goOK, tmplOK, ledger...); len(got) != 0 {
		t.Fatalf("a ledger entry covers its key: %v", got)
	}
	// a stale entry (the key is used, or gone) fails
	mustContain(t, problems(t, enOK, frOK, goOK, tmplOK, ledger...),
		`entry "a.later" matches no unused key`)
	// another catalog's entry does not apply here
	other := []unusedEntry{{Catalog: "slip", Prefix: "a.later", Reason: "x"}}
	mustContain(t, problems(t, enOK+both, frOK+both, goOK, tmplOK, other...),
		`key "a.later" is defined and never used (orphan)`)
}

func TestTestFilesAreNotUses(t *testing.T) {
	root, c := fixture(t, enOK+`"a.only_in_test": "x"`+"\n", frOK+`"a.only_in_test": "x"`+"\n", goOK, tmplOK)
	write(t, root, "pkg/a_test.go", "package pkg\n\nvar _ = \"a.only_in_test\"\n")
	got, err := checkCatalog(root, &c, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, got, `key "a.only_in_test" is defined and never used (orphan)`)
}

func TestTaxonomyDerivation(t *testing.T) {
	src := newSources()
	src.Literals["TBY-AUTH-001"] = true
	src.Literals["not-a-code"] = true
	src.ConstNames["ExitNamePolicy"] = "policy"
	src.ConstNames["Other"] = "x"
	got := strings.Join(taxonomyDerived(src), ",")
	for _, want := range []string{"TBY-AUTH-001.what", "TBY-AUTH-001.cause", "TBY-AUTH-001.action", "exit.policy.title", "exit.policy.meaning"} {
		if !strings.Contains(got, want) {
			t.Errorf("derived keys lack %s: %s", want, got)
		}
	}
	if strings.Contains(got, "not-a-code") || strings.Contains(got, "exit.x") {
		t.Errorf("derived keys include non-sources: %s", got)
	}
}

func TestMalformedCatalogs(t *testing.T) {
	root, c := fixture(t, "a: [1, 2]\n", frOK, goOK, tmplOK)
	if _, err := checkCatalog(root, &c, nil); err == nil {
		t.Error("a list value was accepted")
	}
	root, c = fixture(t, "\"a.p\":\n  one: 1\n", frOK, goOK, tmplOK)
	if _, err := checkCatalog(root, &c, nil); err == nil {
		t.Error("a non-string plural form was accepted")
	}
}

func TestParseKnownUnused(t *testing.T) {
	got, err := parseKnownUnused(strings.NewReader("# c\n\nui a.b :: because\n"))
	if err != nil || len(got) != 1 || got[0].Catalog != "ui" || got[0].Prefix != "a.b" || got[0].Reason != "because" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, bad := range []string{"ui a.b\n", "ui a.b ::\n", "a.b :: reason\n"} {
		if _, err := parseKnownUnused(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestRepositoryCatalogsAreComplete is the gate itself, as a unit test: the
// shipped catalogs, the shipped sources, the shipped ledger.
func TestRepositoryCatalogsAreComplete(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{"-root", filepath.Join("..", "..")}, &out, &errb); rc != 0 {
		t.Fatalf("exit %d:\n%s%s", rc, out.String(), errb.String())
	}
}
