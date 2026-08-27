// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
)

// The corpus carries its diagrams as inline SVG — the project's visual
// language (website/README-docs.md). They are the one construct that must
// reach the browser as markup, so they are the one construct that gets a
// verifier.
//
// Nothing here copies source bytes into the output. The block is parsed
// into XML tokens, every element and attribute is checked against an
// allowlist, and the result is written back from the parsed tree by this
// file. A construct outside the allowlist — a <script>, a <foreignObject>,
// an event handler, an external reference — is not stripped and rendered
// anyway: it fails the whole block, and TestNoDanglingLink surfaces the
// failure at build time. Rendering the diagram matters less than never
// having a raw-HTML path (NFR-013).

// svgElements is the drawing vocabulary of the corpus diagrams, plus the
// accessible-name elements. Deliberately no <script>, <style>, <image>,
// <use>, <foreignObject>, <a>, <animate*>.
var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "marker": true,
	"path": true, "rect": true, "circle": true, "ellipse": true,
	"line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "title": true, "desc": true,
}

// svgAttributes is the geometry, presentation and accessibility set.
// Deliberately no href/xlink:href (no external reference) and no on*
// handler — an unknown attribute is a rejection, so both are excluded by
// simply not being listed.
var svgAttributes = map[string]bool{
	"id": true, "class": true, "role": true, "aria-label": true,
	"aria-labelledby": true, "aria-hidden": true, "xmlns": true,
	"viewBox": true, "preserveAspectRatio": true, "transform": true,
	"width": true, "height": true, "x": true, "y": true,
	"x1": true, "y1": true, "x2": true, "y2": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"d": true, "points": true, "style": true,
	"fill": true, "fill-opacity": true, "fill-rule": true,
	"stroke": true, "stroke-width": true, "stroke-dasharray": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-opacity": true,
	"opacity":     true,
	"font-family": true, "font-size": true, "font-style": true,
	"font-weight": true, "text-anchor": true, "dominant-baseline": true,
	"letter-spacing": true,
	"marker-start":   true, "marker-mid": true, "marker-end": true,
	"markerWidth": true, "markerHeight": true, "markerUnits": true,
	"refX": true, "refY": true, "orient": true,
}

// localURLRe matches the only functional value the allowlist accepts: a
// reference to another element of the same document, which is how the
// diagrams attach their arrowheads.
var localURLRe = regexp.MustCompile(`^url\(#[A-Za-z][\w-]*\)$`)

// sanitizeSVG re-serializes one inline SVG block through the allowlist.
func sanitizeSVG(src string) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(src))
	var b strings.Builder
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parsing: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && t.Name.Local != "svg" {
				return "", fmt.Errorf("block does not start with <svg>")
			}
			if !svgElements[t.Name.Local] {
				return "", fmt.Errorf("element <%s> is not allowed", t.Name.Local)
			}
			b.WriteString("<" + t.Name.Local)
			for _, a := range t.Attr {
				name := attrName(a.Name)
				if !svgAttributes[name] {
					return "", fmt.Errorf("attribute %q on <%s> is not allowed", name, t.Name.Local)
				}
				if err := checkValue(name, a.Value); err != nil {
					return "", err
				}
				b.WriteString(` ` + name + `="` + html.EscapeString(a.Value) + `"`)
			}
			b.WriteString(">")
			depth++
		case xml.EndElement:
			b.WriteString("</" + t.Name.Local + ">")
			depth--
		case xml.CharData:
			b.WriteString(html.EscapeString(string(t)))
		case xml.Comment, xml.ProcInst, xml.Directive:
			// Dropped: none of the three carries drawing information, and
			// a processing instruction is exactly the sort of thing an
			// allowlist exists to keep out.
		}
	}
	if depth != 0 {
		return "", fmt.Errorf("unbalanced markup")
	}
	return b.String(), nil
}

// attrName renders an attribute name, keeping the namespace prefix so a
// namespaced attribute cannot pass as its unprefixed twin.
func attrName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// checkValue rejects the values that would turn an allowed attribute into
// a vector: a functional reference to anything but a local element, and
// the legacy CSS execution forms.
func checkValue(name, value string) error {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "javascript:") || strings.Contains(lower, "expression(") {
		return fmt.Errorf("attribute %q carries an executable value", name)
	}
	if strings.Contains(lower, "url(") && !localURLRe.MatchString(strings.TrimSpace(value)) {
		return fmt.Errorf("attribute %q references something other than a local element", name)
	}
	return nil
}
