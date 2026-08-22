// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Internal tests of the v0.4.2 hardening: the body-progress watchdog and
// the spool's survival of an abandoned store write. In-package on
// purpose — the stall window is injected through the unexported field
// (milliseconds instead of real two-minute sleeps), and the spool
// life-cycle under test is unexported by design.
package blobfetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestAFrozenBodyIsAbandonedAndRetried is the v0.4.2 famine fix: a
// source that answers the headers and a few bytes, then freezes without
// closing, used to block the read forever — ResponseHeaderTimeout had
// been paid, no deadline covered the body, and the engine's withRetries
// never fired because the call never returned. With one sync worker that
// is a global famine. The watchdog must abandon the attempt after the
// no-progress window, as a RETRYABLE operational error (each retry
// resumes from the checkpointed spool), and the whole Open must return
// in bounded time instead of hanging.
func TestAFrozenBodyIsAbandonedAndRetried(t *testing.T) {
	payload := bytes.Repeat([]byte("tobby"), 1024)
	dgst := digest.FromBytes(payload)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		requests.Add(1)
		// A few bytes, flushed so the client sees body progress start —
		// then silence with the connection held open: the failing-proxy
		// shape the watchdog exists for.
		_, _ = w.Write(payload[:4])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	repo, err := name.NewRepository(host+"/frozen/blob", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	r := New(nil, nil, t.TempDir(), config.Size(1))
	r.stall = 50 * time.Millisecond // milliseconds in tests, minutes in production

	start := time.Now()
	_, err = r.Open(context.Background(), repo, dgst, int64(len(payload)), nil)
	elapsed := time.Since(start)

	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeRegistryUnreachable {
		t.Fatalf("frozen body = %v, want %s", err, taxonomy.CodeRegistryUnreachable)
	}
	// Retryable in fact, not just in class: all inner attempts must have
	// hit the wire — the stall must never surface as the context
	// cancellation it is implemented with, which retryable() refuses.
	if got := requests.Load(); got != attempts {
		t.Errorf("origin saw %d blob requests, want %d (the stall error must stay retryable)", got, attempts)
	}
	// Bounded: three stall windows plus the backoffs, with slack for a
	// loaded CI host — nowhere near a hang, and no real two-minute wait.
	if elapsed > 15*time.Second {
		t.Errorf("Open took %s; the watchdog did not bound the frozen body", elapsed)
	}
}

// TestASpoolSurvivesAnAbandonedStoreWrite is the v0.4.2 spool fix: the
// verified spool used to be destroyed by Close unconditionally, even
// when the STORE write had failed mid-copy — throwing away verified
// bytes for a store-side fault, in zones where re-downloading is the
// most expensive way to get bytes already on disk. An abandoned reader
// must leave the spool in place for the next attempt; a drained reader
// must remove it, as before (a completed transfer leaves nothing behind
// in the state directory).
func TestASpoolSurvivesAnAbandonedStoreWrite(t *testing.T) {
	payload := bytes.Repeat([]byte("resume"), 4096)
	dgst := digest.FromBytes(payload)
	root := t.TempDir()

	fill := func(t *testing.T) *spool {
		t.Helper()
		sp, err := openSpool(root, dgst)
		if err != nil {
			t.Fatal(err)
		}
		if err := sp.rehash(); err != nil {
			t.Fatal(err)
		}
		if err := sp.append(context.Background(), bytes.NewReader(payload[sp.offset():]), int64(len(payload)), nil); err != nil {
			t.Fatal(err)
		}
		if !sp.verified() {
			t.Fatal("spool does not verify after a full append")
		}
		return sp
	}

	// A store write that fails mid-blob: the consumer reads a prefix and
	// closes without ever seeing EOF.
	sp := fill(t)
	rc, err := sp.reader(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sp.partPath); err != nil {
		t.Fatalf("the spool was destroyed by an abandoned store write: %v", err)
	}
	if _, err := os.Stat(sp.metaPath); err != nil {
		t.Fatalf("the spool sidecar was destroyed by an abandoned store write: %v", err)
	}

	// The next attempt finds the bytes complete: zero network cost.
	sp2, err := openSpool(root, dgst)
	if err != nil {
		t.Fatal(err)
	}
	if sp2.offset() != int64(len(payload)) {
		t.Fatalf("reopened spool offset = %d, want %d", sp2.offset(), len(payload))
	}
	if err := sp2.rehash(); err != nil {
		t.Fatal(err)
	}
	if !sp2.verified() {
		t.Fatal("reopened spool does not verify")
	}

	// A drained reader still cleans up: nothing may linger in the state
	// directory after a successful hand-off.
	rc2, err := sp2.reader(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("reopened spool served different bytes")
	}
	if err := rc2.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sp2.partPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part file still present after a drained reader: %v", err)
	}
	if _, err := os.Stat(sp2.metaPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sidecar still present after a drained reader: %v", err)
	}
}
