// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command helpcrawl reads the documentation a running instance serves under
// /help the way a reader does — over HTTP, signed in, in each language —
// and checks it against the published sources it was built from
// (website/src/content/docs). It is the client of the milestone-7 crucible
// scenario e-docs-offline (NFR-003, NFR-021): the instance under test sits
// in the air-gapped zone, so nothing it serves may point at a page, an
// anchor or an image it does not carry.
//
//	go run ./test/topology/helpcrawl -base http://10.180.20.5:8080 -user reader \
//	    -docs website/src/content/docs -catalog internal/taxonomy/catalog.go \
//	    -require passthrough/deploy,security/input-validation:table
//
// The password is read from $HELPCRAWL_PASSWORD (a flag value would be in
// the process table). Every finding is printed as "FAIL <finding>" and the
// exit status is 1 when there is one; a clean run prints one "ok <fact>"
// line per check, for the scenario's raw report.
//
// It shares no code with internal/help on purpose: the URL contract it
// checks (/docs/<section>/<page>/ → /help/<section>/<page>, the error
// reference → /help#<CODE>, screenshots → /help/-/assets/<name>) is the
// one website/README-docs.md documents, re-derived here from the sources.
// A renderer that drops a link it could not resolve leaves no dangling
// href behind — only a missing one — so every link of every source page
// is looked for in the page as served.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// excluded mirrors the pages tools/helpsync leaves out of the corpus: the
// website's landing page, and the error reference, which /help renders
// live from the taxonomy catalog.
var excluded = map[string]bool{"index.mdx": true, "reference/errors.md": true}

var (
	codeConstRe = regexp.MustCompile(`Code\s*=\s*"(TBY-[A-Z0-9]+-\d{3})"`)
	codeTextRe  = regexp.MustCompile(`TBY-[A-Z0-9]+-\d{3}`)
	helpSecRe   = regexp.MustCompile(`(?s)<section class="t-helpsec" id="(TBY-[A-Z0-9]+-\d{3})">(.*?)</section>`)
	ddRe        = regexp.MustCompile(`(?s)<dd>(.*?)</dd>`)
	chipRe      = regexp.MustCompile(`href="#(TBY-[A-Z0-9]+-\d{3})"`)
	navRe       = regexp.MustCompile(`<a href="/help/([^"#]+)">`)
	attrRe      = regexp.MustCompile(`\b(href|src)="([^"]*)"`)
	idRe        = regexp.MustCompile(`\bid="([^"]+)"`)
	h1Re        = regexp.MustCompile(`(?s)<h1 class="t-page-title">(.*?)(?:<span|</h1>)`)
	htmlLangRe  = regexp.MustCompile(`<html lang="([a-z]+)"`)
	srcLinkRe   = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	errChipRe   = regexp.MustCompile(`<span class="t-chip t-chip--code" translate="no">(TBY-[A-Z0-9]+-\d{3})</span>`)
	tagRe       = regexp.MustCompile(`<[^>]+>`)
)

type crawler struct {
	base   string
	client *http.Client
	user   string
	pass   string
	issues []string
	facts  []string
	// pages is lang -> key -> served HTML; assets caches checked images.
	pages  map[string]map[string]string
	assets map[string]bool
	others map[string]bool
}

func (c *crawler) failf(format string, args ...any) {
	c.issues = append(c.issues, fmt.Sprintf(format, args...))
}

func (c *crawler) okf(format string, args ...any) {
	c.facts = append(c.facts, fmt.Sprintf(format, args...))
}

