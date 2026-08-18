// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package netx owns an instance's network edges: the single outbound
// transport every fetch path uses (FR-080, FR-081) and the certificate
// its listener presents (FR-082).
//
// The outbound side is one object, built once at startup and shared. That
// is not a tidiness argument. In a segmented enterprise zone direct egress
// is dropped rather than refused, so a fetch path that quietly built its
// own transport does not fail with "connection refused" — it hangs until
// its deadline, on every ingredient, forever. Having exactly one place
// where a transport can come from is what turns "is the proxy configured
// on every path?" into a question with one answer instead of one answer
// per file.
//
// Proxy credentials are the other reason this package exists. They are
// attached to a request's proxy URL at the last possible moment and are
// held nowhere a formatter can reach: the configured URL strings carry no
// userinfo, so no log line, no wrapped error, and no `tobby config dump`
// can print them (FR-080 acceptance, NFR-015).
package netx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Transport tunables. They are not configurable: an operator who needs a
// different idle-connection budget has a capacity problem these knobs do
// not solve, and every additional setting is another thing that can be
// set inconsistently across zones.
const (
	dialTimeout           = 30 * time.Second
	keepAlive             = 30 * time.Second
	tlsHandshakeTimeout   = 15 * time.Second
	responseHeaderTimeout = 60 * time.Second
	expectContinueTimeout = 1 * time.Second
	idleConnTimeout       = 90 * time.Second
	maxIdleConns          = 100
	maxIdleConnsPerHost   = 8
)

// Egress is the single outbound transport of a Tobby instance: the proxy
// selection (FR-080) and the trust store (FR-081) that every registry
// read, registry write, Helm-repository download, retriever fetch, and
// trust-root fetch goes through.
//
// It is safe for concurrent use and is meant to be built once. Callers
// take either RoundTripper — for go-containerregistry's
// remote.WithTransport — or Client, for the plain HTTP paths.
type Egress struct {
	transport *http.Transport

	// selector resolves a destination onto its proxy, honoring noProxy.
	// nil when no proxy is configured: the request goes direct.
	selector func(*url.URL) (*url.URL, error)

	// username and password authenticate to the proxy. password keeps
	// its Secret type all the way down, so the only place the value is
	// ever revealed is the single Reveal call in proxyFor.
	username string
	password config.Secret

	// Reporting state, all of it printable.
	proxyURL   string
	extraRoots int
	exclusive  bool
}

// New builds the instance's outbound transport from its network
// configuration. It fails rather than degrade: a proxy URL that does not
// parse or a CA bundle that holds no certificate is a startup error,
// because the degraded behavior — direct egress in a zone that blocks it
// — is indistinguishable from a hung instance.
func New(cfg *config.Network) (*Egress, error) {
	e := &Egress{
		username:  cfg.Proxy.Username,
		password:  cfg.Proxy.Password,
		exclusive: cfg.TLS.ExclusiveTrust,
	}

	if cfg.Proxy.Configured() {
		sel, err := proxySelector(&cfg.Proxy)
		if err != nil {
			return nil, err
		}
		e.selector = sel
		e.proxyURL = cfg.Proxy.URL
		if e.proxyURL == "" {
			e.proxyURL = cfg.Proxy.HTTPSURL
		}
	}

	pool, added, err := rootPool(&cfg.TLS)
	if err != nil {
		return nil, err
	}
	e.extraRoots = added

	e.transport = &http.Transport{
		Proxy: e.proxyFor,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: keepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
		TLSClientConfig: &tls.Config{
			// FR-081 is explicit that private authorities are trusted
			// WITHOUT disabling verification. InsecureSkipVerify is
			// therefore absent here and has no configuration path
			// anywhere in Tobby; RootCAs is how a private PKI is
			// admitted. A nil pool means the host's own store.
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
	}
	return e, nil
}

// direct is the egress of an instance that configured no network at all:
// no proxy, the host's public trust store. It is built once so that its
// connection pool is shared rather than duplicated per caller.
var direct = sync.OnceValue(func() *Egress {
	e, err := New(&config.Network{})
	if err != nil {
		// New only fails on operator input, and there is none here.
		panic("netx: the empty network configuration must always build: " + err.Error())
	}
	return e
})

// Direct returns the no-proxy, public-trust egress.
//
// It exists so that every call site holds the same type and no path has
// to invent a transport of its own: a component handed a nil Egress falls
// back here explicitly, which keeps test code short without giving
// production code a second way to reach the network. The wiring test is
// what proves no production path takes that fallback.
func Direct() *Egress { return direct() }

// Or returns e, or the direct egress when e is nil.
func Or(e *Egress) *Egress {
	if e == nil {
		return Direct()
	}
	return e
}

// RoundTripper exposes the shared transport, for go-containerregistry's
// remote.WithTransport and anything else that takes a RoundTripper.
func (e *Egress) RoundTripper() http.RoundTripper { return e.transport }

// Client returns an HTTP client over the shared transport, bounded by
// timeout. The transport is shared; only the deadline is per caller.
func (e *Egress) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: e.transport, Timeout: timeout}
}

