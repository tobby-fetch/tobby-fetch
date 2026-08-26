// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestJSONLinesWithStableKeys checks the FR-090 log contract: JSON Lines
// with the stable keys ts/level/msg, RFC 3339 UTC timestamps, and
// correlation fields preserved.
func TestJSONLinesWithStableKeys(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	WithRunID(l, "20260811T140322Z-1a2b3c4d").Info("ingredient pushed",
		KeyRecipe, "wordpress",
		KeyDigest, "sha256:9f2d",
	)

	line := buf.String()
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, line)
	}

	for key, want := range map[string]string{
		"level":   "INFO",
		"msg":     "ingredient pushed",
		KeyRunID:  "20260811T140322Z-1a2b3c4d",
		KeyRecipe: "wordpress",
		KeyDigest: "sha256:9f2d",
	} {
		if got, _ := rec[key].(string); got != want {
			t.Errorf("record[%q] = %q, want %q", key, got, want)
		}
	}

	ts, _ := rec["ts"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("ts %q is not RFC 3339: %v", ts, err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("ts %q is not UTC", ts)
	}
	if _, hasTime := rec["time"]; hasTime {
		t.Error(`record still has slog's default "time" key`)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn)
	l.Info("dropped")
	l.Warn("kept")
	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Error("info record passed a warn-level logger")
	}
	if !strings.Contains(out, "kept") {
		t.Error("warn record missing")
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"Error", slog.LevelError},
		{"WARN ", slog.LevelWarn}, // surrounding whitespace is trimmed
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", tc.input, got, err, tc.want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error(`ParseLevel("loud") must fail`)
	}
}

// TestTeeDuplicatesEveryRecordToBothDestinations locks the property both
// callers depend on: the task queue writes each record to the instance
// stream and to the task's own file, and a mirror instance writes it to
// the instance stream and to the operation log on the transport medium
// (FR-053). If either side could lose a record, the medium would arrive
// carrying a partial account of what was done to it.
func TestTeeDuplicatesEveryRecordToBothDestinations(t *testing.T) {
	var a, b bytes.Buffer
	logger := Tee(New(&a, slog.LevelInfo), New(&b, slog.LevelInfo)).
		With("run_id", "run_tee")

	logger.Info("first", "step", 1)
	logger.With("recipe", "wordpress@6.8.2").WithGroup("g").Info("second", "step", 2)

	for name, buf := range map[string]*bytes.Buffer{"a": &a, "b": &b} {
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("destination %s holds %d records, want 2: %q", name, len(lines), buf.String())
		}
		for i, line := range lines {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("destination %s record %d is not JSON: %v (%q)", name, i, err, line)
			}
			// Attributes attached before the split, and after it, must
			// reach both: WithAttrs and WithGroup are where a naive tee
			// keeps one branch and drops the other.
			if rec["run_id"] != "run_tee" {
				t.Errorf("destination %s record %d lost the correlation field: %v", name, i, rec)
			}
		}
		if !strings.Contains(lines[1], "wordpress@6.8.2") {
			t.Errorf("destination %s lost an attribute added after the tee: %q", name, lines[1])
		}
	}
	if a.String() != b.String() {
		t.Errorf("the two destinations diverged:\n a: %s\n b: %s", a.String(), b.String())
	}
}

// TestTeeReportsAFailingDestination: a medium that filled up, or a log
// file that cannot be written, must surface rather than be swallowed —
// and the OTHER destination must still receive the record.
func TestTeeReportsAFailingDestination(t *testing.T) {
	var good bytes.Buffer
	logger := Tee(New(failingWriter{}, slog.LevelInfo), New(&good, slog.LevelInfo))
	logger.Info("the medium is full")
	if good.Len() == 0 {
		t.Error("the healthy destination lost a record because the other one failed")
	}
}

// failingWriter is a destination that always refuses.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }
