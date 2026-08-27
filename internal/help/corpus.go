// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The documentation corpus travels inside the binary (NFR-003): an
// instance started in an isolated zone with no adjacent file serves the
// complete guides. corpus/ is written by tools/helpsync from
// website/src/content/docs and is never edited by hand — the divergence
// check is a CI gate.
//
//go:embed corpus
var corpusFS embed.FS

// The source paths, the exclusion list and the copy itself live in
// tools/helpsync. This package deliberately holds no path into the working
// tree: it must build and serve from the embedded bytes alone (NFR-003),
// and the tool that writes corpus/ must stay runnable even when corpus/
// has been emptied.

// sectionOrder is the reading order of the sections, the one
// website/astro.config.mjs gives the sidebar. Hard-coded rather than
// derived: alphabetical order would open the documentation on "air-gap"
// for a reader who has not yet installed anything. A section added to or
// removed from the corpus without appearing here fails TestSectionOrder.
var sectionOrder = []string{
	"discover",
	"try",
	"passthrough",
	"air-gap",
	"recipes",
	"security",
	"project",
	"reference",
}

// Page is one documentation page in one language.
type Page struct {
	// Key is the language-independent identity, "passthrough/operate".
	Key string
	// Section and Slug are the two halves of Key.
	Section string
	Slug    string
	// Lang is the language of Title, Description and the body — "en" for a
	// page served as the English fallback of a missing translation.
	Lang string

	Title       string
	Description string
	// BadgeText and BadgeVariant carry the frontmatter status badge
	// ("J5"/"Partial") of pages that are not fully available yet.
	BadgeText    string
	BadgeVariant string
	// Order is the position of the page inside its section.
	Order int

	body string
}

// frontmatter is the YAML head of a source page (website/README-docs.md).
type frontmatter struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Sidebar     struct {
		Order int `yaml:"order"`
		Badge struct {
			Text    string `yaml:"text"`
			Variant string `yaml:"variant"`
		} `yaml:"badge"`
	} `yaml:"sidebar"`
}

// Corpus is the loaded documentation: pages per language, the screenshots
// they reference, and the status data file.
type Corpus struct {
	// pages is lang -> key -> page. English is complete by construction;
	// French holds the translated subset (website/README-docs.md: the rest
	// falls back to English).
	pages map[string]map[string]*Page
	// order lists the keys of each section, in reading order.
	order map[string][]string
	// assets is the screenshot name -> bytes, with its ETag.
	assets map[string]asset
	status statusData
}

// asset is one embedded screenshot.
type asset struct {
	body []byte
	etag string
}

// corpus is loaded once at startup. A malformed corpus is a build defect —
// helpsync writes it and the tests walk it — so loading panics rather than
// degrading a screen that must work when nothing else does.
var corpus = mustLoad()

// Load returns the embedded corpus.
func Load() *Corpus { return corpus }

func mustLoad() *Corpus {
	c, err := load(corpusFS)
	if err != nil {
		panic(fmt.Sprintf("help: loading the embedded corpus: %v", err))
	}
	return c
}

func load(fsys fs.FS) (*Corpus, error) {
	c := &Corpus{
		pages:  map[string]map[string]*Page{},
		order:  map[string][]string{},
		assets: map[string]asset{},
	}
	err := fs.WalkDir(fsys, "corpus", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "corpus/")
		switch {
		case rel == "status.yaml":
			return yaml.Unmarshal(raw, &c.status)
		case strings.HasPrefix(rel, "assets/"):
			sum := sha256.Sum256(raw)
			c.assets[strings.TrimPrefix(rel, "assets/")] = asset{
				body: raw, etag: `"` + hex.EncodeToString(sum[:8]) + `"`,
			}
			return nil
		}
		lang, docPath, ok := strings.Cut(rel, "/")
		if !ok {
			return fmt.Errorf("%s: unexpected corpus entry", p)
		}
		pg, err := parsePage(lang, docPath, raw)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if c.pages[lang] == nil {
			c.pages[lang] = map[string]*Page{}
		}
		c.pages[lang][pg.Key] = pg
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(c.pages["en"]) == 0 {
		return nil, fmt.Errorf("no English page in the corpus")
	}
	c.buildOrder()
	return c, nil
}