func main() {
	base := flag.String("base", "", "base URL of the instance under test")
	docs := flag.String("docs", "website/src/content/docs", "the website's content/docs directory (English under docs/, French under fr/docs/)")
	catalog := flag.String("catalog", "internal/taxonomy/catalog.go", "the taxonomy catalog source")
	user := flag.String("user", "", "local account to sign in with (empty: authentication disabled)")
	require := flag.String("require", "", "comma-separated page keys that must be served in every language; a key suffixed :table must carry a table")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "usage: helpcrawl -base <url> [-user <name>] [-docs <dir>] [-catalog <file>] [-require <keys>]")
		os.Exit(2)
	}
	jar, _ := cookiejar.New(nil)
	c := &crawler{
		base: strings.TrimRight(*base, "/"), user: *user, pass: os.Getenv("HELPCRAWL_PASSWORD"),
		client: &http.Client{
			Jar: jar, Timeout: 30 * time.Second,
			// A redirect is a finding, never followed: a page that answers
			// 303 to /login is a page the reader does not get.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		pages: map[string]map[string]string{}, assets: map[string]bool{}, others: map[string]bool{},
	}
	if err := c.run(*docs, *catalog, *require); err != nil {
		fmt.Fprintln(os.Stderr, "helpcrawl:", err)
		os.Exit(2)
	}
	for _, f := range c.facts {
		fmt.Println("ok   " + f)
	}
	for _, i := range c.issues {
		fmt.Println("FAIL " + i)
	}
	if len(c.issues) > 0 {
		os.Exit(1)
	}
}

// source is one published page in one language.
type source struct {
	key   string
	title string
	body  string
}

func (c *crawler) run(docs, catalog, require string) error {
	codes, err := catalogCodes(catalog)
	if err != nil {
		return err
	}
	srcs := map[string]map[string]source{}
	for lang, dir := range map[string]string{"en": filepath.Join(docs, "docs"), "fr": filepath.Join(docs, "fr", "docs")} {
		if srcs[lang], err = loadSources(dir); err != nil {
			return err
		}
	}
	if len(srcs["en"]) == 0 {
		return errors.New("no English source page found under " + docs)
	}
	for key := range srcs["en"] {
		if _, ok := srcs["fr"][key]; !ok {
			c.failf("NFR-021: %s has no French source page", key)
		}
	}
	if c.user != "" {
		if err := c.login(); err != nil {
			return err
		}
	}
	whats := map[string]map[string]string{}
	for _, lang := range []string{"en", "fr"} {
		whats[lang] = c.home(lang, srcs["en"], codes)
		c.crawlPages(lang, srcs[lang], srcs["en"])
		c.checkLinks(lang)
		c.checkSourceLinks(lang, srcs[lang])
		c.liveErrors(lang, codes)
	}
	same := 0
	for code, en := range whats["en"] {
		if fr, ok := whats["fr"][code]; ok && fr == en {
			c.failf("NFR-021: the /help entry of %s reads the same in French and in English (%q): no translation", code, en)
			same++
		}
	}
	if same == 0 && len(whats["fr"]) > 0 {
		c.okf("every one of the %d taxonomy entries reads differently in French and in English (translated, not a fallback)", len(whats["fr"]))
	}
	c.mentionedCodes(codes)
	c.required(require, srcs)
	return nil
}

// login opens a browser-style session, as d-hardening does.
func (c *crawler) login() error {
	resp, err := c.client.PostForm(c.base+"/login", url.Values{
		"username": {c.user}, "password": {c.pass}, "next": {"/help"},
	})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("signing in as %s answered %d", c.user, resp.StatusCode)
	}
	return nil
}

// get fetches one URL in a language: the UI language cookie, and
// Accept-Language for the API.
func (c *crawler) get(lang, target string) (status int, contentType string, body []byte, err error) {
	req, err := http.NewRequest(http.MethodGet, c.base+target, http.NoBody)
	if err != nil {
		return 0, "", nil, err
	}
	// The client side of the UI language cookie: Secure, HttpOnly and
	// SameSite are attributes a server sets, never sent back.
	req.AddCookie(&http.Cookie{Name: "tobby_lang", Value: lang}) //nolint:gosec // G124: request cookie, see above
	req.Header.Set("Accept-Language", lang)
	if strings.HasPrefix(target, "/api/") && c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	body, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, err
}

