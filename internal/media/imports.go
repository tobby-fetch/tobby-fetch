// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The freshness register (FR-052 amendment R-28).
//
// It lives in the INSTANCE STATE directory, never in the store, and the
// distinction is the whole requirement: the store is the medium, it
// changes hands, and a register carried on the medium would be rewritten
// by whoever holds it — which is exactly the accident the guard exists to
// catch. The same reasoning already put the schedule override in the state
// directory (R-16); this is the same rule applied to the same kind of
// fact.
//
// The guard is an anti-accident one and says so: the manifest is unsigned,
// so a hostile party can forge a timestamp. What it prevents is an
// operator re-importing last month's medium and silently rolling a zone
// backwards.

// importsFile is the register's file name inside the state directory.
const importsFile = "media-imports.json"

// ImportRecord is the last completed import of one zone.
type ImportRecord struct {
	// MediaID is the medium it came from (R-28: an incident traces back
	// to a physical object).
	MediaID string `json:"mediaId"`
	// ResolvedAt is that medium's resolution timestamp — the value the
	// next medium is compared against.
	ResolvedAt time.Time `json:"resolvedAt"`
	// ImportedAt is when this instance completed the import.
	ImportedAt time.Time `json:"importedAt"`
	// RunID correlates with the destination-side logs of that import.
	RunID string `json:"runId,omitempty"`
}

// Imports is the per-zone register of completed imports.
//
// Safe for concurrent use: the verification path reads it while an import
// completing on another task writes it.
type Imports struct {
	path string

	mu      sync.RWMutex
	records map[string]ImportRecord
}

// importsState is the on-disk document.
type importsState struct {
	Zones map[string]ImportRecord `json:"zones"`
}

// OpenImports loads the register for an instance.
//
// stateRoot may be empty — an instance running without a state directory,
// which the FR-075 authentication override permits. The register then has
// nowhere to persist, and Record says so rather than accepting a write
// that would evaporate on restart. Verification still runs; it simply has
// no previous import to compare against, which is the same position a
// brand-new instance is in.
//
// An unreadable or malformed register is a startup error, not a silent
// fallback to "no record": falling back would turn a corrupted file into a
// disabled guard, at exactly the moment nobody is looking.
func OpenImports(stateRoot string) (*Imports, error) {
	im := &Imports{records: map[string]ImportRecord{}}
	if stateRoot == "" {
		return im, nil
	}
	im.path = filepath.Join(stateRoot, importsFile)
	raw, err := os.ReadFile(im.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return im, nil
	case err != nil:
		return nil, fmt.Errorf("media: reading %s: %w", im.path, err)
	}
	var s importsState
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("media: %s is not readable as an import register: %w", im.path, err)
	}
	for zone, rec := range s.Zones {
		if zone == "" {
			return nil, fmt.Errorf("media: %s holds a record under an empty zone name", im.path)
		}
		im.records[zone] = rec
	}
	return im, nil
}

// Last returns the last completed import recorded for zone.
func (i *Imports) Last(zone string) (ImportRecord, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	rec, ok := i.records[zone]
	return rec, ok
}

// All returns every recorded zone, sorted by name — the reporting surface.
func (i *Imports) All() map[string]ImportRecord {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make(map[string]ImportRecord, len(i.records))
	for zone, rec := range i.records {
		out[zone] = rec
	}
	return out
}

// Record advances the register for zone.
//
// It is called ONLY on a completed import (R-28: "the record advances only
// on a completed import"). Recording on arrival instead would make a
// failed import poison the next legitimate one.
//
// The register never goes backwards: an admin who deliberately overrode
// the staleness guard to restore an older delivery does not thereby erase
// the fact that a newer one was imported. The record still names the
// medium that was imported last, so the log and the register agree.
func (i *Imports) Record(zone string, record *ImportRecord) error {
	if zone == "" {
		return fmt.Errorf("media: recording an import needs a zone identity")
	}
	rec := *record
	if i.path == "" {
		return fmt.Errorf("media: this instance has no state directory, so the import register cannot persist: " +
			"set state.root to keep the freshness guard (R-28) across restarts")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if previous, ok := i.records[zone]; ok && previous.ResolvedAt.After(rec.ResolvedAt) {
		rec.ResolvedAt = previous.ResolvedAt
	}
	if rec.ImportedAt.IsZero() {
		rec.ImportedAt = time.Now().UTC()
	}
	next := make(map[string]ImportRecord, len(i.records)+1)
	for z, r := range i.records {
		next[z] = r
	}
	next[zone] = rec
	if err := writeJSON(i.path, importsState{Zones: next}); err != nil {
		return err
	}
	i.records = next
	return nil
}

// Zones lists the zones the register knows, sorted.
func (i *Imports) Zones() []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]string, 0, len(i.records))
	for zone := range i.records {
		out = append(out, zone)
	}
	sort.Strings(out)
	return out
}
