// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
)

// TestRecordCarriesFullSchema checks the FR-094 acceptance criterion: every
// audit record carries all six schema fields plus the marker, and one filter
// on the marker separates audit records from operational logs.
func TestRecordCarriesFullSchema(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, slog.LevelInfo)

	logger.Info("operational noise")
	Log(context.Background(), logger, &Event{
		Actor:   "admin@example.test",
		Action:  "token.revoke",
		Target:  "token:ci-deploy",
		Outcome: OutcomeSuccess,
		Origin:  "192.0.2.10",
	})

	var auditRecords, total int
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		total++
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("not JSON Lines: %v", err)
		}
		if rec[KeyLogType] != LogType {
			continue // operational record: the single-filter criterion
		}
		auditRecords++
		for key, want := range map[string]any{
			"actor":        "admin@example.test",
			"action":       "token.revoke",
			"target":       "token:ci-deploy",
			"outcome":      OutcomeSuccess,
			"origin":       "192.0.2.10",
			"audit_schema": float64(SchemaVersion),
		} {
			if rec[key] != want {
				t.Errorf("audit record[%q] = %v, want %v", key, rec[key], want)
			}
		}
		if _, ok := rec["ts"].(string); !ok {
			t.Error("audit record has no ts timestamp")
		}
	}
	if total != 2 || auditRecords != 1 {
		t.Errorf("got %d records, %d audit; want 2 records, 1 audit", total, auditRecords)
	}
}

// TestOptionalFieldsOnlyWhenSet checks that actor_name and detail are
// additive: emitted when set, absent otherwise (FR-096 detail).
func TestOptionalFieldsOnlyWhenSet(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, slog.LevelInfo)
	Log(context.Background(), logger, &Event{
		Actor: "u", Action: "clock.implausible", Target: "clock", Outcome: OutcomeSuccess, Origin: "local",
		ActorName: "Operator", Detail: "gap 3h",
	})
	Log(context.Background(), logger, &Event{Actor: "u", Action: "a", Target: "t", Outcome: OutcomeSuccess, Origin: "o"})

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2", len(lines))
	}
	var full, bare map[string]any
	if err := json.Unmarshal(lines[0], &full); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &bare); err != nil {
		t.Fatal(err)
	}
	if full["actor_name"] != "Operator" || full["detail"] != "gap 3h" {
		t.Errorf("optional fields missing when set: %v", full)
	}
	if _, ok := bare["actor_name"]; ok {
		t.Error("actor_name emitted while unset")
	}
	if _, ok := bare["detail"]; ok {
		t.Error("detail emitted while unset")
	}
}

// TestMediaOverrideDetail: the detail of an applied media waiver names the
// operation, the zone and the medium, and leaves out what is unknown
// rather than writing an empty value (B-073).
func TestMediaOverrideDetail(t *testing.T) {
	for _, c := range []struct{ op, zone, id, want string }{
		{MediaOperationVerify, "zone-alpha", "20260826T000000Z-abcdef", "operation=verify zone=zone-alpha media=20260826T000000Z-abcdef"},
		{MediaOperationImport, "zone-alpha", "", "operation=import zone=zone-alpha"},
	} {
		if got := MediaOverrideDetail(c.op, c.zone, c.id); got != c.want {
			t.Errorf("MediaOverrideDetail(%q, %q, %q) = %q, want %q", c.op, c.zone, c.id, got, c.want)
		}
	}
}
