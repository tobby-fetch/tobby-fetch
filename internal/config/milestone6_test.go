// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"strings"
	"testing"
	"time"
)

// TestMilestone6Defaults locks the safe defaults of the milestone-6
// surface: advisory scanning at 7.0 with a weekly database-age bound that
// only warns, a 30-minute idle timeout, no token expiry, and both
// federated methods off.
func TestMilestone6Defaults(t *testing.T) {
	cfg := Default()
	s := cfg.Scan
	if s.Mode != ScanModeWarn || s.Threshold != 7.0 || time.Duration(s.MaxDBAge) != 168*time.Hour || s.MaxDBAgeAction != ScanModeWarn {
		t.Errorf("scan defaults = %+v", s)
	}
	if time.Duration(cfg.Auth.Session.IdleTimeout) != 30*time.Minute {
		t.Errorf("auth.session.idleTimeout default = %v, want 30m", cfg.Auth.Session.IdleTimeout)
	}
	if cfg.Auth.Tokens.DefaultLifetime != 0 || cfg.Auth.OIDC.Enabled || cfg.Auth.SAML.Enabled {
		t.Errorf("auth defaults = %+v", cfg.Auth)
	}
	if err := validating(func(*Config) {}); err != nil {
		t.Errorf("the defaults do not validate: %v", err)
	}
}

// TestMilestone6FileKeys reads every new key from a configuration file,
// so the YAML names are pinned as the documented surface.
func TestMilestone6FileKeys(t *testing.T) {
	path := writeFile(t, `mode: mirror
scan:
  mode: warn
  threshold: 6.5
  maxDbAge: 72h
  maxDbAgeAction: warn
  dbRepository: ghcr.io/aquasecurity/trivy-db
  exceptions:
    - id: CVE-2024-12345
      justification: parser disabled
      expires: 2027-03-31T00:00:00Z
  trivy:
    ref: ghcr.io/aquasecurity/trivy
auth:
  session:
    idleTimeout: 10m
  saml:
    clockSkew: 30s
`)
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Scan
	if s.Threshold != 6.5 || time.Duration(s.MaxDBAge) != 72*time.Hour || len(s.Exceptions) != 1 ||
		!s.Exceptions[0].Expires.Equal(time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)) || s.Trivy.Ref != "ghcr.io/aquasecurity/trivy" {
		t.Errorf("scan = %+v", s)
	}
	if time.Duration(cfg.Auth.Session.IdleTimeout) != 10*time.Minute || time.Duration(cfg.Auth.SAML.ClockSkew) != 30*time.Second {
		t.Errorf("auth = %+v", cfg.Auth)
	}
}

func TestValidateScan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tweak func(*Config)
		want  []string
	}{
		{"defaults", func(*Config) {}, nil},
		{"unknown mode", func(c *Config) { c.Scan.Mode = "audit" }, []string{"scan.mode", `"warn" and "block"`}},
		{"unknown stale action", func(c *Config) { c.Scan.MaxDBAgeAction = "ignore" }, []string{"scan.maxDbAgeAction"}},
		{"threshold above 10", func(c *Config) { c.Scan.Threshold = 10.5 }, []string{"scan.threshold", "between 0 and 10"}},
		{"negative threshold", func(c *Config) { c.Scan.Threshold = -1 }, []string{"scan.threshold"}},
		{"a zero threshold hardens and is legitimate", func(c *Config) { c.Scan.Threshold = 0 }, nil},
		{"unchecked database age", func(c *Config) { c.Scan.MaxDBAge = 0 }, []string{"scan.maxDbAge must be positive"}},
		{"not a CVE", func(c *Config) { c.Scan.Exceptions = []ScanException{{ID: "GHSA-xxxx"}} }, []string{"scan.exceptions[0]", "CVE"}},
		{"duplicate exception", func(c *Config) {
			c.Scan.Exceptions = []ScanException{{ID: "CVE-2024-1234"}, {ID: "CVE-2024-1234"}}
		}, []string{"scan.exceptions[1]", "already listed"}},
		{"bad database repository", func(c *Config) { c.Scan.DBRepository = "/abs" }, []string{"scan.dbRepository"}},
		{"path and ref", func(c *Config) { c.Scan.Trivy = Trivy{Path: "/usr/bin/trivy", Ref: "ghcr.io/x/trivy"} }, []string{"exclusive"}},
		{"relative path", func(c *Config) { c.Scan.Trivy.Path = "trivy" }, []string{"must be absolute"}},
		{"digest without path", func(c *Config) { c.Scan.Trivy.Digest = "sha256:" + strings.Repeat("0", 64) }, []string{"set without it"}},
		{"malformed digest", func(c *Config) { c.Scan.Trivy = Trivy{Path: "/usr/bin/trivy", Digest: "md5:00"} }, []string{"sha256:<64"}},
		{"ref with a tag", func(c *Config) { c.Scan.Trivy.Ref = "ghcr.io/x/trivy@sha256:00" }, []string{"scan.trivy.ref"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Fragments only: "/usr/bin/trivy" is not absolute on Windows,
			// and the extra refusal there changes nothing a case asserts
			// (NFR-018).
			checkErr(t, validating(tc.tweak), tc.want)
		})
	}
}

