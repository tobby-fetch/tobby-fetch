// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"bytes"
	"sync"
)

// Window is the bounded in-memory window of recent log records the bundle
// carries (diagnostics.logRecords). It is an io.Writer fed with the JSON
// lines of the instance logger — one record per Write, as slog's JSON
// handler writes them — and keeps the last N, within a byte budget as well:
// the count alone would let 100000 records of 16 KiB hold a gigabyte. It is
// a window, never an archive: a passthrough instance logs to stdout and
// keeps nothing else, and the window dies with the process.
type Window struct {
	mu    sync.Mutex
	max   int
	lines [][]byte
	bytes int
}

// maxRecordBytes bounds one record: a pathological message must not turn
// a window of 2000 records into hundreds of megabytes.
const maxRecordBytes = 16 << 10

// maxWindowBytes bounds the whole window, whatever the record count.
const maxWindowBytes = 64 << 20

// NewWindow returns a window of n records (n < 1 keeps one).
func NewWindow(n int) *Window {
	if n < 1 {
		n = 1
	}
	return &Window{max: n}
}

// Write implements io.Writer: p is one log record (a trailing newline is
// dropped, an oversized record is truncated).
func (w *Window) Write(p []byte) (int, error) {
	n := len(p)
	rec := bytes.TrimRight(p, "\r\n")
	if len(rec) == 0 {
		return n, nil
	}
	if len(rec) > maxRecordBytes {
		rec = rec[:maxRecordBytes]
	}
	cp := append([]byte(nil), rec...)
	w.mu.Lock()
	w.lines = append(w.lines, cp)
	w.bytes += len(cp)
	for len(w.lines) > w.max || w.bytes > maxWindowBytes {
		w.bytes -= len(w.lines[0])
		w.lines[0] = nil
		w.lines = w.lines[1:]
	}
	w.mu.Unlock()
	return n, nil
}

// Records returns the window's records, oldest first, as JSON Lines.
func (w *Window) Records() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out bytes.Buffer
	for _, l := range w.lines {
		out.Write(l)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// Len is the number of records currently held.
func (w *Window) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.lines)
}
