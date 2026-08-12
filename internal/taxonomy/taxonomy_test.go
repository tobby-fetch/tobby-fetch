// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// fakeParams builds a plausible value for every declared parameter, so the
// catalog walk can render each template.
func fakeParams(en Entry) Params {
	p := Params{}
	for _, name := range en.Params {
		p[name] = "X-" + name
	}
	return p
}

// TestCatalogComplete walks every entry × language × section: a missing or
// broken message is a build defect, not a runtime fallback (FR-063).
func TestCatalogComplete(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, en := range All() {
		for _, lang := range Languages() {
			m := Localize(lang, New(en.Code, fakeParams(en)))
			for section, got := range map[string]string{"what": m.What, "cause": m.Cause, "action": m.Action} {
				if got == "" {
					t.Errorf("%s: empty %s in %s", en.Code, section, lang)
				}
				if strings.Contains(got, string(en.Code)+" [") {
					t.Errorf("%s: %s in %s fell back to the technical form (missing message?): %q", en.Code, section, lang, got)
				}
				if strings.Contains(got, "<no value>") {
					t.Errorf("%s: %s in %s references an unsupplied parameter: %q", en.Code, section, lang, got)
				}
			}
			if m.HelpAnchor != "/help#"+string(en.Code) {
				t.Errorf("%s: wrong help anchor %q", en.Code, m.HelpAnchor)
			}
		}
	}
}

// TestCatalogPlaceholders enforces that every {{.param}} used by any
// message is declared in the entry's Params — in both directions and both
// languages: EN and FR must consume the same contract.
func TestCatalogPlaceholders(t *testing.T) {
	placeholder := regexp.MustCompile(`\{\{\s*\.(\w+)\s*\}\}`)
	for _, lang := range Languages() {
		data, err := messagesFS.ReadFile("messages/" + lang + ".yaml")
		if err != nil {
			t.Fatalf("reading %s catalog: %v", lang, err)
		}
		for _, en := range All() {
			declared := map[string]bool{}
			for _, p := range en.Params {
				declared[p] = true
			}
			used := map[string]bool{}
			for line := range strings.Lines(string(data)) {
				if !strings.HasPrefix(line, `"`+string(en.Code)+".") {
					continue
				}
				for _, m := range placeholder.FindAllStringSubmatch(line, -1) {
					used[m[1]] = true
				}
			}
			for p := range used {
				if !declared[p] {
					t.Errorf("%s (%s): template uses undeclared parameter %q", en.Code, lang, p)
				}
			}
			for p := range declared {
				if !used[p] {
					t.Errorf("%s (%s): declared parameter %q unused by any template", en.Code, lang, p)
				}
			}
		}
	}
}

// TestCodeShape locks the code contract: TBY-<domain>-<nnn>, never
// renumbered (the set may only grow).
func TestCodeShape(t *testing.T) {
	shape := regexp.MustCompile(`^TBY-[A-Z]{3,4}-\d{3}$`)
	for _, en := range All() {
		if !shape.MatchString(string(en.Code)) {
			t.Errorf("code %q does not match TBY-<domain>-<nnn>", en.Code)
		}
	}
}

func TestLocalizeFrench(t *testing.T) {
	e := New(CodeRegistryAuth, Params{"host": "docker.io"})
	m := Localize("fr", e)
	if want := "Le registre source a refusé l'authentification."; m.What != want {
		t.Errorf("fr what = %q, want %q", m.What, want)
	}
	if !strings.Contains(m.Cause, "docker.io") {
		t.Errorf("fr cause misses the host parameter: %q", m.Cause)
	}
	// Region tags resolve to the base language.
	if got := Localize("fr-FR", e).What; got != m.What {
		t.Errorf("fr-FR resolved differently: %q", got)
	}
	// Unknown languages fall back to English.
	if got := Localize("de", e).What; got != Localize("en", e).What {
		t.Errorf("unknown language did not fall back to English: %q", got)
	}
}

