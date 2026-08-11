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
