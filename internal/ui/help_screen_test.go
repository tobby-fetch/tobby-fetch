// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/help"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestHelpAnchorsMatchTaxonomy: /help carries one anchored section per
// catalog entry, and each id is exactly the fragment of the HelpAnchor the
// taxonomy emits — no error link can 404 (R-03, UI-SPEC §5.10).
func TestHelpAnchorsMatchTaxonomy(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view") // readable by every role

	r := httptest.NewRequest(http.MethodGet, "/help", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/help = %d", w.Code)
	}
	body := w.Body.String()

	if len(taxonomy.All()) == 0 {
		t.Fatal("empty taxonomy catalog")
	}
	for _, en := range taxonomy.All() {
		m := taxonomy.Localize("en", taxonomy.New(en.Code, genericParams(en)))
		anchor, ok := strings.CutPrefix(m.HelpAnchor, "/help#")
		if !ok {
			t.Fatalf("HelpAnchor %q of %s is not a /help fragment", m.HelpAnchor, en.Code)
		}
		if anchor != string(en.Code) {
			t.Errorf("HelpAnchor fragment %q differs from code %q", anchor, en.Code)
		}
		if !strings.Contains(body, `id="`+anchor+`"`) {
			t.Errorf("/help misses the anchored section id=%q", anchor)
		}
		// The summary links every code.
		if !strings.Contains(body, `href="#`+anchor+`"`) {
			t.Errorf("/help summary misses the anchor link for %s", anchor)
		}
	}
}

// genericParams builds the placeholder parameter set of one entry, the
// same way the screen does.
func genericParams(en taxonomy.Entry) taxonomy.Params {
	p := make(taxonomy.Params, len(en.Params))
	for _, name := range en.Params {
		p[name] = "<" + name + ">"
	}
	return p
}

// TestHelpLocalizedTriptych: the triptych renders in the current language
// with readable generic parameters ("<host>").
func TestHelpLocalizedTriptych(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	r := httptest.NewRequest(http.MethodGet, "/help", http.NoBody)
	r.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body := w.Body.String()

	if !strings.Contains(body, "Cause probable") || !strings.Contains(body, "Action corrective") {
		t.Error("French /help misses the localized triptych labels")
	}
	// TBY-REG-003's cause names its host parameter generically (the value
	// is "<host>", HTML-escaped by the template engine).
	if !strings.Contains(body, "&lt;host&gt;") {
		t.Error("generic parameters do not render as <name> placeholders")
	}

	// The head of the trio carries the local sub-menu (UI-SPEC §5.10-5.12).
	for _, href := range []string{`href="/help"`, `href="/about"`, `href="/api-docs"`} {
		if !strings.Contains(body, href) {
			t.Errorf("/help sub-menu misses %s", href)
		}
	}
}

// TestHelpHomeCarriesTheGuideNavigation: the /help home lists every
// embedded page under its section, in both languages — the entry point
// into the documentation NFR-003's amendment puts inside the binary.
func TestHelpHomeCarriesTheGuideNavigation(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	for _, lang := range []string{"en", "fr"} {
		r := httptest.NewRequest(http.MethodGet, "/help", http.NoBody)
		r.Header.Set("Accept-Language", lang)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("/help in %s = %d", lang, w.Code)
		}
		body := w.Body.String()
		for _, key := range help.Load().Keys() {
			if !strings.Contains(body, `href="/help/`+key+`"`) {
				t.Errorf("%s: the help home does not link %s", lang, key)
			}
		}
	}
}

// TestHelpPageServesTheEmbeddedGuide: every page of the corpus is served,
// in both languages, with its title and its body — the acceptance sentence
// of the NFR-003 amendment ("an instance started with no network serves
// the complete guides in both languages").
func TestHelpPageServesTheEmbeddedGuide(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	for _, lang := range []string{"en", "fr"} {
		for _, key := range help.Load().Keys() {
			r := httptest.NewRequest(http.MethodGet, "/help/"+key, http.NoBody)
			r.Header.Set("Accept-Language", lang)
			r.AddCookie(c)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("/help/%s in %s = %d", key, lang, w.Code)
			}
			pg, _, _ := help.Load().Lookup(lang, key)
			if !strings.Contains(w.Body.String(), template.HTMLEscapeString(pg.Title)) {
				t.Errorf("/help/%s in %s does not carry its title %q", key, lang, pg.Title)
			}
			if !strings.Contains(w.Body.String(), `class="t-doc-body"`) {
				t.Errorf("/help/%s in %s renders no body", key, lang)
			}
		}
	}
}