// home checks /help in lang: the guide navigation lists exactly the
// published pages, the troubleshooting index exactly the catalog. It
// returns the "what" sentence of every entry, for the translation check.
func (c *crawler) home(lang string, en map[string]source, codes map[string]bool) map[string]string {
	st, _, body, err := c.get(lang, "/help")
	if err != nil || st != http.StatusOK {
		c.failf("NFR-003: GET /help (%s) answered %d %v", lang, st, err)
		return nil
	}
	page := string(body)
	if m := htmlLangRe.FindStringSubmatch(page); len(m) < 2 || m[1] != lang {
		c.failf("NFR-021: /help requested in %s is not served in %s", lang, lang)
	}
	guides, _, _ := strings.Cut(page, `id="codes"`)
	listed := map[string]bool{}
	for _, m := range navRe.FindAllStringSubmatch(guides, -1) {
		listed[m[1]] = true
	}
	for key := range en {
		if !listed[key] {
			c.failf("NFR-003: the /help navigation (%s) does not list %s", lang, key)
		}
	}
	for key := range listed {
		if _, ok := en[key]; !ok {
			c.failf("NFR-003: the /help navigation (%s) lists %s, which is not a published page", lang, key)
		}
	}
	if n := strings.Count(guides, "t-help-fallback-tag"); n > 0 {
		c.failf("NFR-021: the /help navigation (%s) marks %d pages as an English fallback", lang, n)
	}
	whats := map[string]string{}
	for _, m := range helpSecRe.FindAllStringSubmatch(page, -1) {
		dds := ddRe.FindAllStringSubmatch(m[2], -1)
		if len(dds) < 3 {
			c.failf("NFR-021: the /help entry %s (%s) does not carry what, cause and action", m[1], lang)
			continue
		}
		for _, dd := range dds[:3] {
			if strings.TrimSpace(text(dd[1])) == "" {
				c.failf("NFR-021: the /help entry %s (%s) has an empty what, cause or action", m[1], lang)
			}
		}
		whats[m[1]] = text(dds[0][1])
	}
	chips := map[string]bool{}
	for _, m := range chipRe.FindAllStringSubmatch(page, -1) {
		chips[m[1]] = true
	}
	for code := range codes {
		if _, ok := whats[code]; !ok {
			c.failf("NFR-021: the catalog code %s has no entry on /help (%s)", code, lang)
		}
		if !chips[code] {
			c.failf("NFR-021: the /help code index (%s) does not link to %s", lang, code)
		}
	}
	for code := range whats {
		if !codes[code] {
			c.failf("the /help index (%s) carries %s, which is not in the catalog", lang, code)
		}
	}
	c.okf("/help (%s): the navigation lists the %d published pages and nothing else; the index carries every one of the %d catalog codes, each linked and with what, cause and action", lang, len(en), len(codes))
	return whats
}

// crawlPages fetches every published page in lang.
func (c *crawler) crawlPages(lang string, srcs, en map[string]source) {
	c.pages[lang] = map[string]string{}
	for _, key := range sortedKeys(en) {
		st, ct, body, err := c.get(lang, "/help/"+key)
		if err != nil || st != http.StatusOK || !strings.HasPrefix(ct, "text/html") {
			c.failf("NFR-003: GET /help/%s (%s) answered %d %q %v", key, lang, st, ct, err)
			continue
		}
		page := string(body)
		c.pages[lang][key] = page
		if m := htmlLangRe.FindStringSubmatch(page); len(m) < 2 || m[1] != lang {
			c.failf("NFR-021: /help/%s requested in %s is not served in %s", key, lang, lang)
		}
		if strings.Contains(page, `class="t-doc-body" lang="en"`) && lang != "en" {
			c.failf("NFR-021: /help/%s is served in English to a %s reader (no translation embedded)", key, lang)
		}
		want := en[key].title
		if s, ok := srcs[key]; ok {
			want = s.title
		}
		if m := h1Re.FindStringSubmatch(page); len(m) < 2 || text(m[1]) != want {
			got := ""
			if len(m) > 1 {
				got = text(m[1])
			}
			c.failf("NFR-021: /help/%s (%s) is titled %q, the published page %q", key, lang, got, want)
		}
	}
	c.okf("every one of the %d published pages is served in %s at /help/<section>/<page>, titled as published, without English fallback", len(c.pages[lang]), lang)
}