// proxyFor is http.Transport.Proxy: it resolves the destination through
// the configured selection — honoring noProxy — and only then attaches
// the credentials.
//
// The ordering is the point. Credentials exist in exactly one URL, the
// one handed to the transport for this request, which nothing formats;
// the configured strings that DO get logged, dumped, and quoted in errors
// never held them in the first place (FR-080: "proxy credentials never
// appear in logs").
func (e *Egress) proxyFor(req *http.Request) (*url.URL, error) {
	if e.selector == nil {
		return nil, nil
	}
	u, err := e.selector(req.URL)
	if err != nil || u == nil {
		return u, err
	}
	if e.username == "" && e.password.IsZero() {
		return u, nil
	}
	authenticated := *u
	authenticated.User = url.UserPassword(e.username, e.password.Reveal())
	return &authenticated, nil
}

// ProxyURL returns the configured proxy in the only form it has: without
// credentials. Safe to log and to render on any reporting surface.
// Empty when no proxy is configured.
func (e *Egress) ProxyURL() string { return e.proxyURL }

// ProxyAuthenticated reports whether proxy credentials are configured —
// the fact, never the value.
func (e *Egress) ProxyAuthenticated() bool {
	return e.username != "" || !e.password.IsZero()
}

// ExtraRoots counts the certificate authorities added on top of the host
// store (FR-081), for the startup log line.
func (e *Egress) ExtraRoots() int { return e.extraRoots }

// ExclusiveTrust reports whether the public root store was dropped.
func (e *Egress) ExclusiveTrust() bool { return e.exclusive }

// CloseIdleConnections releases pooled connections. Shutdown calls it so
// a drained instance holds no proxy tunnel open past its grace period.
func (e *Egress) CloseIdleConnections() { e.transport.CloseIdleConnections() }

// proxySelector builds the destination-to-proxy function from the
// configuration, reusing the NO_PROXY matching semantics of the standard
// environment form (host, .suffix, CIDR, "*") so that an operator's
// existing no_proxy list means here exactly what it means everywhere else
// on the host.
func proxySelector(cfg *config.Proxy) (func(*url.URL) (*url.URL, error), error) {
	httpProxy, httpsProxy := cfg.URL, cfg.HTTPSURL
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	if httpProxy == "" {
		httpProxy = httpsProxy
	}
	// httpproxy resolves lazily and reports a bad URL only at use time,
	// once per request and swallowed into a transport error. Parse now,
	// so a typo is a startup refusal with the key that carries it.
	for key, raw := range map[string]string{"network.proxy.url": cfg.URL, "network.proxy.httpsURL": cfg.HTTPSURL} {
		if raw == "" {
			continue
		}
		if _, err := url.Parse(raw); err != nil {
			return nil, taxonomy.New(taxonomy.CodeProxyInvalid, taxonomy.Params{
				"setting": key,
				"proxy":   raw,
			}).WithCause(err)
		}
	}
	pc := &httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    strings.Join(cfg.NoProxy, ","),
	}
	return pc.ProxyFunc(), nil
}

