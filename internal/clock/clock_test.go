// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package clock

import (
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	build := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		at, now     time.Time
		implausible bool
		gap         time.Duration
	}{
		"clock after the anchor":     {build, build.Add(48 * time.Hour), false, -48 * time.Hour},
		"behind, within tolerance":   {build, build.Add(-30 * time.Minute), false, 30 * time.Minute},
		"behind, exactly tolerance":  {build, build.Add(-time.Hour), false, time.Hour},
		"behind, beyond tolerance":   {build, build.Add(-49 * time.Hour), true, 49 * time.Hour},
		"no anchor proves nothing":   {time.Time{}, build.AddDate(-10, 0, 0), false, 0},
		"a clock reset to the epoch": {build, time.Unix(0, 0).UTC(), true, build.Sub(time.Unix(0, 0).UTC())},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v := Check(AnchorBuild, c.at, c.now, time.Hour)
			if v.Implausible != c.implausible || v.Gap != c.gap || v.Anchor != AnchorBuild {
				t.Errorf("Check = %+v, want implausible=%v gap=%v", v, c.implausible, c.gap)
			}
		})
	}
}
