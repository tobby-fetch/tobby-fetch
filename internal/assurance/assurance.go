// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package assurance is the vocabulary of the reduced-assurance marker
// (FR-037, roadmap R-22, milestone 6): what every surface carries for
// content admitted under a relaxed trust-root scope.
//
// The marker extends the FR-075 principle from configuration to content:
// a relaxed scope is already visible as a setting (banner, configuration
// dump); what it let in must stay visible too, item by item — in the task
// report, the API, the UI, a log field, a metric, and the per-recipe entry
// of the media manifest, which the destination side re-reads and
// re-displays. It is never written into a destination registry: content
// stays bit-exact.
//
// This package carries the shape only. Setting the marker where a
// relaxed scope admits a recipe, propagating it to the items the recipe
// reaches, and re-displaying the manifest's claim on the destination
// side are the milestone-6 assurance work.
//
// A marker read from a media manifest is a CLAIM of the source zone — the
// manifest is unsigned (ADR-0006, ADR-0016). The destination computes its
// own marker from its own trust verdict, and a manifest claiming full
// assurance never hides it.
package assurance

// Level is an assurance level. The set is closed; an absent marker means
// full assurance — signature verified against a configured trust root.
type Level string

// LevelReduced marks content admitted under a relaxed trust-root scope.
const LevelReduced Level = "reduced"

// Reason says what the relaxed scope waived.
type Reason string

// ReasonUnsigned means the recipe carried no signature and was admitted by a
// scope declaring allowUnsigned (FR-033). The only relaxation a scope can
// declare today — a roots restriction narrows trust and marks nothing.
const ReasonUnsigned Reason = "unsigned"

// Marker is the reduced-assurance marker carried by a recipe and by every
// item it reaches. Surfaces carry a *Marker: nil is full assurance.
type Marker struct {
	Level Level `json:"level"`
	// Scope is the name of the declared trust scope that admitted the
	// content (config trust.scopes[].name).
	Scope string `json:"scope"`
	// Reasons lists what the scope waived, never empty on a marker.
	Reasons []Reason `json:"reasons"`
}

// Reduced reports whether m marks reduced assurance. A nil marker does
// not.
func (m *Marker) Reduced() bool { return m != nil && m.Level == LevelReduced }

// ForUnsigned builds the marker of a recipe that carried no signature and
// was admitted by the declared scope named scope (FR-033, FR-037). It is
// the one constructor on purpose: every surface that sets a marker —
// the engine at admission, the store when it reads an older record, the
// media manifest writer, the destination's own verdict — builds it here,
// so the level and the reason cannot be spelled two ways.
func ForUnsigned(scope string) *Marker {
	return &Marker{Level: LevelReduced, Scope: scope, Reasons: []Reason{ReasonUnsigned}}
}

// ForAdmission is the marker a trust verdict leaves on the content it
// admitted: nil for a recipe whose signature verified against a trust
// root (full assurance), the reduced marker for one admitted unsigned
// inside a declared scope. A relaxation always names its scope (FR-033:
// no undeclared bypass exists), so an unsigned admission without one is
// not a state any caller can reach and yields nil rather than a marker
// naming nothing.
func ForAdmission(verified bool, scope string) *Marker {
	if verified || scope == "" {
		return nil
	}
	return ForUnsigned(scope)
}

// Log returns the value the log field KeyAssurance carries for m, and
// whether to emit the field at all: only a reduced marker is ever logged,
// so filtering on the field's presence finds exactly the records that
// concern content of reduced assurance.
func (m *Marker) Log() (value string, ok bool) {
	if !m.Reduced() {
		return "", false
	}
	return string(m.Level), true
}

// Valid reports whether a marker READ from a foreign document (the media
// manifest) is well formed: reduced, naming a scope, giving at least one
// known reason, and bounded in size. A claim is data from another zone;
// it is displayed, so it is held to the shape the surfaces expect.
func (m *Marker) Valid() bool {
	if m == nil || m.Level != LevelReduced || m.Scope == "" || len(m.Scope) > MaxScopeLen || len(m.Reasons) == 0 || len(m.Reasons) > MaxReasons {
		return false
	}
	for _, r := range m.Reasons {
		if r != ReasonUnsigned {
			return false
		}
	}
	return true
}

// Bounds on a marker read from a foreign document.
const (
	// MaxScopeLen bounds a scope name: names come from an operator's
	// configuration, never from content, and a claim is no exception.
	MaxScopeLen = 200
	// MaxReasons bounds the reasons list; there is one reason today.
	MaxReasons = 8
)
