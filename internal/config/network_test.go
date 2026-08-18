// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"strings"
	"testing"
)

// TestNetworkLayersAndPrecedence exercises the FR-003 layering on the
// network settings of FR-080/FR-081: the file supplies structure, the
// environment overrides scalars and flattens lists, and a flag override
// wins over both.
func TestNetworkLayersAndPrecedence(t *testing.T) {
	path := writeFile(t, `mode: passthrough
network:
  proxy:
    url: http://file-proxy.example.com:3128
    noProxy:
      - internal.example.com
    username: fileuser
    password: file-password
  tls:
    caFiles:
      - /etc/tobby/ca.pem
`)

	t.Run("file", func(t *testing.T) {
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Network.Proxy.URL != "http://file-proxy.example.com:3128" {
			t.Errorf("proxy.url = %q", cfg.Network.Proxy.URL)
		}
		if cfg.Network.Proxy.Password.Reveal() != "file-password" {
			t.Error("the proxy password did not load from the configuration file")
		}
		if len(cfg.Network.TLS.CAFiles) != 1 {
			t.Errorf("tls.caFiles = %v", cfg.Network.TLS.CAFiles)
		}
	})

	t.Run("environment over file", func(t *testing.T) {
		t.Setenv(EnvNetworkProxyURL, "http://env-proxy.example.com:3128")
		t.Setenv(EnvNetworkProxyPassword, "env-password")
		t.Setenv(EnvNetworkProxyNoProxy, "a.example.com, .b.example.com ,")
		t.Setenv(EnvNetworkTLSCAFiles, "/run/secrets/ca.pem,/run/secrets/ca2.pem")
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Network.Proxy.URL != "http://env-proxy.example.com:3128" {
			t.Errorf("proxy.url = %q", cfg.Network.Proxy.URL)
		}
		if cfg.Network.Proxy.Password.Reveal() != "env-password" {
			t.Error("the environment did not override the file password")
		}
		if got := cfg.Network.Proxy.NoProxy; len(got) != 2 || got[0] != "a.example.com" || got[1] != ".b.example.com" {
			t.Errorf("proxy.noProxy = %v, want the two non-empty entries trimmed", got)
		}
		if len(cfg.Network.TLS.CAFiles) != 2 {
			t.Errorf("tls.caFiles = %v", cfg.Network.TLS.CAFiles)
		}
	})

	t.Run("flag over environment", func(t *testing.T) {
		t.Setenv(EnvNetworkProxyURL, "http://env-proxy.example.com:3128")
		cfg, err := Load(path, true, func(c *Config) {
			c.Network.Proxy.URL = "http://flag-proxy.example.com:3128"
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Network.Proxy.URL != "http://flag-proxy.example.com:3128" {
			t.Errorf("proxy.url = %q", cfg.Network.Proxy.URL)
		}
	})
}

// TestNetworkValidation locks the startup refusals. Each of these would
// otherwise degrade into a working-but-wrong instance, which in a zone
// with blocked egress means hanging rather than failing.
func TestNetworkValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantIn  string
		wantErr bool
	}{
		{
			name:    "a non-proxy scheme",
			mutate:  func(c *Config) { c.Network.Proxy.URL = "socks5://proxy.example.com:1080" },
			wantIn:  "forward-proxy scheme",
			wantErr: true,
		},
		{
			name:    "a URL without a host",
			mutate:  func(c *Config) { c.Network.Proxy.URL = "http://" },
			wantIn:  "no host",
			wantErr: true,
		},
		{
			name:    "credentials embedded in the URL",
			mutate:  func(c *Config) { c.Network.Proxy.URL = "http://user:pass@proxy.example.com:3128" },
			wantIn:  "network.proxy.username",
			wantErr: true,
		},
		{
			name: "credentials without a proxy",
			mutate: func(c *Config) {
				c.Network.Proxy.Username = "tobby"
				c.Network.Proxy.Password = NewSecret("x")
			},
			wantIn:  "without network.proxy.url",
			wantErr: true,
		},
		{
			name:    "noProxy without a proxy",
			mutate:  func(c *Config) { c.Network.Proxy.NoProxy = []string{"a.example.com"} },
			wantIn:  "no proxy to exempt",
			wantErr: true,
		},
		{
			name:    "exclusive trust with nothing to trust",
			mutate:  func(c *Config) { c.Network.TLS.ExclusiveTrust = true },
			wantIn:  "would trust nothing",
			wantErr: true,
		},
		{
			name: "a complete authenticated proxy",
			mutate: func(c *Config) {
				c.Network.Proxy.URL = "http://proxy.example.com:3128"
				c.Network.Proxy.HTTPSURL = "https://proxy.example.com:3129"
				c.Network.Proxy.NoProxy = []string{"internal.example.com"}
				c.Network.Proxy.Username = "tobby"
				c.Network.Proxy.Password = NewSecret("x")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Mode = ModePassthrough
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.wantErr && err == nil:
				t.Fatal("the configuration was accepted")
			case !tc.wantErr && err != nil:
				t.Fatalf("the configuration was refused: %v", err)
			case tc.wantErr && !strings.Contains(err.Error(), tc.wantIn):
				t.Errorf("error = %v, want it to mention %q", err, tc.wantIn)
			}
		})
	}
}

