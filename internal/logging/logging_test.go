// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package logging

import (
	"bytes"
	"encoding/json"
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
