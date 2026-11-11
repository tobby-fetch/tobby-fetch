// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"fmt"
	"strconv"
	"time"
)

// Environment variables of the scan section (FR-031, FR-032). The lists —
// scan.exceptions — are configuration-file only, like every other list
// whose entries carry more than one field.
const (
	EnvScanMode           = "TOBBY_SCAN_MODE"
	EnvScanThreshold      = "TOBBY_SCAN_THRESHOLD"
	EnvScanMaxDBAge       = "TOBBY_SCAN_MAX_DB_AGE"
	EnvScanMaxDBAgeAction = "TOBBY_SCAN_MAX_DB_AGE_ACTION"
	EnvScanTimeout        = "TOBBY_SCAN_TIMEOUT"
	EnvScanDBRepository   = "TOBBY_SCAN_DB_REPOSITORY"
	EnvScanTrivyPath      = "TOBBY_SCAN_TRIVY_PATH"
	EnvScanTrivyDigest    = "TOBBY_SCAN_TRIVY_DIGEST"
	EnvScanTrivyRef       = "TOBBY_SCAN_TRIVY_REF"
)

// applyScanEnv layers the scan variables over cfg.
func applyScanEnv(cfg *Config, lookup func(string) (string, bool)) error {
	s := &cfg.Scan
	for _, f := range []struct {
		env string
		dst *string
	}{
		{EnvScanMode, &s.Mode},
		{EnvScanMaxDBAgeAction, &s.MaxDBAgeAction},
		{EnvScanDBRepository, &s.DBRepository},
		{EnvScanTrivyPath, &s.Trivy.Path},
		{EnvScanTrivyDigest, &s.Trivy.Digest},
		{EnvScanTrivyRef, &s.Trivy.Ref},
	} {
		if v, ok := lookup(f.env); ok {
			*f.dst = v
		}
	}
	if v, ok := lookup(EnvScanThreshold); ok {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid CVSS score %q (expected e.g. \"7.0\")", EnvScanThreshold, v)
		}
		s.Threshold = n
	}
	for _, f := range []struct {
		env string
		dst *Duration
	}{
		{EnvScanMaxDBAge, &s.MaxDBAge},
		{EnvScanTimeout, &s.Timeout},
	} {
		if v, ok := lookup(f.env); ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: invalid duration %q (expected e.g. \"168h\")", f.env, v)
			}
			*f.dst = Duration(d)
		}
	}
	return nil
}