// TestNoGlobalSkipTLSVerifySetting is the structural half of the FR-081
// acceptance: the criterion is that no such switch exists in production
// configuration, so the test is over the configuration surface itself
// rather than over any behavior.
//
// It walks every YAML key the operator can write and fails on anything
// that reads like a verification bypass. A future field named
// "insecureSkipVerify", "skipTLSVerify" or "tlsVerify: false" fails here,
// which is the point: the requirement is about what cannot be configured.
func TestNoGlobalSkipTLSVerifySetting(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModePassthrough
	dump, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	// The dump renders every key, including the empty ones, so it is the
	// operator-visible configuration surface.
	forbidden := []string{
		"insecureskipverify",
		"skiptlsverify",
		"skipverify",
		"tlsverify",
		"disabletls",
		"insecuretls",
	}
	lowered := strings.ToLower(dump)
	for _, bad := range forbidden {
		if strings.Contains(lowered, bad) {
			t.Errorf("the configuration exposes %q: FR-081 requires private authorities to be trusted, never verification to be dropped\n%s", bad, dump)
		}
	}
	// registries.insecure survives on purpose — per host, explicit, and
	// about the scheme rather than about verification (FR-075).
	if !strings.Contains(lowered, "registries") {
		t.Error("the dump no longer renders the registries section")
	}
}

// TestServerTLSValidation locks the FR-082 refusals: a half-declared pair
// is an error naming what is missing, never a silent fall back to the
// self-signed certificate.
func TestServerTLSValidation(t *testing.T) {
	cases := []struct {
		name   string
		tls    ServerTLS
		wantIn string
	}{
		{"certificate without key", ServerTLS{CertFile: "/etc/tobby/tls.crt"}, "needs its private key"},
		{"key without certificate", ServerTLS{KeyFile: "/etc/tobby/tls.key"}, "without server.tls.certFile"},
		{"hosts alongside a supplied certificate", ServerTLS{
			CertFile: "/etc/tobby/tls.crt", KeyFile: "/etc/tobby/tls.key", Hosts: []string{"tobby.example.com"},
		}, "has no effect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Mode = ModePassthrough
			cfg.Server.TLS = tc.tls
			err := cfg.Validate()
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantIn)
			}
		})
	}
}

// TestServerTLSServes locks when the listener speaks TLS: on the explicit
// flag, and on a supplied certificate, which is a statement of intent on
// its own.
func TestServerTLSServes(t *testing.T) {
	cases := []struct {
		name string
		tls  ServerTLS
		want bool
	}{
		{"unset", ServerTLS{}, false},
		{"enabled", ServerTLS{Enabled: true}, true},
		{"certificate supplied", ServerTLS{CertFile: "a", KeyFile: "b"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tls.Serves(); got != tc.want {
				t.Errorf("Serves() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestServerTLSEnvironment locks the TOBBY_SERVER_TLS_* layer.
func TestServerTLSEnvironment(t *testing.T) {
	t.Setenv(EnvServerTLSEnabled, "true")
	t.Setenv(EnvServerTLSCertFile, "/run/tls/tls.crt")
	t.Setenv(EnvServerTLSKeyFile, "/run/tls/tls.key")
	cfg, err := Load(writeFile(t, "mode: mirror\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Server.TLS.Enabled || cfg.Server.TLS.CertFile != "/run/tls/tls.crt" || cfg.Server.TLS.KeyFile != "/run/tls/tls.key" {
		t.Errorf("server.tls = %+v", cfg.Server.TLS)
	}

	t.Setenv(EnvServerTLSEnabled, "perhaps")
	if _, err := Load(writeFile(t, "mode: mirror\n"), true); err == nil {
		t.Error("an invalid boolean was accepted")
	}
}

// TestSecretRoundTripDoesNotEchoTheValue locks the one thing the Secret
// type must never do, on the path that reads it: a malformed secret in
// the configuration file must be reported without quoting the value back.
func TestSecretRoundTripDoesNotEchoTheValue(t *testing.T) {
	path := writeFile(t, "mode: mirror\nnetwork:\n  proxy:\n    url: http://p.example.com:3128\n    password:\n      - not-a-scalar-but-secret\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("a non-scalar secret was accepted")
	}
	if strings.Contains(err.Error(), "not-a-scalar-but-secret") {
		t.Errorf("the error echoed the secret: %v", err)
	}
}