// docBody is the served page's own content: the article, chrome excluded.
func docBody(page string) string {
	_, rest, ok := strings.Cut(page, `<article class="t-doc">`)
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "</article>")
	return body
}

// checkLinks follows every link and image of every served page in lang.
func (c *crawler) checkLinks(lang string) {
	ids := map[string]map[string]bool{}
	for key, page := range c.pages[lang] {
		ids[key] = map[string]bool{}
		for _, m := range idRe.FindAllStringSubmatch(page, -1) {
			ids[key][html.UnescapeString(m[1])] = true
		}
	}
	helpIDs := map[string]bool{}
	if _, _, body, err := c.get(lang, "/help"); err == nil {
		for _, m := range idRe.FindAllStringSubmatch(string(body), -1) {
			helpIDs[m[1]] = true
		}
	}
	links, frags, images, external := 0, 0, 0, 0
	for _, key := range sortedKeys(c.pages[lang]) {
		for _, m := range attrRe.FindAllStringSubmatch(docBody(c.pages[lang][key]), -1) {
			target := html.UnescapeString(m[2])
			where := fmt.Sprintf("/help/%s (%s): %s=%q", key, lang, m[1], target)
			p, frag, hasFrag := strings.Cut(target, "#")
			// A browser matches the DECODED fragment against the ids:
			// html/template percent-encodes non-ASCII in a href.
			if f, err := url.PathUnescape(frag); err == nil {
				frag = f
			}
			switch {
			case strings.HasPrefix(target, "https://"), strings.HasPrefix(target, "http://"):
				external++
				continue
			case p == "":
				if !ids[key][frag] {
					c.failf("NFR-003: %s: no such anchor on the page", where)
				}
				frags++
			case strings.HasPrefix(p, "/help/-/assets/"):
				images++
				if !c.asset(lang, p) {
					c.failf("NFR-003: %s: the image is not served", where)
				}
			case p == "/help":
				links++
				if hasFrag {
					frags++
					if !helpIDs[frag] {
						c.failf("NFR-003: %s: /help carries no anchor %q", where, frag)
					}
				}
			case strings.HasPrefix(p, "/help/"):
				links++
				tk := strings.TrimSuffix(strings.TrimPrefix(p, "/help/"), "/")
				if _, ok := c.pages[lang][tk]; !ok {
					c.failf("NFR-003: %s: no published page %s", where, tk)
					continue
				}
				if hasFrag {
					frags++
					if !ids[tk][frag] {
						c.failf("NFR-003: %s: /help/%s carries no anchor %q", where, tk, frag)
					}
				}
			default:
				links++
				if !c.other(lang, p) {
					c.failf("NFR-003: %s: the target does not answer 200", where)
				}
			}
		}
	}
	c.okf("crawl (%s): %d links, %d anchors and %d images followed on the served pages, none dangling; %d external links, marked as such, not followed offline", lang, links, frags, images, external)
}

func (c *crawler) asset(lang, p string) bool {
	if ok, seen := c.assets[p]; seen {
		return ok
	}
	st, ct, body, err := c.get(lang, p)
	ok := err == nil && st == http.StatusOK && ct == "image/png" && bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n"))
	c.assets[p] = ok
	return ok
}

func (c *crawler) other(lang, p string) bool {
	if ok, seen := c.others[p]; seen {
		return ok
	}
	st, _, _, err := c.get(lang, p)
	ok := err == nil && st == http.StatusOK
	c.others[p] = ok
	return ok
}

