// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/help"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/ui/format"
)

// Templates travel inside the binary (NFR-003): the container image cannot
// ship a broken UI because there is no external asset directory to forget
// (ADR-0010, POC lesson).
//
//go:embed templates
var templatesFS embed.FS

// pages maps every full-page template. Each page file defines "title" and
// "main" and may define fragment blocks; the shared shell lives in
// layout.html. Standalone pages (login, pre-auth errors) carry their own
// document.
var pageFiles = []string{
	"about",
	"account",
	"admin-accounts",
	"admin-network",
	"admin-retriever",
	"api-docs",
	"content-list",
	"content-manifest",
	"content-repo",
	"dashboard",
	"error",
	"help",
	"help-page",
	"import",
	"login",
	"recipe-mapping",
	"recipe-plan",
	"recipe-publish",
	"recipes",
	"task-detail",
	"tasks",
}

// noHistoryPages lists the pages excluded from the htmx history cache
// (hx-history="false" stamped on <main>): screens that may carry a secret
// (ADR-0015 §5). Belt over the global historyCacheSize:0 — the attribute
// survives a configuration regression.
var noHistoryPages = map[string]bool{"admin-accounts": true, "account": true}

// parseTemplates builds one template set per page: layout + partials +
// the page file. Called once at startup; a parse error is a build defect.
func parseTemplates() map[string]*template.Template {
	funcs := template.FuncMap{
		"hasPrefix": strings.HasPrefix,
		// kindClass maps a kind onto its badge modifier (UI-SPEC §7: five
		// pills, never translated).
		"kindClass": kindClass,
		// withErr rebinds the frozen error-block partial onto another error
		// (per-item task failures): same View context, substituted Err.
		"withErr": func(v *View, e *ErrView) *View {
			cp := *v
			cp.Err = e
			return &cp
		},
		// withData rebinds a shared partial (task-badge) onto row data:
		// same View context — translations stay available — substituted
		// Data.
		"withData": func(v *View, d any) *View {
			cp := *v
			cp.Data = d
			return &cp
		},
	}
	base := template.Must(template.New("layout").Funcs(funcs).ParseFS(templatesFS,
		"templates/layout.html", "templates/partials/*.html"))
	out := make(map[string]*template.Template, len(pageFiles))
	for _, name := range pageFiles {
		t := template.Must(template.Must(base.Clone()).ParseFS(templatesFS,
			"templates/pages/"+name+".html"))
		out[name] = t
	}
	return out
}

// View is the data every template receives: request-scoped context plus
// the page's own Data. Its methods are the template helpers — translation
// first, so no visible string escapes the catalogs (FR-063).
type View struct {
	loc  *i18n.Localizer
	lang string

	Lang         string
	OtherLang    string
	Theme        string
	Path         string
	Identity     auth.Identity
	CSRF         string
	Mode         string
	Version      string
	AuthDisabled bool
	ShowUpcoming bool
	// HasThemeOverride switches the operator stylesheet link on (FR-064).
	HasThemeOverride bool
	// CanOperate and IsAdmin pre-compute the role gates for templates
	// (templates cannot convert string literals to auth.Role).
	CanOperate bool
	IsAdmin    bool
	// Banner stack (ADR-0015 §UI-SPEC): security first, operational after.
	Banners []Banner
	// NoHistory excludes the page from the htmx history cache (secret on
	// screen — ADR-0015 §5); the layout stamps hx-history="false" on <main>.
	NoHistory bool
	// Toasts are success confirmations rendered into #toasts with the page
	// (UI-SPEC §7: toasts carry successes only, every error is a block).
	Toasts []string
	// Err feeds the error page and the inline error block partial.
	Err *ErrView
	// Data is the page-specific payload.
	Data any
}

// Banner is one entry of the shell's banner stack.
type Banner struct {
	// Kind is "danger" (security, FR-075) or "warning" (operational).
	Kind string
	// Code anchors the banner's help link (/help#<code>); optional.
	Code string
	// Text is the localized banner sentence.
	Text string
}

// ErrView is a taxonomy error localized for templates.
type ErrView struct {
	Code        string
	What        string
	Cause       string
	Action      string
	HelpAnchor  string
	Correlation string
	// Detail is the verbatim technical cause of a persisted failure
	// (B-021), never localized and only ever set where one was recorded —
	// the task surfaces. What/Cause/Action above stay the catalog's
	// localized triple; this is the line that says what the registry, the
	// parser or the filesystem actually answered, and without it a
	// TBY-SRV-001 row explains nothing at all.
	Detail string
	// Status is the HTTP status, shown on full error pages.
	Status int
}

// T translates key with optional alternating name/value pairs:
// {{.T "nav.content"}} or {{.T "content.count" "Count" .N}}.
func (v *View) T(key string, pairs ...any) string {
	data := pairsToMap(pairs)
	return translate(v.loc, key, data, nil)
}

