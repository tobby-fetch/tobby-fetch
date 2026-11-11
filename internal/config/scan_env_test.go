// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"strings"
	"testing"
	"time"
)

func TestScanEnvironment(t *testing.T) {
	path := writeFile(t, "mode: mirror\n")
	t.Setenv(EnvScanMode, "block")
	t.Setenv(EnvScanThreshold, "5.5")
	t.Setenv(EnvScanMaxDBAge, "72h")
	t.Setenv(EnvScanMaxDBAgeAction, "block")
	t.Setenv(EnvScanTimeout, "3m")
	t.Setenv(EnvScanDBRepository, "mirror/ghcr.io/aquasecurity/trivy-db")
	t.Setenv(EnvScanTrivyPath, "/opt/trivy/trivy")
	t.Setenv(EnvScanTrivyDigest, "sha256:"+strings.Repeat("a", 64))
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Scan
	if s.Mode != ScanModeBlock || s.Threshold != 5.5 || time.Duration(s.MaxDBAge) != 72*time.Hour ||
		s.MaxDBAgeAction != ScanModeBlock || time.Duration(s.Timeout) != 3*time.Minute ||
		s.DBRepository != "mirror/ghcr.io/aquasecurity/trivy-db" || s.Trivy.Path != "/opt/trivy/trivy" ||
		s.Trivy.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Errorf("scan = %+v", s)
	}

	for _, tc := range []struct{ env, value, want string }{
		{EnvScanThreshold, "high", "invalid CVSS score"},
		{EnvScanMaxDBAge, "a week", "invalid duration"},
		{EnvScanTimeout, "soon", "invalid duration"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			if _, err := Load(path, true); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.env) {
				t.Errorf("error = %v; want %q naming %s", err, tc.want, tc.env)
			}
		})
	}
}

// TestBlockingScanIsHonoured: the two blocking opt-ins of 6.1 and R-13 are
// implemented, so an instance asking for them starts — they are no longer
// refused as pending.
func TestBlockingScanIsHonoured(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Scan.Mode = ScanModeBlock
	cfg.Scan.MaxDBAgeAction = ScanModeBlock
	for _, err := range splitJoined(cfg.validate(ScopeInstance)) {
		if strings.Contains(err, "scan.") {
			t.Errorf("a blocking scan setting is refused: %s", err)
		}
	}
	if time.Duration(Default().Scan.Timeout) != DefaultScanTimeout {
		t.Errorf("scan.timeout default = %v", Default().Scan.Timeout)
	}
	checkErr(t, validating(func(c *Config) { c.Scan.Timeout = 0 }), []string{"scan.timeout must be positive"})
}
