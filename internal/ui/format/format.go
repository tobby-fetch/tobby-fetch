// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package format localizes dates, durations, and binary sizes for the web
// UI (ADR-0015 §7). It is the single place where numbers become sentences:
// the API serves raw bytes and RFC 3339 exclusively — localization is a
// template concern, or the FR-061 parity becomes untestable. The
// milestone-5 pre-flight (FR-055) inherits these formatters.
package format

import (
	"fmt"
	"strings"
	"time"
)

// Bytes renders a byte count in binary units, one decimal, localized:
// "1.5 GiB" in English, "1,5 Gio" in French. The exact byte count belongs
// in the element's title attribute (UI-SPEC).
func Bytes(lang string, n int64) string {
	const unit = 1024
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	if lang == "fr" {
		units = []string{"o", "Kio", "Mio", "Gio", "Tio", "Pio"}
	}
	if n < unit {
		return fmt.Sprintf("%d %s", n, units[0])
	}
	value, idx := float64(n), 0
	for value >= unit && idx < len(units)-1 {
		value /= unit
		idx++
	}
	s := fmt.Sprintf("%.1f", value)
	s = strings.TrimSuffix(s, ".0")
	if lang == "fr" {
		s = strings.ReplaceAll(s, ".", ",")
	}
	return s + " " + units[idx]
}

// Time renders t for display: ISO 8601 to the minute, UTC. Templates wrap
// it in <time datetime="…"> with the RFC 3339 form from TimeAttr.
func Time(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04")
}

// TimeAttr is the machine-readable datetime attribute value (RFC 3339).
func TimeAttr(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// Duration renders an elapsed duration compactly: "4 min 03 s", "2 h 05",
// "12 s" — identical in both languages (unit symbols, not words).
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min %02d s", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d h %02d", int(d.Hours()), int(d.Minutes())%60)
	}
}

// Ago renders how long ago t was, localized and coarse ("il y a 22 min").
// The exact timestamp belongs in the title/datetime attributes.
func Ago(lang string, t, now time.Time) string {
	d := now.Sub(t).Round(time.Minute)
	en := lang != "fr"
	switch {
	case d < time.Minute:
		if en {
			return "just now"
		}
		return "à l'instant"
	case d < time.Hour:
		if en {
			return fmt.Sprintf("%d min ago", int(d.Minutes()))
		}
		return fmt.Sprintf("il y a %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		if en {
			return fmt.Sprintf("%d h ago", int(d.Hours()))
		}
		return fmt.Sprintf("il y a %d h", int(d.Hours()))
	default:
		days := int(d.Hours()) / 24
		if en {
			if days == 1 {
				return "yesterday"
			}
			return fmt.Sprintf("%d days ago", days)
		}
		if days == 1 {
			return "hier"
		}
		return fmt.Sprintf("il y a %d jours", days)
	}
}

// TruncateMiddle shortens long relocated paths and digests around an
// ellipsis, keeping both ends readable (UI-SPEC: truncation in the middle,
// full value always available for copy). max is the rune budget.
func TruncateMiddle(s string, limit int) string {
	if limit < 5 {
		limit = 5
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	keep := limit - 1
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
