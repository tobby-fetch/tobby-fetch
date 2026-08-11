// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
)

// startServer runs a Server on an ephemeral port and returns its base URL
// plus the cancel that triggers graceful shutdown and a channel with Run's
// result.
func startServer(t *testing.T) (srv *Server, base string, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	srv = New("127.0.0.1:0", 2*time.Second, metrics.New(), logging.New(io.Discard, slog.LevelError))

	ctx, stop := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- srv.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server did not start listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(stop)
	return srv, "http://" + srv.Addr(), stop, res
}

func get(t *testing.T, url string) (code int, body string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // G107: fixed loopback URL under test control
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// TestProbeLifecycle covers FR-092: liveness always up, readiness 503 until
// declared ready, and 503 again while draining (FR-093).
func TestProbeLifecycle(t *testing.T) {
	srv, base, cancel, done := startServer(t)

	if code, _ := get(t, base+"/healthz"); code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", code)
	}
	if code, _ := get(t, base+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("readyz before ready = %d, want 503", code)
	}

	srv.SetReady(true)
	if code, _ := get(t, base+"/readyz"); code != http.StatusOK {
		t.Errorf("readyz after ready = %d, want 200", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil after clean drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown timed out")
	}
	if srv.ready.Load() {
		t.Error("readiness still true after drain")
	}
}

// TestGracefulDrainLetsInflightFinish verifies the FR-093 behavior: a
// request in flight when shutdown starts completes inside the grace period.
func TestGracefulDrainLetsInflightFinish(t *testing.T) {
	srv := New("127.0.0.1:0", 3*time.Second, metrics.New(), logging.New(io.Discard, slog.LevelError))
	release := make(chan struct{})
	srv.Handle("GET /slow", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = fmt.Fprint(w, "finished")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server did not start listening")
		}
		time.Sleep(5 * time.Millisecond)
	}

	type result struct {
		code int
		body string
	}
	inflight := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/slow")
		if err != nil {
			inflight <- result{}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		inflight <- result{resp.StatusCode, string(body)}
	}()
	time.Sleep(100 * time.Millisecond) // request is now blocked in the handler

	cancel()                           // shutdown starts, drain begins
	time.Sleep(100 * time.Millisecond) // give Shutdown time to stop the listener
	close(release)                     // let the in-flight request finish

	got := <-inflight
	if got.code != http.StatusOK || got.body != "finished" {
		t.Errorf("in-flight request = %+v, want 200/finished", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain timed out")
	}
}

// TestMetricsEndpoint covers the FR-091 socle: /metrics is scrapeable, and
// exposes the runtime collectors and the build-info family.
func TestMetricsEndpoint(t *testing.T) {
	_, base, _, _ := startServer(t)

	code, body := get(t, base+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", code)
	}
	for _, family := range []string{"go_goroutines", "process_", "tobby_build_info"} {
		if !strings.Contains(body, family) {
			t.Errorf("metrics output missing %q", family)
		}
	}
}

// TestOpenMetricsNegotiation asserts the endpoint honors OpenMetrics
// content negotiation (FR-091: OpenMetrics format).
func TestOpenMetricsNegotiation(t *testing.T) {
	_, base, _, _ := startServer(t)

	req, err := http.NewRequest(http.MethodGet, base+"/metrics", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "openmetrics-text") {
		t.Errorf("Content-Type = %q, want openmetrics-text", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasSuffix(strings.TrimSpace(string(body)), "# EOF") {
		t.Error("OpenMetrics exposition must end with # EOF")
	}
}

func TestFeatureHandlerMounts(t *testing.T) {
	srv := New("127.0.0.1:0", time.Second, metrics.New(), logging.New(io.Discard, slog.LevelError))
	srv.Handle("GET /v2/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server did not start listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ := get(t, "http://"+srv.Addr()+"/v2/"); code != http.StatusOK {
		t.Errorf("mounted handler = %d, want 200", code)
	}
}
