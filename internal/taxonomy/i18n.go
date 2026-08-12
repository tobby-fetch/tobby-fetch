// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy

import (
	"embed"
	"fmt"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"
)

// Message catalogs travel inside the binary (NFR-003). English is the
// fallback language; tests enforce that both catalogs are complete, so the
// fallback never fires in a released build.
//
//go:embed messages/en.yaml messages/fr.yaml
var messagesFS embed.FS

var bundle = func() *i18n.Bundle {
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)
	for _, f := range []string{"messages/en.yaml", "messages/fr.yaml"} {
		if _, err := b.LoadMessageFileFS(messagesFS, f); err != nil {
			panic(fmt.Sprintf("taxonomy: loading %s: %v", f, err))
		}
	}
	return b
}()

// Message is one error localized for display: the three-part structure of
// R-03 plus the stable code and its troubleshooting anchor.
type Message struct {
	Code Code
	// What states what happened.
	What string
	// Cause states the probable cause.
	Cause string
	// Action states the corrective action, executable with the means of
	// the current milestone (roadmap DoD).
	Action string
	// HelpAnchor is the stable in-instance troubleshooting link,
	// "/help#TBY-REG-003". Anchors are real from milestone 2 (R-03).
	HelpAnchor string
	// Correlation carries the run/task identifier, when attached.
	Correlation string
}

// Localize renders e in lang ("en", "fr", or any matching BCP 47 tag;
// unknown languages fall back to English).
func Localize(lang string, e *Error) Message {
	loc := i18n.NewLocalizer(bundle, lang)
	part := func(section string) string {
		s, err := loc.Localize(&i18n.LocalizeConfig{
			MessageID:    string(e.code) + "." + section,
			TemplateData: map[string]any(e.params),
		})
		if err != nil {
			// A missing message is a build defect (tests walk the catalog);
			// degrade to the technical form rather than a blank zone.
			return e.Error()
		}
		return s
	}
	return Message{
		Code:        e.code,
		What:        part("what"),
		Cause:       part("cause"),
		Action:      part("action"),
		HelpAnchor:  "/help#" + string(e.code),
		Correlation: e.correlation,
	}
}

// Languages lists the shipped catalog languages, English first (the
// fallback). The UI language switcher and the completeness tests use it.
func Languages() []string { return []string{"en", "fr"} }
