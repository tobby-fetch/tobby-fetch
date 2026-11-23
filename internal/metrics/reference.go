// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

// The metrics reference (R-11, FR-091 amendment): every family Tobby
// exposes, its type, labels with their closed value sets, unit and the
// requirement it serves — GENERATED from the running registry, so a
// family cannot exist undocumented. The registry knows names, types, help
// and label names; what only a person knows (unit, requirement, the
// closed sets) is the table below, and the freshness test fails when the
// two disagree in either direction: a family without a row, a row without
// a family, a label value outside its declared set.
//
// The reference is published as a versioned release artifact beside
// openapi.yaml. Renaming or removing a family is a breaking change under
// the 1.x promise.

// ReferenceFormat versions the reference document.
const ReferenceFormat = 1

// Scope is the prefix the reference covers. The Go runtime and process
// collectors (go_*, process_*) are the standard families of the client
// library, whose set depends on the Go version and the platform; they are
// not Tobby's to version.
const Scope = "tobby_"

// Reference is the published document.
type Reference struct {
	Format int `json:"format"`
	// Scope says which families the document covers.
	Scope string `json:"scope"`
	// Note states what the document does not cover.
	Note     string      `json:"note"`
	Families []FamilyDoc `json:"families"`
}

// FamilyDoc documents one family.
type FamilyDoc struct {
	Name string `json:"name"`
	// Type is counter, gauge or histogram.
	Type string `json:"type"`
	Help string `json:"help"`
	// Unit is "seconds", "bytes", "timestamp_seconds" (Unix time) or
	// empty for a pure count or a flag.
	Unit   string     `json:"unit,omitempty"`
	Labels []LabelDoc `json:"labels,omitempty"`
	// Requirement is the SRS requirement, or the roadmap item, it serves.
	Requirement string `json:"requirement"`
	// Presence says when the family is exposed: "always" or the condition
	// under which it is absent (never a 0 standing for "not measured").
	Presence string `json:"presence"`
}

// LabelDoc documents one label. Values is its closed set; Open says what
// bounds a label whose values come from configuration instead.
type LabelDoc struct {
	Name   string   `json:"name"`
	Values []string `json:"values,omitempty"`
	Open   string   `json:"open,omitempty"`
}

type meta struct {
	unit, requirement, presence string
	labels                      []LabelDoc
}

const always = "always"

var (
	taxonomyCodes = "taxonomy codes (TBY-xxx-nnn): a closed catalog, never a host or a user-supplied string"
	declaredScope = "the names of the trust scopes declared in the configuration"
)

