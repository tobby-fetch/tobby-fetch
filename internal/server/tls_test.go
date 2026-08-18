// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
)

// TestListenerServesTLS is the FR-082 acceptance seen from the listener:
// one listener carries the UI, the API, the embedded registry and the
// probes, so configuring TLS here covers all of them at once — there is
// no arrangement in which one of them is protected and another is not.
//
// The self-signed fallback is what the test configures, because that is
// the path an operator gets without supplying anything, and its
// fingerprint is the value FR-082 requires to be reportable.
func TestListenerServesTLS(t *testing.T) {
	cert, err := netx.NewServerCert(config.ServerTLS{Enabled: true}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	srv := New("127.0.0.1:0", 2*time.Second, metrics.New(), logging.New(io.Discard, slog.LevelError))
	srv.SetTLS(cert.TLSConfig())
	srv.SetReady(true)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the server did not start listening")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A client trusting only the generated certificate: anything else
	// answering would fail the handshake, so a success proves the
	// listener presented exactly what the instance reported.
	pair, err := cert.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(leaf)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+srv.Addr()+"/healthz", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the listener did not serve TLS: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test read side
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz over TLS = %d", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Fatal("the response carries no TLS state")
	}
	if got := netx.Fingerprint(resp.TLS.PeerCertificates[0].Raw); got != cert.Fingerprint() {
		t.Errorf("the listener presented %s, the instance reported %s", got, cert.Fingerprint())
	}

	// Plain HTTP against a TLS listener must not be served. Go answers
	// the mistake with a 400 explaining it rather than dropping the
	// connection, which is the right behavior — but the probe must not
	// come back OK, or an operator could believe the instance is in the
	// clear when it is not.
	plain := &http.Client{Timeout: 3 * time.Second}
	plainReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+srv.Addr()+"/healthz", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	presp, perr := plain.Do(plainReq)
	if perr == nil {
		defer presp.Body.Close() //nolint:errcheck // test read side
		if presp.StatusCode == http.StatusOK {
			t.Error("the TLS listener served a plain HTTP request")
		}
	}

	stop()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
}
