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

// TestThresholdIsAFiniteScore: NaN compares false with everything, so a
// range check written as "< 0 || > 10" let it through — and a NaN
// threshold turns every comparison of the policy into "false". NaN, the
// infinities and anything outside [0, 10] are refused, from the file as
// from the environment.
func TestThresholdIsAFiniteScore(t *testing.T) {
	for _, v := range []string{".nan", ".NaN", ".inf", "-.inf", "10.5", "-1"} {
		t.Run("file "+v, func(t *testing.T) {
			path := writeFile(t, "mode: mirror\nscan:\n  threshold: "+v+"\n")
			if _, err := Load(path, true); err == nil || !strings.Contains(err.Error(), "scan.threshold") {
				t.Errorf("threshold %s: error = %v; want scan.threshold refused", v, err)
			}
		})
	}
	path := writeFile(t, "mode: mirror\n")
	for _, v := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity", "10.5", "-0.1"} {
		t.Run("env "+v, func(t *testing.T) {
			t.Setenv(EnvScanThreshold, v)
			if _, err := Load(path, true); err == nil || !strings.Contains(err.Error(), EnvScanThreshold) {
				t.Errorf("%s=%s: error = %v; want it refused, naming the variable", EnvScanThreshold, v, err)
			}
		})
	}
	for _, v := range []string{"0", "7", "10"} {
		t.Setenv(EnvScanThreshold, v)
		if _, err := Load(path, true); err != nil {
			t.Errorf("%s=%s refused: %v", EnvScanThreshold, v, err)
		}
	}
}