// parsePage splits the frontmatter from the body and derives the page's
// identity from its path ("passthrough/operate.md").
func parsePage(lang, docPath string, raw []byte) (*Page, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, fmt.Errorf("no frontmatter")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	if fm.Title == "" {
		return nil, fmt.Errorf("frontmatter carries no title")
	}
	key := strings.TrimSuffix(strings.TrimSuffix(docPath, ".mdx"), ".md")
	section, slug, ok := strings.Cut(key, "/")
	if !ok {
		return nil, fmt.Errorf("page %q is not under a section", key)
	}
	return &Page{
		Key: key, Section: section, Slug: slug, Lang: lang,
		Title: fm.Title, Description: fm.Description,
		BadgeText: fm.Sidebar.Badge.Text, BadgeVariant: fm.Sidebar.Badge.Variant,
		Order: fm.Sidebar.Order,
		body:  body,
	}, nil
}

// buildOrder sorts each section's pages by their frontmatter order, then
// by key so the result never depends on map iteration (NFR-004: the same
// input renders the same page).
func (c *Corpus) buildOrder() {
	for key, pg := range c.pages["en"] {
		c.order[pg.Section] = append(c.order[pg.Section], key)
	}
	for section := range c.order {
		keys := c.order[section]
		sort.Slice(keys, func(i, j int) bool {
			a, b := c.pages["en"][keys[i]], c.pages["en"][keys[j]]
			if a.Order != b.Order {
				return a.Order < b.Order
			}
			return a.Key < b.Key
		})
	}
}

// Lookup returns the page for key in lang, falling back to English when
// the translation does not exist yet — the fallback website/README-docs.md
// documents. The second result reports that fallback so the screen can say
// so out loud rather than silently switching language on the reader.
func (c *Corpus) Lookup(lang, key string) (*Page, bool, bool) {
	if pg, ok := c.pages[lang][key]; ok {
		return pg, false, true
	}
	pg, ok := c.pages["en"][key]
	return pg, ok, ok
}

