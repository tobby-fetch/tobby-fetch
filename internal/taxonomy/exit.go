// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy

import (
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
)

// The exhaustive exit-code table of the command line (FR-066, amendment
// 2026-08-11 / R-08).
//
// R-08 puts the table under the project's semantic-versioning promise:
// removing a row or renumbering one is a breaking change. A promise like
// that cannot rest on prose, so the table lives here — beside the classes
// it projects and beside the constants the process actually returns — and
// the published reference pages are generated from it and checked against
// it (TestPublishedExitCodeTableMatchesTheCode). Prose that drifts is
// caught by a failing build, not by the operator whose pipeline branched
// on a number that moved.

// ExitCode is one row of the published table: a process exit status and
// the stable machine name a pipeline can use for it.
type ExitCode struct {
	// Code is the process exit status.
	Code int
	// Name is the row's stable machine name ("policy"). It is part of the
	// same contract as the number and is never localized — a script that
	// maps names to numbers must keep working across languages.
	Name string
}

// Machine names of the table's rows. They are constants rather than
// literals because the JSON reports name them too: a report saying
// "verification" and a table saying "verification" have to be the same
// string by construction.
const (
	ExitNameOK             = "ok"
	ExitNameFailure        = "failure"
	ExitNameUsage          = "usage"
	ExitNamePolicy         = "policy"
	ExitNameVerification   = "verification"
	ExitNameChangesPlanned = "changes-planned"
)

// ExitCodes returns the exhaustive table, ordered by code. Every value
// the CLI can hand to the operating system is here: the two codes no
// taxonomy class produces (success, usage error), the three class
// projections, and the plan-mode outcome of FR-055's R-04 amendment.
func ExitCodes() []ExitCode {
	return []ExitCode{
		{Code: ExitOK, Name: ExitNameOK},
		{Code: ExitFailure, Name: ExitNameFailure},
		{Code: ExitUsage, Name: ExitNameUsage},
		{Code: ExitPolicy, Name: ExitNamePolicy},
		{Code: ExitVerification, Name: ExitNameVerification},
		{Code: ExitChangesPlanned, Name: ExitNameChangesPlanned},
	}
}

// Classes returns every class of the taxonomy. Go cannot enumerate the
// values of an integer type, and the completeness tests need something to
// walk: without this the "no exit code exists outside the table" half of
// the R-08 contract could only be asserted by hand.
func Classes() []Class { return []Class{ClassOperational, ClassPolicy, ClassVerification} }

// ExitCodeName returns the table's machine name for a process exit code,
// and whether the code is in the table at all.
func ExitCodeName(code int) (string, bool) {
	for _, e := range ExitCodes() {
		if e.Code == code {
			return e.Name, true
		}
	}
	return "", false
}

// ExitCodeMessage renders one row in lang: its short title and the
// meaning an operator reads. Both come from the shipped message catalogs,
// like every other user-visible string of this package.
func ExitCodeMessage(lang string, e ExitCode) (title, meaning string) {
	loc := i18n.NewLocalizer(bundle, lang)
	part := func(section string) string {
		s, err := loc.Localize(&i18n.LocalizeConfig{MessageID: "exit." + e.Name + "." + section})
		if err != nil {
			// A missing message is a build defect, caught by the catalog
			// completeness tests; degrade to the machine name rather than
			// to a blank cell.
			return e.Name
		}
		return s
	}
	return part("title"), part("meaning")
}

// ExitCodeTable renders the published Markdown table in lang. The
// documentation pages carry its output verbatim between generation
// markers, which is what makes "the table is generated from the code"
// checkable rather than aspirational.
func ExitCodeTable(lang string) string {
	loc := i18n.NewLocalizer(bundle, lang)
	header := func(id, fallback string) string {
		s, err := loc.Localize(&i18n.LocalizeConfig{MessageID: id})
		if err != nil {
			return fallback
		}
		return s
	}
	var b strings.Builder
	b.WriteString("| " + header("exit.column.code", "Code") +
		" | " + header("exit.column.name", "Name") +
		" | " + header("exit.column.meaning", "Meaning") + " |\n")
	b.WriteString("|---|---|---|\n")
	for _, e := range ExitCodes() {
		title, meaning := ExitCodeMessage(lang, e)
		b.WriteString("| `" + itoa(e.Code) + "` | `" + e.Name + "` | **" + title + "** — " + meaning + " |\n")
	}
	return b.String()
}

// itoa keeps the renderer free of a strconv import for six single-digit
// values, and states that the codes are small by construction.
func itoa(n int) string {
	if n < 0 || n > 9 {
		// Unreachable for the shipped table; a wider code would need a
		// real conversion, and this makes the assumption fail loudly
		// rather than print a wrong number.
		panic("taxonomy: exit code outside the single-digit range")
	}
	return string(rune('0' + n))
}