// TestHelpPageRejectsTraversal: the path segment of /help/… is a key in a
// fixed set, never a file name. Every probe below answers the taxonomized
// 404 and nothing leaves the binary (NFR-011).
//
// The percent-encoded probes are the ones that matter: ServeMux cleans a
// literal "/help/../go.mod" into a redirect before any handler sees it,
// but "%2e%2e%2f" and "..%2f" reach PathValue as "../../go.mod" intact —
// so the guard has to be the lookup, not the router.
//
// Proved fallible: replacing the corpus lookup with os.ReadFile over
// filepath.Join(".", key) served the repository's go.mod on both encoded
// probes.
func TestHelpPageRejectsTraversal(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	for _, path := range []string{
		"/help/../go.mod",
		"/help/passthrough/../../go.mod",
		"/help/%2e%2e%2f%2e%2e%2fgo.mod",
		"/help/..%2f..%2fgo.mod",
		"/help/.%2e/.%2e/go.mod",
		"/help/%2e%2e%2fcorpus%2fen%2fpassthrough%2foperate.md",
		"/help//etc/passwd",
		"/help/passthrough",
		"/help/corpus/en/passthrough/operate.md",
		"/help/en/passthrough/operate",
	} {
		r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code == http.StatusOK && strings.Contains(body, "t-doc-body") {
			t.Errorf("%s served a document", path)
		}
		if strings.Contains(body, "module github.com/tobby-fetch") {
			t.Errorf("%s served a file of the repository", path)
		}
	}
}

// TestHelpAssetIsServedAndRevalidated: the screenshots travel with the
// guides and answer 304 from a precomputed digest, like every other
// embedded asset.
func TestHelpAssetIsServedAndRevalidated(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	names := help.Load().AssetNames()
	if len(names) == 0 {
		t.Fatal("no screenshot embedded")
	}
	r := httptest.NewRequest(http.MethodGet, "/help/-/assets/"+names[0], http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("asset %s = %d", names[0], w.Code)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the asset carries no ETag")
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type %q", ct)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/help/-/assets/"+names[0], http.NoBody)
	r2.Header.Set("If-None-Match", etag)
	r2.AddCookie(c)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)
	if w2.Code != http.StatusNotModified {
		t.Errorf("conditional request = %d, want 304", w2.Code)
	}

	// An asset name nobody published is a 404, not a file read.
	for _, name := range []string{"nope.png", "../../go.mod"} {
		r3 := httptest.NewRequest(http.MethodGet, "/help/-/assets/"+name, http.NoBody)
		r3.AddCookie(c)
		w3 := httptest.NewRecorder()
		mux.ServeHTTP(w3, r3)
		if w3.Code == http.StatusOK {
			t.Errorf("asset %q was served", name)
		}
	}
}

// TestHelpPageFallsBackAndSaysSo: a page with no translation is served in
// English, announced as such, and carries lang="en" on its body so the
// document's language stays true for assistive technology (NFR-017).
func TestHelpPageFallsBackAndSaysSo(t *testing.T) {
	c := help.Load()
	var untranslated string
	for _, key := range c.Keys() {
		if !c.Translated("fr", key) {
			untranslated = key
			break
		}
	}
	if untranslated == "" {
		t.Skip("every page is translated — the fallback path has no subject left")
	}
	u := newTestUI(t, false)
	mux := mount(u)
	cookie := login(t, mux, "lecteur", "pw-view")

	r := httptest.NewRequest(http.MethodGet, "/help/"+untranslated, http.NoBody)
	r.Header.Set("Accept-Language", "fr")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `lang="en"`) {
		t.Error("the English body of a fallback page is not marked lang=\"en\"")
	}
	if !strings.Contains(body, "pas encore traduite") {
		t.Error("the fallback is not announced to the reader")
	}
}

// TestScreenGuidesExist: every contextual pointer a screen renders names a
// page the corpus carries. A screen offering documentation that does not
// exist is worse than a screen offering none.
//
// Proved fallible: pointing the tasks screen at "passthrough/operations"
// (a page that does not exist) failed here, naming the template.
func TestScreenGuidesExist(t *testing.T) {
	corpus := help.Load()
	call := regexp.MustCompile(`\.Guide\s+"([^"]+)"`)
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(raw), -1) {
			if _, _, ok := corpus.Lookup("en", m[1]); !ok {
				t.Errorf("%s: .Guide %q names no page of the embedded corpus", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestHelpSectionLabelsAreTranslated: every section of the corpus has its
// label in both catalogs. The navigation builds its keys with printf, so
// TestTemplateKeysExist cannot see them — this test is what stands in for
// it (FR-063).
func TestHelpSectionLabelsAreTranslated(t *testing.T) {
	for _, lang := range []string{"en", "fr"} {
		loc := localizer(lang)
		for _, ns := range help.Load().Nav(lang) {
			key := "help.section." + ns.ID
			if got := translate(loc, key, nil, nil); got == "["+key+"]" {
				t.Errorf("%s: no %s label for section %q", lang, lang, ns.ID)
			}
		}
	}
}