// Keys returns every page key, sorted. Tests and the link check walk it.
func (c *Corpus) Keys() []string {
	out := make([]string, 0, len(c.pages["en"]))
	for key := range c.pages["en"] {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Langs returns the corpus languages, English first.
func (c *Corpus) Langs() []string {
	out := make([]string, 0, len(c.pages))
	for lang := range c.pages {
		if lang != "en" {
			out = append(out, lang)
		}
	}
	sort.Strings(out)
	return append([]string{"en"}, out...)
}

// Translated reports whether key exists in lang without falling back.
func (c *Corpus) Translated(lang, key string) bool {
	_, ok := c.pages[lang][key]
	return ok
}

// NavPage is one entry of the help navigation.
type NavPage struct {
	Key   string
	Title string
	// BadgeText and BadgeVariant mirror the frontmatter status badge.
	BadgeText    string
	BadgeVariant string
	// Fallback marks an entry shown in English for lack of a translation.
	Fallback bool
}

// NavSection groups the pages of one section.
type NavSection struct {
	// ID is the section directory ("passthrough"); the screen translates
	// it through the UI catalogs, so no label travels in the corpus.
	ID    string
	Pages []NavPage
}

// Nav returns the whole corpus as a localized navigation, in reading
// order.
func (c *Corpus) Nav(lang string) []NavSection {
	out := make([]NavSection, 0, len(sectionOrder))
	for _, section := range sectionOrder {
		keys := c.order[section]
		if len(keys) == 0 {
			continue
		}
		ns := NavSection{ID: section, Pages: make([]NavPage, 0, len(keys))}
		for _, key := range keys {
			pg, fallback, ok := c.Lookup(lang, key)
			if !ok {
				continue
			}
			ns.Pages = append(ns.Pages, NavPage{
				Key: key, Title: pg.Title, BadgeText: pg.BadgeText,
				BadgeVariant: pg.BadgeVariant, Fallback: fallback,
			})
		}
		out = append(out, ns)
	}
	return out
}

// Sections returns the section identifiers present in the corpus, sorted —
// the set TestSectionOrder diffs against sectionOrder.
func (c *Corpus) Sections() []string {
	out := make([]string, 0, len(c.order))
	for section := range c.order {
		out = append(out, section)
	}
	sort.Strings(out)
	return out
}

// Asset returns one embedded screenshot with its ETag.
func (c *Corpus) Asset(name string) (body []byte, etag string, ok bool) {
	a, found := c.assets[name]
	return a.body, a.etag, found
}

// AssetNames returns every embedded screenshot name, sorted.
func (c *Corpus) AssetNames() []string {
	out := make([]string, 0, len(c.assets))
	for name := range c.assets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Rendered is one page rendered for display.
type Rendered struct {
	Page *Page
	// Fallback marks a page served in English for lack of a translation.
	Fallback bool
	// HTML is the page body. Every span of source text in it went through
	// contextual escaping (NFR-013); see markdown.go.
	HTML template.HTML
	// Headings is the page's own table of contents (level-2 headings).
	Headings []Heading
	// Neighbours are the previous and next pages of the section, for
	// sequential reading; either may be zero.
	Prev NavPage
	Next NavPage
}

// Heading is one anchored heading of a page.
type Heading struct {
	ID    string
	Text  string
	Level int
}

// Render renders key in lang. It returns false when key names no page —
// the routing layer turns that into the taxonomized 404, and since the
// lookup is a map hit on a fixed key set, no path a client can write ever
// reaches a file (NFR-011).
func (c *Corpus) Render(lang, key string, labels *Labels) (Rendered, bool) {
	r, ok := c.render(lang, key, labels)
	if !ok {
		return Rendered{}, false
	}
	return r.rendered, true
}

// CheckLinks renders every page of every language and returns the links
// that resolved to nothing. It is the offline link check NFR-003 asks for
// (TestNoDanglingLink); exported because that guard is worth running from
// anywhere the corpus is loaded, not only from this package's tests.
func (c *Corpus) CheckLinks(labels *Labels) []LinkIssue {
	var out []LinkIssue
	anchors := map[string]map[string]bool{}
	var frags []fragRef
	for _, lang := range c.Langs() {
		for _, key := range c.Keys() {
			r, ok := c.render(lang, key, labels)
			if !ok {
				continue
			}
			out = append(out, r.issues...)
			anchors[lang+"\x00"+key] = r.headings
			frags = append(frags, r.frags...)
		}
	}
	// Fragments are verified in a second pass: a link written on the first
	// page of the corpus may target a heading of the last one, and an
	// anchor that resolves to nothing is the failure mode this check
	// exists for (NFR-003 amendment: no dangling target).
	for _, f := range frags {
		if anchors[f.lang+"\x00"+f.targetKey][f.frag] {
			continue
		}
		out = append(out, LinkIssue{
			Lang: f.lang, Key: f.fromKey, Target: f.raw,
			Reason: fmt.Sprintf("page %q carries no heading anchored %q in %s", f.targetKey, f.frag, f.lang),
		})
	}
	return out
}

// fragRef is one cross-page anchor link, checked once every page's
// headings are known.
type fragRef struct {
	lang      string
	fromKey   string
	targetKey string
	frag      string
	raw       string
}

// pageRender is one completed rendering plus the diagnostics collected
// on the way.
type pageRender struct {
	rendered Rendered
	issues   []LinkIssue
	headings map[string]bool
	frags    []fragRef
}

func (c *Corpus) render(lang, key string, labels *Labels) (pageRender, bool) {
	pg, fallback, ok := c.Lookup(lang, key)
	if !ok {
		return pageRender{}, false
	}
	r := &renderer{corpus: c, page: pg, lang: pg.Lang, labels: labels}
	body := r.blocks(splitLines(pg.body), 0)
	out := Rendered{
		Page:     pg,
		Fallback: fallback,
		HTML:     template.HTML(body), //nolint:gosec // G203: not a raw injection — the string is assembled tag by tag from escaped source text and allowlisted SVG (NFR-013), see markdown.go, inline.go and svg.go.
		Headings: r.headings,
	}
	out.Prev, out.Next = c.neighbours(lang, pg)
	anchors := make(map[string]bool, len(r.headings))
	for _, h := range r.headings {
		anchors[h.ID] = true
	}
	return pageRender{rendered: out, issues: r.issues, headings: anchors, frags: r.frags}, true
}

// neighbours returns the pages surrounding pg inside its section.
func (c *Corpus) neighbours(lang string, pg *Page) (prev, next NavPage) {
	keys := c.order[pg.Section]
	nav := func(key string) NavPage {
		p, fb, ok := c.Lookup(lang, key)
		if !ok {
			return NavPage{}
		}
		return NavPage{Key: key, Title: p.Title, BadgeText: p.BadgeText,
			BadgeVariant: p.BadgeVariant, Fallback: fb}
	}
	for i, key := range keys {
		if key != pg.Key {
			continue
		}
		if i > 0 {
			prev = nav(keys[i-1])
		}
		if i+1 < len(keys) {
			next = nav(keys[i+1])
		}
	}
	return prev, next
}

// splitLines splits a body into lines without a trailing empty element.
func splitLines(body string) []string {
	lines := strings.Split(body, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// pagePath is the URL of a page under the /help namespace.
func pagePath(key string) string { return "/help/" + key }

// assetPath is the URL of an embedded screenshot. The "/-/" separator is
// the project's sub-resource convention (ADR-0015 §3): it keeps the asset
// namespace out of the page key space, where a page named "assets" would
// otherwise collide.
func assetPath(name string) string { return "/help/-/assets/" + path.Base(name) }