// checkSourceLinks looks, in every served page, for every link its source
// page writes — resolved by the documented URL contract. A link the
// renderer could not resolve is dropped, text kept: it is a missing href,
// not a dangling one, and only this comparison sees it.
func (c *crawler) checkSourceLinks(lang string, srcs map[string]source) {
	n := 0
	for _, key := range sortedKeys(srcs) {
		page, ok := c.pages[lang][key]
		if !ok {
			continue
		}
		body := docBody(page)
		for _, target := range sourceTargets(srcs[key].body) {
			want := resolve(key, target)
			n++
			if !strings.Contains(body, `="`+html.EscapeString(want)+`"`) {
				c.failf("NFR-003: /help/%s (%s): the source link %q (served as %q) is missing from the page — dropped as unresolved", key, lang, target, want)
			}
		}
	}
	c.okf("every one of the %d links and images written in the %s sources is present on its served page", n, lang)
}

// resolve maps a source link target onto the URL /help must serve it at.
func resolve(key, target string) string {
	if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "#") {
		return target
	}
	link, frag, _ := strings.Cut(target, "#")
	resolved := path.Join("/docs/"+key+"/", link)
	if rest, ok := strings.CutPrefix(resolved, "/assets/docs/"); ok {
		return "/help/-/assets/" + path.Base(rest)
	}
	k := strings.TrimPrefix(resolved, "/docs/")
	if k == "reference/errors" {
		if frag == "" {
			return "/help"
		}
		return "/help#" + strings.ToUpper(frag)
	}
	if frag != "" {
		return "/help/" + k + "#" + frag
	}
	return "/help/" + k
}

// sourceTargets extracts the link and image targets of a Markdown body,
// outside fenced code and the raw blocks (MDX imports, components, HTML
// comments) the renderer drops.
func sourceTargets(body string) []string {
	var out []string
	fence, comment := false, false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "```"):
			fence = !fence
			continue
		case fence:
			continue
		case comment:
			if strings.Contains(t, "-->") {
				comment = false
			}
			continue
		case strings.HasPrefix(t, "<!--"):
			comment = !strings.Contains(t, "-->")
			continue
		case strings.HasPrefix(t, "<"), strings.HasPrefix(t, "import "):
			continue
		}
		for _, m := range srcLinkRe.FindAllStringSubmatch(stripCodeSpans(line), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// stripCodeSpans blanks `code` spans that hold no link syntax of their
// own: a span written inside a link's text keeps the link intact, while a
// span showing Markdown as an example is not a link.
func stripCodeSpans(line string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(line, '`')
		if i < 0 {
			b.WriteString(line)
			return b.String()
		}
		j := strings.IndexByte(line[i+1:], '`')
		if j < 0 {
			b.WriteString(line)
			return b.String()
		}
		span := line[i : i+j+2]
		b.WriteString(line[:i])
		if strings.Contains(span, "](") {
			b.WriteString(strings.Repeat(" ", len(span)))
		} else {
			b.WriteString(span)
		}
		line = line[i+j+2:]
	}
}

// liveErrors provokes one error on each surface and follows its code to
// its /help entry: the UI's taxonomized 404 and an API problem document.
func (c *crawler) liveErrors(lang string, codes map[string]bool) {
	st, _, body, err := c.get(lang, "/help/no-such-section/helpcrawl-missing-page")
	page := string(body)
	m := errChipRe.FindStringSubmatch(page)
	switch {
	case err != nil || st != http.StatusNotFound:
		c.failf("an unknown /help page (%s) answered %d %v, not a taxonomized 404", lang, st, err)
	case m == nil:
		c.failf("the 404 page (%s) renders no taxonomy code", lang)
	case !codes[m[1]]:
		c.failf("the 404 page (%s) renders %s, which is not in the catalog", lang, m[1])
	case !strings.Contains(page, `href="/help#`+m[1]+`"`):
		c.failf("the 404 page (%s) renders %s without a link to /help#%s", lang, m[1], m[1])
	default:
		c.okf("UI (%s): the 404 error block renders %s and links to /help#%s, an entry of the index", lang, m[1], m[1])
	}
	st, ct, body, err := c.get(lang, "/api/v1/content/helpcrawl/no-such-repository")
	var code, typ string
	if err == nil {
		code = jsonField(body, "code")
		typ = jsonField(body, "type")
	}
	switch {
	case err != nil || st != http.StatusNotFound || !strings.HasPrefix(ct, "application/problem+json"):
		c.failf("an unknown repository on the API (%s) answered %d %q %v, not a problem document", lang, st, ct, err)
	case !codes[code]:
		c.failf("the API problem document (%s) carries code %q, not a catalog code", lang, code)
	case typ != "/help#"+code:
		c.failf("the API problem document (%s) for %s has type %q, not /help#%s", lang, code, typ, code)
	default:
		c.okf("API (%s): the problem document carries %s and type /help#%s, an entry of the index", lang, code, code)
	}
}

