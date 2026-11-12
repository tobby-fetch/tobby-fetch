// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command vulngate turns govulncheck's JSON stream into a gate: it fails on
// every REACHABLE finding (a call path into a vulnerable symbol, stdlib
// included), except those an allow-list entry covers. An entry covers a
// finding only when its ID and module match, the module in use is at least
// the version the entry declares fixed, and its review date has not passed.
// An expired entry fails the gate whether or not it is still matched: an
// exception nobody re-read is not an exception any more.
//
//	govulncheck -format json ./... | go run ./tools/vulngate tools/govulncheck-allow.yaml
//
// The tool exists for advisories whose database entry is wrong — a fixed
// version missing — and nothing else; every entry carries its proof.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Entry is one allow-listed advisory.
type Entry struct {
	ID string `yaml:"id"`
	// Module is the affected module path, as govulncheck names it.
	Module string `yaml:"module"`
	// FixedIn is the first version known fixed (from the upstream
	// advisory); the entry applies only at or above it.
	FixedIn string `yaml:"fixedIn"`
	// Justification states why the finding does not apply, with Evidence
	// (advisories, tests) beside it.
	Justification string   `yaml:"justification"`
	Evidence      []string `yaml:"evidence"`
	// ReviewBy is mandatory: past it, the entry fails the gate.
	ReviewBy string `yaml:"reviewBy"`
}

type allowFile struct {
	Entries []Entry `yaml:"entries"`
}

// finding is the part of a govulncheck JSON finding the gate reads.
type finding struct {
	OSV   string `json:"osv"`
	Trace []struct {
		Module   string `json:"module"`
		Version  string `json:"version"`
		Package  string `json:"package"`
		Function string `json:"function"`
	} `json:"trace"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: govulncheck -format json ./... | vulngate <allow.yaml>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1]) //nolint:gosec // G703: the allow-list path given by the wrapper script, read-only
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	problems, err := gate(raw, os.Stdin, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "vulngate:", err)
		os.Exit(2)
	}
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
	fmt.Println("vulngate: no reachable vulnerability outside the reviewed allow-list")
}

// gate returns the reasons to fail.
func gate(allowRaw []byte, stream io.Reader, now time.Time) ([]string, error) {
	var allow allowFile
	if err := yaml.Unmarshal(allowRaw, &allow); err != nil {
		return nil, fmt.Errorf("allow-list: %w", err)
	}
	var problems []string
	today := now.UTC().Format("2006-01-02")
	for i, e := range allow.Entries {
		switch {
		case e.ID == "" || e.Module == "" || e.FixedIn == "" || e.Justification == "" || len(e.Evidence) == 0 || e.ReviewBy == "":
			return nil, fmt.Errorf("allow-list entry %d (%s): id, module, fixedIn, justification, evidence and reviewBy are all required", i, e.ID)
		case parseDate(e.ReviewBy) == "":
			return nil, fmt.Errorf("allow-list entry %s: reviewBy %q is not a YYYY-MM-DD date", e.ID, e.ReviewBy)
		case e.ReviewBy < today:
			problems = append(problems, fmt.Sprintf("EXPIRED allow-list entry %s (%s): review was due %s — re-read the advisory and renew or remove it", e.ID, e.Module, e.ReviewBy))
		}
	}

	// Keyed by advisory AND module: one advisory may affect several
	// modules, the allow-list covers one, and a map keyed by the advisory
	// alone kept whichever finding came last — an allow-listed module
	// could hide a reachable one (B-053).
	type key struct{ id, module string }
	reached := map[key]finding{}
	dec := json.NewDecoder(stream)
	for {
		var msg struct {
			Finding *finding `json:"finding"`
		}
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("reading govulncheck output: %w", err)
		}
		f := msg.Finding
		if f == nil || len(f.Trace) == 0 || f.Trace[0].Function == "" {
			continue // module- or package-level: imported, not called
		}
		reached[key{f.OSV, f.Trace[0].Module}] = *f
	}
	keys := make([]key, 0, len(reached))
	for k := range reached {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].id != keys[j].id {
			return keys[i].id < keys[j].id
		}
		return keys[i].module < keys[j].module
	})
	for _, k := range keys {
		f, id := reached[k], k.id
		mod, ver := f.Trace[0].Module, f.Trace[0].Version
		where := fmt.Sprintf("%s@%s (%s.%s)", mod, ver, f.Trace[0].Package, f.Trace[0].Function)
		e, ok := lookup(allow.Entries, id, mod)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("REACHABLE %s in %s", id, where))
		case compareVersions(ver, e.FixedIn) < 0:
			problems = append(problems, fmt.Sprintf("REACHABLE %s in %s: allow-listed only from %s", id, where, e.FixedIn))
		default:
			fmt.Printf("allowed  %s in %s — %s (review by %s)\n", id, where, e.Justification, e.ReviewBy)
		}
	}
	return problems, nil
}

func lookup(entries []Entry, id, module string) (Entry, bool) {
	for _, e := range entries {
		if e.ID == id && e.Module == module {
			return e, true
		}
	}
	return Entry{}, false
}

func parseDate(s string) string {
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return ""
	}
	return s
}

// compareVersions compares two semantic versions ("v1.2.3", pre-release
// suffixes ranking below their release). An unparsable version compares
// lowest, so it is never covered.
func compareVersions(a, b string) int {
	pa, preA, okA := parseVersion(a)
	pb, preB, okB := parseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	case preA < preB:
		return -1
	}
	return 1
}

func parseVersion(v string) (out [3]int, pre string, ok bool) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, "", false
		}
		out[i] = n
	}
	return out, pre, true
}
