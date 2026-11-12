// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

// TestVerificationDoesNotStallWrites: a removal asked for during an
// integrity verification waits for it (HoldContent), but it must not
// stall content writes meanwhile. With the verification holding the
// shared side of gcMu, the pending exclusive lock — Go's RWMutex prefers
// writers — blocked every later write until the walk ended.
func TestVerificationDoesNotStallWrites(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()

	release := st.HoldContent()
	swept := make(chan error, 1)
	go func() { swept <- st.Sweep(ctx, slog.New(slog.DiscardHandler)) }()
	// Let the removal reach its lock and queue there.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-swept:
		release()
		t.Fatalf("the sweep ran during the verification (err %v): it must wait for it", err)
	default:
	}

	payload := []byte("written during a verification")
	wrote := make(chan error, 1)
	go func() {
		wrote <- st.WriteBlob(ctx, "docker.io/a/live", digest.FromBytes(payload), bytes.NewReader(payload))
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		release()
		if err := <-wrote; err != nil {
			t.Log(err)
		}
		t.Fatal("a content write stalled behind a removal waiting for the verification")
	}

	release()
	select {
	case err := <-swept:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep never ran once the verification ended")
	}
}
