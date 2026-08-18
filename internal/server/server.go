// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package server runs Tobby's HTTP listener: health probes (FR-092), the
// OpenMetrics endpoint (FR-091), and graceful shutdown (FR-093, ADR-0012).
// Feature handlers (registry /v2/, API, UI) mount onto the same listener
// through Handle.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
)

// Server is the single HTTP listener of a Tobby instance.
type Server struct {
	logger      *slog.Logger
	mux         *http.ServeMux
	httpSrv     *http.Server
	gracePeriod time.Duration

	// tls holds the listener's certificate source when TLS is
	// configured (FR-082); nil serves in the clear. One listener serves
	// the UI, the API and the embedded registry, so this single field
	// decides for all three — none of them can be left exposed while
	// another is protected.
	tls *tls.Config

	// ready gates /readyz: false during startup and drain (FR-092).
	ready atomic.Bool

	// addr holds the bound listener address once Run has started listening;
	// tests bind ":0" and read the effective address from Addr.
	addr atomic.Value
}

// New builds a server listening on addr, serving /healthz, /readyz, and
// /metrics from reg. gracePeriod bounds the drain on shutdown.
func New(addr string, gracePeriod time.Duration, reg *metrics.Registry, logger *slog.Logger) *Server {
	s := &Server{
		logger:      logger,
		mux:         http.NewServeMux(),
		gracePeriod: gracePeriod,
	}

	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		// OpenMetrics negotiation on top of the classic exposition format
		// (FR-091: the endpoint serves OpenMetrics).
		EnableOpenMetrics: true,
	}))

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handle mounts a feature handler on the shared listener. Call before Run.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// Mux exposes the shared mux so the web UI can register its route tree
// (the UI owns the root; machine surfaces keep the reserved prefixes —
// ADR-0015).
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// SetReady flips the readiness gate (FR-092: false until storage and
// configuration are usable).
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// SetTLS makes the listener serve TLS with cfg (FR-082). Call before Run.
// Nil leaves the listener in the clear — the shape an instance behind a
// terminating reverse proxy keeps.
func (s *Server) SetTLS(cfg *tls.Config) { s.tls = cfg }

// Addr returns the effective listen address once Run is serving. Empty
// before that.
func (s *Server) Addr() string {
	if v := s.addr.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// Run serves until ctx is canceled, then drains gracefully: readiness flips
// to 503 first, in-flight requests get the grace period to finish, then the
// listener closes (FR-093). Run returns nil after a clean drain.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.httpSrv.Addr, err)
	}
	s.addr.Store(ln.Addr().String())
	scheme := "http"
	if s.tls != nil {
		ln = tls.NewListener(ln, s.tls)
		scheme = "https"
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "http server listening",
		slog.String("addr", s.Addr()),
		slog.String("scheme", scheme))

	serveErr := make(chan error, 1)
	go func() {
		if err := s.httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// Drain: stop advertising readiness, then give in-flight requests the
	// grace period to complete.
	s.ready.Store(false)
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "shutting down",
		slog.String("grace_period", s.gracePeriod.String()))

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.gracePeriod)
	defer cancel()
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("draining http server: %w", err)
	}
	return nil
}
