// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command proxy is the authenticated forward proxy of the milestone-4
// topologies: the only way out of a sealed segment, exactly as a
// segmented enterprise zone is built (FR-080).
//
// It exists in this repository rather than as a third-party image for two
// reasons. The scenarios must stay hermetic — no image pulled at test
// time beyond the ones the topology already uses — and the proxy has to
// be a WITNESS, not just a pipe: it refuses anonymous requests with 407,
// and it prints one line per authorized request naming the destination,
// so a scenario can prove that the bytes went through it instead of
// merely asserting that they arrived.
//
//	proxy -addr :3128            # PROXY_USERNAME / PROXY_PASSWORD in the environment
//
// Both proxying styles are implemented because Tobby uses both: plain
// HTTP through an absolute-URI request line, and TLS through CONNECT.
package main

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// hopByHop are the headers a proxy consumes rather than forwards
// (RFC 9110 §7.6.1). Proxy-Authorization is in the list: the upstream
// server must never see the credential this proxy checked.
var hopByHop = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// dialTimeout bounds one upstream connection attempt. Short on purpose:
// in a sealed segment an unreachable destination is a scenario failure to
// report, not a condition to wait out.
const dialTimeout = 10 * time.Second

func main() {
	addr := flag.String("addr", ":3128", "listen address")
	flag.Parse()

	username, password := os.Getenv("PROXY_USERNAME"), os.Getenv("PROXY_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("proxy: PROXY_USERNAME and PROXY_PASSWORD are required: " +
			"an unauthenticated proxy would not exercise FR-080")
	}

	p := &proxy{username: username, password: password}
	srv := &http.Server{
		Addr:    *addr,
		Handler: p,
		// No WriteTimeout: a CONNECT tunnel carrying an image layer is a
		// long-lived write by design. ReadHeaderTimeout is the bound that
		// matters for a proxy — it caps a client that opens a socket and
		// never finishes its request line.
		ReadHeaderTimeout: 30 * time.Second,
	}
	log.Printf("proxy: listening on %s, authenticating %s", *addr, username)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("proxy: %v", err)
	}
}

// proxy is the forward proxy handler.
type proxy struct {
	username, password string
}

// ServeHTTP authenticates first and proxies second. The order is the
// point: a request that arrives without credentials is answered 407 and
// never reaches a socket, which is what makes the scenario's
// "unauthenticated egress is refused" step meaningful.
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		log.Printf("proxy: 407 %s %s (missing or wrong proxy credentials)", r.Method, r.Host)
		w.Header().Set("Proxy-Authenticate", `Basic realm="tobby-topology"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		log.Printf("proxy: CONNECT %s", r.Host)
		p.tunnel(w, r)
		return
	}
	log.Printf("proxy: %s %s", r.Method, r.Host)
	p.forward(w, r)
}

// authorized checks the Basic credential in constant time.
func (p *proxy) authorized(r *http.Request) bool {
	const prefix = "Basic "
	header := r.Header.Get("Proxy-Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return false
	}
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(p.username)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(pass), []byte(p.password)) == 1
	return okUser && okPass
}

// tunnel serves CONNECT: the TLS bytes are relayed untouched, so the
// destination's certificate — issued by the scenario's private CA — is
// verified by Tobby end to end, through the proxy rather than by it.
func (p *proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	upstream, err := net.DialTimeout("tcp", r.Host, dialTimeout)
	if err != nil {
		log.Printf("proxy: CONNECT %s failed: %v", r.Host, err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer closeQuietly(upstream)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	client, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("proxy: hijacking the client connection: %v", err)
		return
	}
	defer closeQuietly(client)

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()
	_, _ = io.Copy(client, upstream)
	<-done
}

// forward serves a plain-HTTP proxied request.
func (p *proxy) forward(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "this is a forward proxy: absolute-URI requests only", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopByHop {
		out.Header.Del(h)
	}
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		log.Printf("proxy: %s %s failed: %v", r.Method, r.Host, err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer closeQuietly(resp.Body)

	header := w.Header()
	for name, values := range resp.Header {
		for _, v := range values {
			header.Add(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("proxy: relaying the response body of %s: %v", r.Host, err)
	}
}

// closeQuietly closes c and reports a failure on the log rather than
// swallowing it: a proxy that leaks sockets in a long scenario is a
// scenario that fails for the wrong reason.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		fmt.Fprintf(os.Stderr, "proxy: closing: %v\n", err)
	}
}