// metaTable is the part of the reference only a person can write.
var metaTable = map[string]meta{
	"tobby_build_info": {requirement: "FR-091", presence: always, labels: []LabelDoc{
		{Name: "version", Open: "one value per running binary: its version"}, {Name: "commit", Open: "one value per running binary: its commit"}}},
	"tobby_sync_transfers_inflight":         {requirement: "NFR-008, FR-091", presence: always},
	"tobby_sync_transferred_bytes_total":    {unit: "bytes", requirement: "FR-091", presence: always},
	"tobby_sync_parallelism":                {requirement: "NFR-008, R-11", presence: always},
	"tobby_policy_rejections_total":         {requirement: "FR-030, FR-091", presence: "after the first refusal", labels: []LabelDoc{{Name: "code", Open: taxonomyCodes}}},
	"tobby_promotion_pushes_total":          {requirement: "FR-013, FR-028", presence: always, labels: []LabelDoc{{Name: "result", Values: []string{"pushed", "skipped"}}}},
	"tobby_promotion_pushed_bytes_total":    {unit: "bytes", requirement: "FR-028", presence: always},
	"tobby_promotion_refusals_total":        {requirement: "FR-030, FR-033, FR-035", presence: "after the first refusal", labels: []LabelDoc{{Name: "code", Open: taxonomyCodes}}},
	"tobby_store_occupancy_bytes":           {unit: "bytes", requirement: "FR-045, R-33", presence: always},
	"tobby_store_occupancy_threshold_bytes": {unit: "bytes", requirement: "FR-045, R-33", presence: always},
	"tobby_store_occupancy_exceeded":        {requirement: "FR-045, R-33", presence: always},
	"tobby_sync_runs_total": {requirement: "FR-091, R-11", presence: always,
		labels: []LabelDoc{{Name: "outcome", Values: []string{OutcomeSuccess, OutcomeFailure}}}},
	"tobby_sync_duration_seconds": {unit: "seconds", requirement: "FR-091, R-11", presence: always,
		labels: []LabelDoc{{Name: "outcome", Values: []string{OutcomeSuccess, OutcomeFailure}}}},
	"tobby_sync_last_success_timestamp_seconds": {unit: "timestamp_seconds", requirement: "FR-091, R-11", presence: "after the first successful run since the process started"},
	"tobby_sync_ingredients": {requirement: "FR-026, FR-091", presence: "after the first completed run",
		labels: []LabelDoc{{Name: "status", Values: ingredientStatuses}}},
	"tobby_server_certificate_expiry_timestamp_seconds": {unit: "timestamp_seconds", requirement: "FR-082, R-11", presence: "while the listener serves TLS"},
	"tobby_clock_implausible": {requirement: "FR-096, R-32", presence: "per anchor, once checked (build at start, medium once one is opened)",
		labels: []LabelDoc{{Name: "anchor", Values: clockAnchors}}},
	"tobby_diagnostics_bundles_total": {requirement: "FR-095, R-30", presence: always,
		labels: []LabelDoc{{Name: "outcome", Values: bundleOutcomes}}},
	"tobby_scan_reblocked_items_total": {requirement: "FR-031, R-15", presence: always},
	"tobby_scan_exceptions": {requirement: "FR-031, R-15", presence: "on a serving instance (tobby serve), every state and source from the first scrape",
		labels: []LabelDoc{{Name: "state", Values: exceptionStates}, {Name: "source", Values: exceptionSources}}},
	"tobby_hardening_enabled": {requirement: "FR-098, R-17", presence: "on a serving instance (tobby serve), both settings from the first scrape",
		labels: []LabelDoc{{Name: "setting", Values: []string{SettingDisableUnitImport, SettingDisableRegistryPush}}}},
	"tobby_store_unmanaged_repositories": {requirement: "FR-098, R-17", presence: "on a serving instance (tobby serve), both classes; a scrape whose store walk fails leaves it out",
		labels: []LabelDoc{{Name: "class", Values: []string{ClassUnitImport, ClassSeed}}}},
	"tobby_scan_items_total":       {requirement: "FR-031", presence: always, labels: []LabelDoc{{Name: "verdict", Values: scanVerdicts}}},
	"tobby_scan_findings_total":    {requirement: "FR-031", presence: always, labels: []LabelDoc{{Name: "severity", Values: scanSeverities}}},
	"tobby_scan_policy_blocking":   {requirement: "FR-031", presence: "once the scanner state is known"},
	"tobby_scan_scanner_available": {requirement: "FR-031", presence: "once the scanner state is known"},
	"tobby_scan_db_present":        {requirement: "FR-032", presence: "once the scanner state is known"},
	"tobby_scan_db_age_seconds":    {unit: "seconds", requirement: "FR-032, R-13", presence: "while a vulnerability database is present"},
	"tobby_scan_db_stale":          {requirement: "FR-032, R-13", presence: "while a vulnerability database is present"},
	"tobby_auth_attempts_total": {requirement: "R-14, FR-072", presence: always,
		labels: []LabelDoc{{Name: "method", Values: authMethods}, {Name: "outcome", Values: authOutcomes}}},
	"tobby_auth_sessions_expired_total": {requirement: "R-14", presence: always, labels: []LabelDoc{{Name: "reason", Values: expiryReason}}},
	"tobby_auth_idp_reachable": {requirement: "R-20, FR-078", presence: "per federated method, once probed",
		labels: []LabelDoc{{Name: "method", Values: []string{"oidc", "saml"}}}},
	"tobby_auth_local_logins_under_federation_total": {requirement: "FR-078", presence: always},
	"tobby_integrity_verifications_total": {requirement: "FR-057, R-31", presence: always,
		labels: []LabelDoc{{Name: "verdict", Values: []string{VerdictClean, VerdictDefects}}}},
	"tobby_integrity_last_verification_timestamp_seconds": {unit: "timestamp_seconds", requirement: "FR-057, R-31", presence: "after the first completed verification"},
	"tobby_assurance_reduced_recipes": {requirement: "FR-037, R-22", presence: "per trust scope that relaxes verification (none declared: absent)",
		labels: []LabelDoc{{Name: "scope", Open: declaredScope}}},
}