// mentionedCodes: every code any served page names is a catalog code, so
// the reader who searches /help for it finds its entry.
func (c *crawler) mentionedCodes(codes map[string]bool) {
	seen := map[string]bool{}
	for lang, pages := range c.pages {
		for key, page := range pages {
			for _, code := range codeTextRe.FindAllString(docBody(page), -1) {
				seen[code] = true
				if !codes[code] {
					c.failf("/help/%s (%s) names %s, which has no entry in the catalog", key, lang, code)
				}
			}
		}
	}
	c.okf("the served pages name %d distinct taxonomy codes, every one with its entry on /help in both languages", len(seen))
}

// required checks the NFR-021 pages: published in both languages, served,
// and carrying a table where the requirement is one.
func (c *crawler) required(spec string, srcs map[string]map[string]source) {
	if spec == "" {
		return
	}
	var keys []string
	for _, item := range strings.Split(spec, ",") {
		key, kind, _ := strings.Cut(strings.TrimSpace(item), ":")
		if key == "" {
			continue
		}
		keys = append(keys, key)
		for _, lang := range []string{"en", "fr"} {
			if _, ok := srcs[lang][key]; !ok {
				c.failf("NFR-021: the required page %s is not published in %s", key, lang)
				continue
			}
			page, ok := c.pages[lang][key]
			if !ok {
				c.failf("NFR-021: the required page %s is not served in %s", key, lang)
				continue
			}
			if kind == "table" && !strings.Contains(docBody(page), "<table") {
				c.failf("NFR-021: the required page %s (%s) carries no table", key, lang)
			}
		}
	}
	c.okf("NFR-021: the %d required pages are published and served in English and in French: %s", len(keys), strings.Join(keys, ", "))
}

// -- sources -------------------------------------------------------------------

func loadSources(dir string) (map[string]source, error) {
	out := map[string]source{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excluded[rel] || (!strings.HasSuffix(rel, ".md") && !strings.HasSuffix(rel, ".mdx")) {
			return nil
		}
		raw, err := os.ReadFile(p) //nolint:gosec // G304: test-tool input path
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		rest, ok := strings.CutPrefix(text, "---\n")
		if !ok {
			return fmt.Errorf("%s: no frontmatter", p)
		}
		head, body, ok := strings.Cut(rest, "\n---\n")
		if !ok {
			return fmt.Errorf("%s: unterminated frontmatter", p)
		}
		var fm struct {
			Title string `yaml:"title"`
		}
		if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		key := strings.TrimSuffix(strings.TrimSuffix(rel, ".mdx"), ".md")
		out[key] = source{key: key, title: fm.Title, body: body}
		return nil
	})
	return out, err
}

func catalogCodes(file string) (map[string]bool, error) {
	raw, err := os.ReadFile(file) //nolint:gosec // G304: test-tool input path
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range codeConstRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s declares no taxonomy code", file)
	}
	return out, nil
}

// -- helpers -------------------------------------------------------------------

// text is the visible text of an HTML fragment.
func text(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(s, ""))), " ")
}

// jsonField reads one top-level string member of a JSON document.
func jsonField(body []byte, name string) string {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	s, _ := doc[name].(string)
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