// TN translates a plural-aware key with the count and optional pairs.
func (v *View) TN(key string, count any, pairs ...any) string {
	data := pairsToMap(pairs)
	return translate(v.loc, key, data, count)
}

func pairsToMap(pairs []any) map[string]any {
	if len(pairs) == 0 {
		return nil
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if k, ok := pairs[i].(string); ok {
			m[k] = pairs[i+1]
		}
	}
	return m
}

// GuideLink is a screen's contextual pointer into the embedded
// documentation (NFR-003, amendment 2026-08-11: "screens SHOULD link into
// the relevant section contextually"). It is nil when the corpus carries
// no such page, so a screen can never render a help link that leads
// nowhere — the failure mode offline documentation exists to avoid.
type GuideLink struct {
	Href  string
	Title string
}

// Guide resolves a corpus page key into a link labelled with the page's
// own title, in the viewer's language. TestScreenGuidesExist walks the
// templates and fails on a key the corpus does not carry.
func (v *View) Guide(key string) *GuideLink {
	pg, _, ok := help.Load().Lookup(v.lang, key)
	if !ok {
		return nil
	}
	return &GuideLink{Href: "/help/" + key, Title: pg.Title}
}

// Formatting helpers (package format: the single localization point).

// FmtBytes renders n in localized binary units.
func (v *View) FmtBytes(n int64) string { return format.Bytes(v.lang, n) }

// FmtTime renders t ISO-8601 to the minute.
func (v *View) FmtTime(t time.Time) string { return format.Time(t) }

// TimeAttr renders the RFC 3339 datetime attribute.
func (v *View) TimeAttr(t time.Time) string { return format.TimeAttr(t) }

// FmtAgo renders a coarse localized "time ago".
func (v *View) FmtAgo(t time.Time) string { return format.Ago(v.lang, t, time.Now()) }

// FmtDuration renders an elapsed duration compactly.
func (v *View) FmtDuration(d time.Duration) string { return format.Duration(d) }

// Trunc shortens s around a middle ellipsis.
func (v *View) Trunc(s string, limit int) string { return format.TruncateMiddle(s, limit) }

// Renderer renders pages, fragments, and errors. One instance per process.
type Renderer struct {
	templates map[string]*template.Template
	logger    *slog.Logger

	// Version and Mode are shown in the shell (FR-001: displayed, never
	// switchable).
	Version string
	Mode    string
	// AuthDisabled and ShowUpcoming mirror the configuration.
	AuthDisabled     bool
	ShowUpcoming     bool
	HasThemeOverride bool
	// RelaxedScopes names the declared allowUnsigned trust scopes (FR-033):
	// a permanent danger banner surfaces the relaxed posture on every page,
	// like the FR-075 override.
	RelaxedScopes []string
	// AnonymousFileSets names the FileSets served without authentication
	// (FR-047 opt-in): a permanent warning banner, never silent.
	AnonymousFileSets []string
}

// NewRenderer parses the embedded templates.
func NewRenderer(logger *slog.Logger, version, mode string, authDisabled, showUpcoming, hasThemeOverride bool) *Renderer {
	return &Renderer{
		templates:        parseTemplates(),
		logger:           logger,
		Version:          version,
		Mode:             mode,
		AuthDisabled:     authDisabled,
		ShowUpcoming:     showUpcoming,
		HasThemeOverride: hasThemeOverride,
	}
}

// view assembles the request-scoped View.
func (rd *Renderer) view(r *http.Request, data any) *View {
	lang := requestLang(r)
	v := &View{
		loc:              localizer(lang),
		lang:             lang,
		Lang:             lang,
		OtherLang:        otherLang(lang),
		Theme:            requestTheme(r),
		Path:             r.URL.Path,
		Mode:             rd.Mode,
		Version:          rd.Version,
		AuthDisabled:     rd.AuthDisabled,
		ShowUpcoming:     rd.ShowUpcoming,
		HasThemeOverride: rd.HasThemeOverride,
		Data:             data,
	}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		v.Identity = id
		v.CanOperate = id.Role.AtLeast(auth.RoleOperator)
		v.IsAdmin = id.Role.AtLeast(auth.RoleAdmin)
	}
	if sess, ok := sessionFrom(r.Context()); ok {
		v.CSRF = sess.CSRF
	}
	// Banner stack, security first (UI-SPEC §7): the FR-075 override, the
	// FR-033 relaxed trust scopes, then the FR-047 anonymous FileSets.
	if rd.AuthDisabled {
		v.Banners = append(v.Banners, Banner{
			Kind: "danger",
			Text: v.T("banner.auth_disabled"),
		})
	}
	if len(rd.RelaxedScopes) > 0 {
		v.Banners = append(v.Banners, Banner{
			Kind: "danger",
			Text: v.T("banner.trust_relaxed", "Scopes", strings.Join(rd.RelaxedScopes, ", ")),
		})
	}
	if len(rd.AnonymousFileSets) > 0 {
		v.Banners = append(v.Banners, Banner{
			Kind: "warning",
			Text: v.T("banner.anonymous_filesets", "Names", strings.Join(rd.AnonymousFileSets, ", ")),
		})
	}
	return v
}