func TestValidateAuthMilestone6(t *testing.T) {
	oidc := func(c *Config) {
		//nolint:gosec // G101: a test fixture naming a secret FILE path, not a credential
		c.Auth.OIDC = OIDC{
			Enabled: true, Issuer: "https://idp.example.com/realms/tobby", ClientID: "tobby",
			ClientSecretFile: "/etc/tobby/oidc-secret", RedirectURL: "https://tobby.example.com/-/oidc/callback",
			Roles: RoleMapping{Admin: []string{"tobby-admins"}},
		}
	}
	saml := func(c *Config) {
		c.Auth.SAML = SAML{
			Enabled: true, EntityID: "https://tobby.example.com/-/saml", ACSURL: "https://tobby.example.com/-/saml/acs",
			IDPMetadataFile: "/etc/tobby/idp.xml", CertFile: "/etc/tobby/sp.crt", KeyFile: "/etc/tobby/sp.key",
			ClockSkew: Duration(90 * time.Second), Roles: RoleMapping{Viewer: []string{"staff"}},
		}
	}
	for _, tc := range []struct {
		name  string
		tweak func(*Config)
		want  []string
	}{
		{"zero idle timeout", func(c *Config) { c.Auth.Session.IdleTimeout = 0 }, []string{"auth.session.idleTimeout must be positive"}},
		{"idle longer than the lifetime is tolerated", func(c *Config) { c.Auth.SessionTTL = Duration(10 * time.Minute) }, nil},
		{"negative token lifetime", func(c *Config) { c.Auth.Tokens.DefaultLifetime = -1 }, []string{"auth.tokens.defaultLifetime"}},
		{"oidc over http", func(c *Config) { oidc(c); c.Auth.OIDC.Issuer = "http://idp.example.com" }, []string{"auth.oidc.issuer", "https://"}},
		{"oidc none algorithm", func(c *Config) { oidc(c); c.Auth.OIDC.SigningAlgorithms = []string{"RS256", "none"} }, []string{`"none" is refused`}},
		{"oidc shared secret", func(c *Config) { oidc(c); c.Auth.OIDC.SigningAlgorithms = []string{"HS256"} }, []string{`"HS256" is refused`}},
		{"oidc incomplete", func(c *Config) { c.Auth.OIDC.Enabled = true }, []string{"auth.oidc.clientID", "clientSecretFile", "redirectURL", "maps no group"}},
		{"saml two metadata sources", func(c *Config) { saml(c); c.Auth.SAML.IDPMetadataURL = "https://idp.example.com/md" }, []string{"exactly one of"}},
		{"saml plain metadata URL", func(c *Config) {
			saml(c)
			c.Auth.SAML.IDPMetadataFile = ""
			c.Auth.SAML.IDPMetadataURL = "http://idp.example.com/md"
		}, []string{"must be https://"}},
		{"saml incomplete", func(c *Config) { c.Auth.SAML.Enabled = true }, []string{"auth.saml.entityID", "acsURL", "certFile", "maps no group"}},
		{"saml unbounded skew", func(c *Config) { c.Auth.SAML.ClockSkew = Duration(time.Hour) }, []string{"auth.saml.clockSkew"}},
		{"federation without authentication", func(c *Config) { oidc(c); c.Auth.Disabled = true }, []string{"nothing to federate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkErr(t, validating(tc.tweak), tc.want)
		})
	}
}

// TestPendingOptInsAreRefused is the guard of this branch: an opt-in the
// build does not implement yet is a refusal at instance scope — never an
// instance that starts and silently does none of it. Each case is deleted
// by the track that implements the behaviour.
func TestPendingOptInsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tweak func(*Config)
		want  string
	}{
		{"blocking scan", func(c *Config) { c.Scan.Mode = ScanModeBlock }, "scan.mode: block"},
		{"blocking stale database", func(c *Config) { c.Scan.MaxDBAgeAction = ScanModeBlock }, "scan.maxDbAgeAction: block"},
		{"token expiry", func(c *Config) { c.Auth.Tokens.DefaultLifetime = Duration(time.Hour) }, "auth.tokens.defaultLifetime"},
		{"oidc", func(c *Config) { c.Auth.OIDC.Enabled = true }, "auth.oidc.enabled is not implemented"},
		{"saml", func(c *Config) { c.Auth.SAML.Enabled = true }, "auth.saml.enabled is not implemented"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkErr(t, validating(tc.tweak), []string{tc.want})
			// A state-directory command is not an instance: it must not be
			// refused for a setting it never reads (R-34).
			cfg := Default()
			tc.tweak(&cfg)
			for _, err := range splitJoined(cfg.validate(ScopeState)) {
				if strings.Contains(err, "not implemented") {
					t.Errorf("ScopeState refused a pending opt-in: %s", err)
				}
			}
		})
	}
}

func splitJoined(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}