// rootPool builds the outbound trust store: the host's public roots plus
// every configured authority (FR-081), or only the configured ones when
// exclusiveTrust narrows it. A nil pool means "the host's own store" and
// is what an unconfigured instance gets.
func rootPool(cfg *config.ClientTLS) (pool *x509.CertPool, added int, err error) {
	if !cfg.Configured() && !cfg.ExclusiveTrust {
		return nil, 0, nil
	}
	if cfg.ExclusiveTrust {
		pool = x509.NewCertPool()
	} else {
		pool, err = x509.SystemCertPool()
		if err != nil {
			return nil, 0, taxonomy.New(taxonomy.CodeTrustStore, taxonomy.Params{
				"source": "the host certificate store",
			}).WithCause(err)
		}
	}
	for _, path := range cfg.CAFiles {
		pem, rerr := os.ReadFile(path) //nolint:gosec // G304: the operator-configured CA bundle path is the feature (FR-081)
		if rerr != nil {
			return nil, 0, taxonomy.New(taxonomy.CodeTrustStore, taxonomy.Params{
				"source": path,
			}).WithCause(rerr)
		}
		n, cerr := appendPEM(pool, pem, path)
		if cerr != nil {
			return nil, 0, cerr
		}
		added += n
	}
	if cfg.CA != "" {
		n, cerr := appendPEM(pool, []byte(cfg.CA), "network.tls.ca")
		if cerr != nil {
			return nil, 0, cerr
		}
		added += n
	}
	return pool, added, nil
}

// appendPEM adds a bundle to the pool and counts what it contributed. A
// bundle that adds nothing is refused: silently trusting nothing extra is
// how an instance ends up failing every handshake with a message about
// the peer instead of about the empty file it was given.
func appendPEM(pool *x509.CertPool, pem []byte, source string) (int, error) {
	before := len(pool.Subjects()) //nolint:staticcheck // SA1019: Subjects is deprecated for system pools; this pool is ours, and counting what a bundle contributed is the only way to refuse an empty one
	if !pool.AppendCertsFromPEM(pem) {
		return 0, taxonomy.New(taxonomy.CodeTrustStore, taxonomy.Params{
			"source": source,
		}).WithCause(errors.New("no PEM CERTIFICATE block could be parsed"))
	}
	after := len(pool.Subjects()) //nolint:staticcheck // SA1019: see above
	if after == before {
		return 0, taxonomy.New(taxonomy.CodeTrustStore, taxonomy.Params{
			"source": source,
		}).WithCause(errors.New("the bundle added no new authority"))
	}
	return after - before, nil
}

// Describe renders the outbound posture as one line for the startup log
// and the reported configuration. It is built from the printable state
// only; there is no code path from a credential to this string.
func (e *Egress) Describe() string {
	var b strings.Builder
	if e.proxyURL == "" {
		b.WriteString("direct egress")
	} else {
		fmt.Fprintf(&b, "proxy %s", e.proxyURL)
		if e.ProxyAuthenticated() {
			fmt.Fprintf(&b, " (authenticated as %s, password %s)", e.username, e.password.Redact())
		}
	}
	switch {
	case e.exclusive:
		fmt.Fprintf(&b, ", trusting %d configured authorities only", e.extraRoots)
	case e.extraRoots > 0:
		fmt.Fprintf(&b, ", trusting %d configured authorities plus the host store", e.extraRoots)
	default:
		b.WriteString(", trusting the host certificate store")
	}
	return b.String()
}