// errView localizes e for templates.
func errView(lang string, e *taxonomy.Error) *ErrView {
	m := taxonomy.Localize(lang, e)
	status := e.Entry().HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return &ErrView{
		Code:        string(m.Code),
		What:        m.What,
		Cause:       m.Cause,
		Action:      m.Action,
		HelpAnchor:  m.HelpAnchor,
		Correlation: m.Correlation,
		Status:      status,
	}
}

// Page renders the named page. With HX-Request (and no HX-Boosted), only
// the page's "main" block is swapped — same URL, no shadow routes
// (ADR-0015 §1); otherwise the full document.
func (rd *Renderer) Page(w http.ResponseWriter, r *http.Request, name string, data any) {
	rd.render(w, r, name, http.StatusOK, rd.view(r, data))
}

// Error renders e as a full page or an inline fragment block with its real
// HTTP status (ADR-0015 §6).
func (rd *Renderer) Error(w http.ResponseWriter, r *http.Request, e *taxonomy.Error) {
	v := rd.view(r, nil)
	v.Err = errView(v.lang, e)
	if isFragment(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(v.Err.Status)
		if err := rd.templates["error"].ExecuteTemplate(w, "error-block", v); err != nil {
			rd.logger.LogAttrs(r.Context(), slog.LevelError, "rendering error fragment",
				slog.String("error", err.Error()))
		}
		return
	}
	rd.render(w, r, "error", v.Err.Status, v)
}

// Fragment renders one named block of a page template — for htmx partial
// updates on canonical URLs.
func (rd *Renderer) Fragment(w http.ResponseWriter, r *http.Request, page, block string, data any) {
	v := rd.view(r, data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := rd.templates[page].ExecuteTemplate(w, block, v); err != nil {
		rd.logger.LogAttrs(r.Context(), slog.LevelError, "rendering fragment",
			slog.String("template", page+"#"+block), slog.String("error", err.Error()))
	}
}

// render executes the page: full document, or the "main" block for htmx
// partial navigation. Buffered so template failures yield a clean 500
// instead of a torn page.
func (rd *Renderer) render(w http.ResponseWriter, r *http.Request, name string, status int, v *View) {
	t, ok := rd.templates[name]
	if !ok {
		rd.logger.LogAttrs(r.Context(), slog.LevelError, "unknown template",
			slog.String("template", name))
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	v.NoHistory = noHistoryPages[name]
	target := "layout"
	if isFragment(r) {
		target = "main"
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, target, v); err != nil {
		rd.logger.LogAttrs(r.Context(), slog.LevelError, "rendering page",
			slog.String("template", name), slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// isFragment reports whether the request wants an htmx partial: HX-Request
// without HX-Boosted (a boosted navigation replaces the body and manages
// history, so it gets the full main; a true fragment swap targets an
// element). HX-History-Restore-Request also gets the full page.
func isFragment(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" &&
		r.Header.Get("HX-Boosted") == "" &&
		r.Header.Get("HX-History-Restore-Request") == ""
}

// StaticHandler serves the embedded assets under /static/ with ETag
// revalidation (no-cache + strong ETag): the browser always revalidates,
// the instance answers 304 from a precomputed digest — no stale asset can
// survive an upgrade, at the cost of one conditional request. The operator
// theme override is served from disk when configured (FR-064).
func StaticHandler(themeOverride string) http.Handler {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("ui: embedded static tree: %v", err))
	}
	etags := map[string]string{}
	if err := fs.WalkDir(staticSub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(staticSub, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		etags[path] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	}); err != nil {
		panic(fmt.Sprintf("ui: hashing static assets: %v", err))
	}
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/static/theme-override.css" {
			if themeOverride == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, themeOverride)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/static/")
		if tag, ok := etags[rel]; ok {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", tag)
			if r.Header.Get("If-None-Match") == tag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// staticFS embeds the stylesheet, the vendored JS, and their notices.
//
//go:embed static
var staticFS embed.FS

// kindClass maps an ingredient kind onto the .t-badge--* modifier of its
// pill (app.css). It accepts store.Kind and the plain-string kind of the
// importer report (same frozen names, RECIPE-SPEC §7).
func kindClass(kind any) string {
	k, ok := kind.(store.Kind)
	if !ok {
		if s, isString := kind.(string); isString {
			k = store.Kind(s)
		}
	}
	switch k {
	case store.KindContainerImage:
		return "image"
	case store.KindHelmChart:
		return "chart"
	case store.KindFileSet:
		return "fileset"
	case store.KindRecipe:
		return "recipe"
	default:
		return "artifact"
	}
}

// sanitizeNext validates a post-login redirect target: a local path,
// never protocol-relative, never a reserved prefix. Anything else falls
// back to the dashboard.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}
