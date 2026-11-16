// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestUnitImportRecordsItsProvenance locks the FR-023 half of FR-098: a
// unit import leaves the store knowing it was one. Before R-17 the runner
// recorded nothing, so a unit-imported repository read as seeded content
// — pushed through /v2/ — and the FR-044 amendment's removal refused it
// with TBY-POL-003.
func TestUnitImportRecordsItsProvenance(t *testing.T) {
	for name, multi := range map[string]bool{"single manifest": false, "index": true} {
		t.Run(name, func(t *testing.T) {
			host := upstream(t)
			ref := host + "/team/tool:1.0.0"
			if multi {
				pushIndex(t, ref, "linux/amd64", "linux/arm64")
			} else {
				pushImage(t, ref)
			}
			dst := destStore(t)
			rep, err := Inspect(context.Background(), ref, dst)
			if err != nil {
				t.Fatal(err)
			}
			task := runTask(t, dst, ref, itemsFromReport(rep, nil))
			if task.Error != nil || task.Aggregate().Failed != 0 {
				t.Fatalf("import failed: %+v %+v", task.Error, task.Items)
			}
			p, ok := dst.ProvenanceOf(rep.Repository)
			if !ok || p.Class != store.ProvenanceUnitImport || p.Origin != "" {
				t.Fatalf("provenance of %s = %+v (recorded %v), want unit-import of network origin", rep.Repository, p, ok)
			}
			if c := dst.Classify(rep.Repository); c.Class != store.ProvenanceUnitImport {
				t.Errorf("classified %s, want unit-import", c.Class)
			}
		})
	}
}

// TestUnitImportNeverDowngradesAnotherClass keeps the ledger conservative:
// a unit import into a repository already recorded as seeded content keeps
// it seeded — the class that is NOT removable through the FR-044
// amendment — rather than making the pushed tags removable along with it.
func TestUnitImportNeverDowngradesAnotherClass(t *testing.T) {
	host := upstream(t)
	ref := host + "/team/tool:1.0.0"
	pushImage(t, ref)
	dst := destStore(t)
	rep, err := Inspect(context.Background(), ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.RecordSeed(rep.Repository); err != nil {
		t.Fatal(err)
	}
	task := runTask(t, dst, ref, itemsFromReport(rep, nil))
	if task.Error != nil {
		t.Fatalf("import failed: %+v", task.Error)
	}
	if p, _ := dst.ProvenanceOf(rep.Repository); p.Class != store.ProvenanceSeed {
		t.Errorf("class = %s, want seed kept", p.Class)
	}
}

// TestUnitImportRefusedWhenHardened locks hardening.disableUnitImport at
// the runner (FR-098): the last line of defence, which also holds for a
// task queued before the switch was set and resumed after a restart
// (FR-029). Nothing lands and the task names the setting.
func TestUnitImportRefusedWhenHardened(t *testing.T) {
	host := upstream(t)
	ref := host + "/team/tool:1.0.0"
	pushImage(t, ref)
	dst := destStore(t)
	rep, err := Inspect(context.Background(), ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	task := runTask(t, dst, ref, itemsFromReport(rep, nil), WithUnitImportDisabled(true))
	if task.Error == nil || task.Error.Code != taxonomy.CodeUnitImportDisabled {
		t.Fatalf("task error = %+v, want TBY-POL-005", task.Error)
	}
	if task.Error.Params["setting"] != "hardening.disableUnitImport" {
		t.Errorf("params = %v, want the setting named", task.Error.Params)
	}
	if _, ok := dst.ResolveTag(context.Background(), rep.Repository, rep.Tag); ok {
		t.Error("a refused import landed content")
	}
	if _, err := Inspect(context.Background(), ref, dst, WithUnitImportDisabled(true)); !hasCode(err, taxonomy.CodeUnitImportDisabled) {
		t.Errorf("inspection = %v, want TBY-POL-005", err)
	}
	for _, it := range task.Items {
		if it.Status == tasks.StatusDone {
			t.Errorf("item %s done under the switch", it.Name)
		}
	}
}

func hasCode(err error, code taxonomy.Code) bool {
	var te *taxonomy.Error
	return errors.As(err, &te) && te.Code() == code
}