func TestErrorFormAndUnwrap(t *testing.T) {
	cause := errors.New("connect: connection refused")
	e := New(CodeRegistryUnreachable, Params{"host": "quay.io"}).WithCause(cause).WithCorrelation("run_x1")
	if got := e.Error(); got != "TBY-REG-002 [host=quay.io]: connect: connection refused" {
		t.Errorf("technical form = %q", got)
	}
	if !errors.Is(e, cause) {
		t.Error("wrapped cause lost")
	}
	var te *Error
	if !errors.As(fmt.Errorf("wrapped: %w", e), &te) || te.Code() != CodeRegistryUnreachable {
		t.Error("errors.As does not recover the taxonomy error")
	}
}

func TestPrincipalAggregation(t *testing.T) {
	op1 := New(CodeRegistryUnreachable, Params{"host": "a"})
	op2 := New(CodeStoreWrite, Params{"detail": "d"})
	pol := New(CodeNotAllowlisted, Params{"host": "b"})
	ver := New(CodeDigestMismatch, Params{"reference": "r", "expected": "e", "actual": "a"})

	cases := []struct {
		name string
		in   []*Error
		want Code
	}{
		{"severity wins over frequency", []*Error{op1, op1, op1, ver}, CodeDigestMismatch},
		{"policy over operational", []*Error{op1, pol, op2}, CodeNotAllowlisted},
		{"frequency breaks class ties", []*Error{op2, op1, op2}, CodeStoreWrite},
		{"lexicographic last resort", []*Error{op2, op1}, CodeRegistryUnreachable},
		{"nil entries ignored", []*Error{nil, op1, nil}, CodeRegistryUnreachable},
	}
	for _, tc := range cases {
		got := Principal(tc.in)
		if got == nil || got.Code() != tc.want {
			t.Errorf("%s: principal = %v, want %s", tc.name, got, tc.want)
		}
	}
	if Principal(nil) != nil || Principal([]*Error{nil}) != nil {
		t.Error("empty input must yield nil")
	}
}

func TestExitCodes(t *testing.T) {
	if got := New(CodeRegistryAuth, Params{"host": "h"}).Entry().Class.ExitCode(); got != ExitFailure {
		t.Errorf("operational exit = %d", got)
	}
	if got := New(CodeNotAllowlisted, Params{"host": "h"}).Entry().Class.ExitCode(); got != ExitPolicy {
		t.Errorf("policy exit = %d", got)
	}
	if got := New(CodeSignature, Params{"recipe": "r"}).Entry().Class.ExitCode(); got != ExitVerification {
		t.Errorf("verification exit = %d", got)
	}
}

func TestProblemRFC9457(t *testing.T) {
	e := New(CodeTaskNotFound, Params{"id": "tsk_missing"}).WithCorrelation("run_y2")
	rec := httptest.NewRecorder()
	WriteProblem(rec, "en", e)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q", ct)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if p.Code != CodeTaskNotFound || p.Status != 404 || p.Type != "/help#TBY-TSK-001" ||
		p.CorrelationID != "run_y2" || !strings.Contains(p.ProbableCause, "tsk_missing") {
		t.Errorf("problem document off contract: %+v", p)
	}
}

func TestTextRendering(t *testing.T) {
	e := New(CodeRegistryAuth, Params{"host": "docker.io"}).WithCorrelation("run_01j8m2vx4q")
	out := Text("fr", e)
	for _, want := range []string{"TBY-REG-003", "refusé l'authentification", "cause:", "action:", "/help#TBY-REG-003", "run_01j8m2vx4q"} {
		if !strings.Contains(out, want) {
			t.Errorf("text rendering misses %q:\n%s", want, out)
		}
	}
}

func TestUnknownCodePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New with an unknown code must panic")
		}
	}()
	_ = New(Code("TBY-XXX-999"), nil)
}
