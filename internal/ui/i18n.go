// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"embed"
	"fmt"
	"net/http"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"
)

// UI label catalogs (FR-063). Every visible string is a complete key with
// named variables; concatenation of translated fragments is forbidden
// (ADR-0015). The taxonomy carries its own catalogs.
//
//go:embed i18n/en.yaml i18n/fr.yaml
var labelsFS embed.FS

// langCookie stores the user's language override; without it the first hit
// negotiates on Accept-Language (ADR-0010). Cookie only at this milestone —
// the per-account preference joins the full account model at milestone 4.
const langCookie = "tobby_lang"

// themeCookie stores the theme choice; the server stamps data-theme on
// <html> from it — zero FOUC by construction. Default: dark, the canonical
// identity, then prefers-color-scheme via the client-side default.
const themeCookie = "tobby_theme"

var labelBundle = func() *i18n.Bundle {
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)
	for _, f := range []string{"i18n/en.yaml", "i18n/fr.yaml"} {
		if _, err := b.LoadMessageFileFS(labelsFS, f); err != nil {
			panic(fmt.Sprintf("ui: loading %s: %v", f, err))
		}
	}
	return b
}()

var langMatcher = language.NewMatcher([]language.Tag{language.English, language.French})

// requestLang resolves the request's language: cookie override first, then
// Accept-Language, then English.
func requestLang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil {
		switch c.Value {
		case "en", "fr":
			return c.Value
		}
	}
	tags, _, err := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if err != nil || len(tags) == 0 {
		return "en"
	}
	_, idx, _ := langMatcher.Match(tags...)
	if idx == 1 {
		return "fr"
	}
	return "en"
}

// requestTheme resolves the theme cookie: "light" or the canonical "dark".
func requestTheme(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// localizer builds the go-i18n localizer for lang.
func localizer(lang string) *i18n.Localizer {
	return i18n.NewLocalizer(labelBundle, lang)
}

// translate resolves key in lang with optional template data and plural
// count. A missing key renders as the bracketed key — loud in development,
// and the completeness test keeps it out of releases.
func translate(loc *i18n.Localizer, key string, data map[string]any, count any) string {
	cfg := &i18n.LocalizeConfig{MessageID: key, TemplateData: data}
	if count != nil {
		cfg.PluralCount = count
		if data == nil {
			cfg.TemplateData = map[string]any{"Count": count}
		}
	}
	s, err := loc.Localize(cfg)
	if err != nil {
		return "[" + key + "]"
	}
	return s
}

// messageIDs lists every message id of one embedded catalog — the
// completeness test walks both languages and diffs the sets.
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

// otherLang is the language the switcher toggles to (a French page offers
// English and vice versa).
func otherLang(lang string) string {
	if lang == "fr" {
		return "en"
	}
	return "fr"
}
