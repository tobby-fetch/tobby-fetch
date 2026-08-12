// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// stylesheetPath locates the design-token stylesheet under test (FR-064),
// relative to the package directory (mirrors thirdparty_test.go).
const stylesheetPath = "static/tokens.css"

// badgeInk is the fixed ink painted on solid accent/success fills
// (--tobby-on-accent in tokens.css).
const badgeInk = "#0b0d12"

// ratioAA is the WCAG 2.x AA minimum for normal text; ratioAAA the
// enhanced minimum required for the primary text-on-background pair.
const (
	ratioAA  = 4.5
	ratioAAA = 7.0
)

// tokenDeclRe matches one custom-property declaration inside a theme block.
var tokenDeclRe = regexp.MustCompile(`(--tobby-[a-z0-9-]+)\s*:\s*([^;]+);`)

// srgbColor is a color in sRGB space, channels in 0..255.
type srgbColor struct {
	r, g, b float64
}

// parseHexColor parses #rgb / #rrggbb CSS colors. Non-hex values (rgba(),
// keywords, var() references) report ok=false: alpha compositing depends on
// the backdrop and is out of scope for this test.
func parseHexColor(raw string) (srgbColor, bool) {
	val := strings.TrimSpace(strings.ToLower(raw))
	if !strings.HasPrefix(val, "#") {
		return srgbColor{}, false
	}
	digits := val[1:]
	if len(digits) == 3 {
		digits = string([]byte{
			digits[0], digits[0],
			digits[1], digits[1],
			digits[2], digits[2],
		})
	}
	if len(digits) != 6 {
		return srgbColor{}, false
	}
	var channels [3]float64
	for i := range channels {
		parsed, err := strconv.ParseUint(digits[2*i:2*i+2], 16, 8)
		if err != nil {
			return srgbColor{}, false
		}
		channels[i] = float64(parsed)
	}
	return srgbColor{r: channels[0], g: channels[1], b: channels[2]}, true
}

// relativeLuminance implements the WCAG 2.x definition: linearize each sRGB
// channel, then weight by the standard luminous-efficiency coefficients.
func relativeLuminance(c srgbColor) float64 {
	linear := func(v float64) float64 {
		s := v / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.r) + 0.7152*linear(c.g) + 0.0722*linear(c.b)
}

// contrastRatio returns the WCAG contrast ratio (L1+0.05)/(L2+0.05) with L1
// the lighter of the two luminances; the result is in 1..21.
func contrastRatio(a, b srgbColor) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if lb > la {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// themeTokens extracts the custom-property declarations of the theme block
// introduced by the given selector prefix in the stylesheet.
func themeTokens(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("selector %q not found in %s", selector, stylesheetPath)
	}
	rest := css[start:]
	open := strings.Index(rest, "{")
	end := strings.Index(rest, "}")
	if open < 0 || end < 0 || end < open {
		t.Fatalf("malformed block for selector %q in %s", selector, stylesheetPath)
	}
	tokens := make(map[string]string)
	for _, match := range tokenDeclRe.FindAllStringSubmatch(rest[open:end], -1) {
		tokens[match[1]] = strings.TrimSpace(match[2])
	}
	if len(tokens) == 0 {
		t.Fatalf("no --tobby-* declarations in block %q of %s", selector, stylesheetPath)
	}
	return tokens
}

// tokenValue returns the declared value of a token, failing the test if the
// theme block does not declare it.
func tokenValue(t *testing.T, tokens map[string]string, name string) string {
	t.Helper()
	val, ok := tokens[name]
	if !ok {
		t.Fatalf("token %s not declared in this theme block of %s", name, stylesheetPath)
	}
	return val
}

// assertContrast fails the (sub)test when the fg/bg pair renders below
// minRatio. Pairs where either side is not a hex literal (e.g. rgba()) are
// skipped: they composite with the backdrop and are out of scope.
func assertContrast(t *testing.T, theme, fgName, fgVal, bgName, bgVal string, minRatio float64) {
	t.Helper()
	fg, ok := parseHexColor(fgVal)
	if !ok {
		t.Skipf("%s: %s is %q, not a hex literal — alpha compositing out of scope", theme, fgName, fgVal)
	}
	bg, ok := parseHexColor(bgVal)
	if !ok {
		t.Skipf("%s: %s is %q, not a hex literal — alpha compositing out of scope", theme, bgName, bgVal)
	}
	ratio := contrastRatio(fg, bg)
	if ratio < minRatio {
		t.Errorf("theme %s: %s (%s) on %s (%s) has contrast %.2f:1, want >= %.1f:1",
			theme, fgName, fgVal, bgName, bgVal, ratio, minRatio)
	}
}

// TestTokenContrastWCAG verifies that the text-bearing design tokens of both
// themes in static/tokens.css satisfy WCAG 2.x AA (and AAA for the primary
// text pair) against the surfaces they are documented to sit on (FR-064):
//
//	(a) --tobby-text        >= 7:1 on --tobby-bg, >= 4.5:1 on --tobby-bg-raise
//	(b) --tobby-*-text      >= 4.5:1 on --tobby-bg and --tobby-bg-raise
//	(c) --tobby-muted       >= 4.5:1 on --tobby-bg
//	(d) badge ink #0b0d12   >= 4.5:1 on --tobby-accent and --tobby-success
func TestTokenContrastWCAG(t *testing.T) {
	raw, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatalf("read %s: %v", stylesheetPath, err)
	}
	css := string(raw)

	themes := []struct {
		name     string
		selector string
	}{
		{name: "dark", selector: ":root {"},
		{name: "light", selector: `:root[data-theme="light"]`},
	}

	pairs := []struct {
		fg, bg   string
		minRatio float64
	}{
		{fg: "--tobby-text", bg: "--tobby-bg", minRatio: ratioAAA},
		{fg: "--tobby-text", bg: "--tobby-bg-raise", minRatio: ratioAA},
		{fg: "--tobby-muted", bg: "--tobby-bg", minRatio: ratioAA},
		{fg: "--tobby-accent-text", bg: "--tobby-bg", minRatio: ratioAA},
		{fg: "--tobby-accent-text", bg: "--tobby-bg-raise", minRatio: ratioAA},
		{fg: "--tobby-success-text", bg: "--tobby-bg", minRatio: ratioAA},
		{fg: "--tobby-success-text", bg: "--tobby-bg-raise", minRatio: ratioAA},
		{fg: "--tobby-danger-text", bg: "--tobby-bg", minRatio: ratioAA},
		{fg: "--tobby-danger-text", bg: "--tobby-bg-raise", minRatio: ratioAA},
	}

	badgeFills := []string{"--tobby-accent", "--tobby-success"}

	for _, theme := range themes {
		t.Run(theme.name, func(t *testing.T) {
			tokens := themeTokens(t, css, theme.selector)
			for _, pair := range pairs {
				t.Run(pair.fg+"_on_"+pair.bg, func(t *testing.T) {
					assertContrast(t, theme.name,
						pair.fg, tokenValue(t, tokens, pair.fg),
						pair.bg, tokenValue(t, tokens, pair.bg),
						pair.minRatio)
				})
			}
			for _, fill := range badgeFills {
				t.Run("badge-ink_on_"+fill, func(t *testing.T) {
					assertContrast(t, theme.name,
						"badge ink", badgeInk,
						fill, tokenValue(t, tokens, fill),
						ratioAA)
				})
			}
		})
	}
}