// Describe builds the reference from a registry on which every family has
// been fed. It fails when the registry and the table disagree.
func Describe() (*Reference, error) {
	gathered, err := Fed().Gather()
	if err != nil {
		return nil, err
	}
	doc := &Reference{
		Format: ReferenceFormat, Scope: Scope,
		Note: "Covers the " + Scope + "* families. The go_* and process_* families are the standard Go runtime and process collectors of the Prometheus client library; their set depends on the Go version and the platform and is not versioned here.",
	}
	seen := map[string]bool{}
	for _, f := range gathered {
		name := f.GetName()
		if !strings.HasPrefix(name, Scope) {
			continue
		}
		m, ok := metaTable[name]
		if !ok {
			return nil, fmt.Errorf("metrics: %s is exposed but undocumented: add its row to metaTable (reference.go)", name)
		}
		seen[name] = true
		fd := FamilyDoc{
			Name: name, Type: strings.ToLower(f.GetType().String()), Help: f.GetHelp(),
			Unit: m.unit, Requirement: m.requirement, Presence: m.presence,
		}
		if err := checkLabels(f, m, &fd); err != nil {
			return nil, err
		}
		doc.Families = append(doc.Families, fd)
	}
	for name := range metaTable {
		if !seen[name] {
			return nil, fmt.Errorf("metrics: %s is documented but the registry does not expose it: remove its row or feed it in Fed", name)
		}
	}
	sort.Slice(doc.Families, func(i, j int) bool { return doc.Families[i].Name < doc.Families[j].Name })
	return doc, nil
}

// checkLabels holds the exposed series to the documented label sets.
func checkLabels(f *dto.MetricFamily, m meta, fd *FamilyDoc) error {
	declared := map[string]LabelDoc{}
	for _, l := range m.labels {
		declared[l.Name] = l
	}
	used := map[string]bool{}
	for _, metric := range f.GetMetric() {
		for _, l := range metric.GetLabel() {
			used[l.GetName()] = true
			d, ok := declared[l.GetName()]
			if !ok {
				return fmt.Errorf("metrics: %s carries label %q the reference does not document", f.GetName(), l.GetName())
			}
			if d.Values != nil && !member(d.Values, l.GetValue()) {
				return fmt.Errorf("metrics: %s{%s=%q} is outside its documented closed set %v", f.GetName(), l.GetName(), l.GetValue(), d.Values)
			}
		}
	}
	for _, l := range m.labels {
		if !used[l.Name] {
			return fmt.Errorf("metrics: %s documents label %q the registry does not expose", f.GetName(), l.Name)
		}
		d := l
		if d.Values != nil {
			d.Values = append([]string(nil), d.Values...)
			sort.Strings(d.Values)
		}
		fd.Labels = append(fd.Labels, d)
	}
	return nil
}

// Render serializes the reference as the published document: indented
// JSON, stable key order, newline-terminated — byte-identical for the
// same registry, which is what the freshness test and the signed release
// artifact rely on.
func Render(doc *Reference) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
