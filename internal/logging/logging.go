// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package logging builds the structured JSON loggers used across Tobby
// (FR-090, ADR-0012).
//
// Every record is a JSON object with the stable keys "ts" (RFC 3339 UTC),
// "level", and "msg", plus contextual correlation fields. Correlation fields
// carry stable names too: "run_id" identifies one synchronization run end to
// end (R-09), "task_id" one tracked task, then "recipe", "ingredient",
// "digest" as work narrows down. No third-party logging framework is used:
// the standard library's log/slog is the whole story.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// Correlation field names, fixed by the log schema (FR-090). Use these
// constants instead of literal strings so the schema stays greppable.
const (
	KeyRunID      = "run_id"
	KeyTaskID     = "task_id"
	KeyRecipe     = "recipe"
	KeyIngredient = "ingredient"
	KeyDigest     = "digest"
)

// New returns a JSON logger writing to w at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: stableKeys,
	}))
}

// stableKeys renames slog's built-in attributes to the schema's stable keys
// and normalizes timestamps to RFC 3339 in UTC.
func stableKeys(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		return slog.String("ts", a.Value.Time().UTC().Format(time.RFC3339Nano))
	case slog.LevelKey:
		return slog.String("level", a.Value.String())
	}
	return a
}

// Tee returns a logger that duplicates every record onto both loggers.
//
// Two destinations, one schema. The task queue writes each record to the
// instance stream and to the task's own file; a mirror instance writes it
// to the instance stream and to the operation log on the transport medium
// (FR-053). Both are the same need, and one implementation of it is what
// keeps the medium's log parseable by the same tooling as everything else.
func Tee(a, b *slog.Logger) *slog.Logger {
	return slog.New(teeHandler{a: a.Handler(), b: b.Handler()})
}

// teeHandler duplicates records to two handlers.
type teeHandler struct{ a, b slog.Handler }

func (h teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.a.Enabled(ctx, l) || h.b.Enabled(ctx, l)
}

func (h teeHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // hugeParam: slog.Handler's fixed signature
	// Both sides get their own copy: a handler may retain the record's
	// attributes, and slog documents Record as unsafe to share.
	err1 := h.a.Handle(ctx, r.Clone())
	err2 := h.b.Handle(ctx, r)
	return errors.Join(err1, err2)
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{a: h.a.WithAttrs(attrs), b: h.b.WithAttrs(attrs)}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{a: h.a.WithGroup(name), b: h.b.WithGroup(name)}
}

// ParseLevel maps a configuration string to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (expected debug, info, warn, or error)", s)
}

// WithRunID returns a logger that stamps every record with the given run ID
// (R-09/FR-090). Engine code obtains one from runid.New at the start of a
// synchronization and threads the returned logger through the whole run.
func WithRunID(l *slog.Logger, runID string) *slog.Logger {
	return l.With(KeyRunID, runID)
}
