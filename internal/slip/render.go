// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package slip

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"unicode/utf8"
)

// Content types of the two renderings.
const (
	ContentTypeHTML = "text/html; charset=utf-8"
	ContentTypeText = "text/plain; charset=utf-8"
)

func (*kvBlock) Kind() string    { return "kv" }
func (*tableBlock) Kind() string { return "table" }
func (*paraBlock) Kind() string  { return "para" }
func (*listBlock) Kind() string  { return "list" }
func (*section) Kind() string    { return "section" }

// css is the whole stylesheet of the autonomous HTML. It is inline on
// purpose: the document is handed to people who print it, mail it inside a
// zone or archive it, and it must read the same anywhere, with no network
// and no companion file. It loads nothing — no font, no image, no script.
//
// The print rules are the point of the document: A4, black on white, no
// colour needed (every tone is also a word), tables that do not split a
// row across pages, and the two notices kept whole.
const css = `
:root{color-scheme:light}
*{box-sizing:border-box}
body{margin:0;padding:24px;background:#fff;color:#111;font:14px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif}
main{max-width:1000px;margin:0 auto}
h1{font-size:26px;margin:0 0 4px}
h2{font-size:18px;margin:28px 0 8px;padding-bottom:4px;border-bottom:2px solid #111}
h3{font-size:15px;margin:18px 0 6px}
.sub{margin:0;color:#444}
.gen{margin:2px 0 16px;color:#444;font-size:12px}
.notice{border:3px solid #111;background:#fff7d6;padding:10px 14px;margin:12px 0;break-inside:avoid;page-break-inside:avoid}
.notice strong{display:block;font-size:16px;text-transform:uppercase;letter-spacing:.02em}
.notice p{margin:6px 0 0}
dl{display:grid;grid-template-columns:minmax(180px,max-content) 1fr;gap:4px 16px;margin:8px 0}
dt{font-weight:600}
dd{margin:0;overflow-wrap:anywhere}
table{border-collapse:collapse;width:100%;margin:6px 0 12px;font-size:12.5px}
caption{text-align:left;font-weight:600;padding:6px 0 4px}
th,td{border:1px solid #888;padding:3px 6px;text-align:left;vertical-align:top;overflow-wrap:anywhere}
th{background:#eee}
tr{break-inside:avoid;page-break-inside:avoid}
ul{margin:6px 0 10px;padding-left:20px}
p{margin:6px 0}
.ok{color:#14622b;font-weight:600}
.warn{color:#8a5200;font-weight:600}
.bad{color:#a31515;font-weight:700}
.mute{color:#555}
footer{margin-top:28px}
@page{size:A4;margin:14mm}
@media print{
body{padding:0;font-size:11.5px}
h2{break-after:avoid;page-break-after:avoid}
h3{break-after:avoid;page-break-after:avoid}
.ok,.warn,.bad{color:#000}
}
`

// cssHash is the CSP source expression of the stylesheet. The document
// states its own policy (a <meta> tag) and the API/UI responses send the
// same one as a header, so an inline stylesheet is the ONLY thing that may
// load — nothing external can, even by a later edit of the template.
var cssHash = func() string {
	sum := sha256.Sum256([]byte(css))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// CSP is the Content-Security-Policy of the HTML rendering: no source of
// any kind except its own inline stylesheet.
func CSP() string {
	return "default-src 'none'; style-src " + cssHash + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

var htmlTmpl = template.Must(template.New("slip").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="{{.CSP}}">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<style>{{.CSS}}</style>
</head>
<body>
<main>
<header>
<h1>{{.Title}}</h1>
<p class="sub">{{.Subtitle}}</p>
<p class="gen">{{.Generated}}</p>
<div class="notice" role="note" id="notice-top"><strong>{{.NoticeTitle}}</strong><p>{{.NoticeBody}}</p></div>
</header>
{{range .Sections}}{{template "section" .}}{{end}}
<footer>
<div class="notice" role="note" id="notice-bottom"><strong>{{.NoticeTitle}}</strong><p>{{.NoticeBody}}</p></div>
<p class="gen">{{.Footer}}</p>
</footer>
</main>
</body>
</html>
{{define "section"}}<section>
{{if eq .Level 3}}<h3>{{.Title}}</h3>{{else}}<h2>{{.Title}}</h2>{{end}}
{{range .Blocks}}{{if eq .Kind "kv"}}<dl>{{range .Rows}}<dt>{{.Label}}</dt><dd{{if .Val.Tone}} class="{{.Val.Tone}}"{{end}}>{{.Val.Text}}</dd>{{end}}</dl>
{{else if eq .Kind "table"}}<table>{{if .Caption}}<caption>{{.Caption}}</caption>{{end}}<thead><tr>{{range .Head}}<th scope="col">{{.}}</th>{{end}}</tr></thead><tbody>
{{range .Rows}}<tr>{{range .}}<td{{if .Tone}} class="{{.Tone}}"{{end}}>{{.Text}}</td>{{end}}</tr>
{{end}}</tbody></table>
{{else if eq .Kind "para"}}<p{{if .Tone}} class="{{.Tone}}"{{end}}>{{.Text}}</p>
{{else if eq .Kind "list"}}<ul>{{range .Items}}<li{{if .Tone}} class="{{.Tone}}"{{end}}>{{.Text}}</li>{{end}}</ul>
{{else if eq .Kind "section"}}{{template "section" .}}
{{end}}{{end}}</section>
{{end}}`))

// HTML renders the slip as a self-contained HTML document: the stylesheet
// inline, no external resource of any kind, print rules included.
func (s *Slip) HTML(lang string) ([]byte, error) {
	d := s.view(lang)
	var buf bytes.Buffer
	err := htmlTmpl.Execute(&buf, struct {
		*doc
		CSS template.CSS
		CSP string
	}{doc: d, CSS: template.CSS(css), CSP: CSP()}) //nolint:gosec // G203: css is a package constant, never input
	if err != nil {
		return nil, fmt.Errorf("slip: rendering HTML: %w", err)
	}
	return buf.Bytes(), nil
}

// textWidth is the column the plain-text rendering wraps at.
const textWidth = 78

// Text renders the slip as plain text, from the same view as the HTML.
func (s *Slip) Text(lang string) []byte {
	d := s.view(lang)
	var b strings.Builder
	rule := strings.Repeat("=", textWidth)

	fmt.Fprintf(&b, "%s\n%s\n%s\n%s\n\n", rule, strings.ToUpper(d.Title), d.Subtitle, d.Generated)
	writeNotice(&b, d)
	for _, sec := range d.Sections {
		writeSection(&b, sec)
	}
	b.WriteString("\n")
	writeNotice(&b, d)
	fmt.Fprintf(&b, "%s\n%s\n", d.Footer, rule)
	return []byte(b.String())
}

func writeNotice(b *strings.Builder, d *doc) {
	bar := strings.Repeat("*", textWidth)
	b.WriteString(bar + "\n")
	b.WriteString(strings.ToUpper(d.NoticeTitle) + "\n")
	for _, ln := range wrap(d.NoticeBody, textWidth) {
		b.WriteString(ln + "\n")
	}
	b.WriteString(bar + "\n")
}

func writeSection(b *strings.Builder, sec *section) {
	b.WriteString("\n")
	if sec.Level == 3 {
		fmt.Fprintf(b, "%s\n", sec.Title)
		b.WriteString(strings.Repeat("~", min(utf8.RuneCountInString(sec.Title), textWidth)) + "\n")
	} else {
		fmt.Fprintf(b, "%s\n", strings.ToUpper(sec.Title))
		b.WriteString(strings.Repeat("-", min(utf8.RuneCountInString(sec.Title), textWidth)) + "\n")
	}
	for _, blk := range sec.Blocks {
		switch x := blk.(type) {
		case *kvBlock:
			width := 0
			for _, r := range x.Rows {
				width = max(width, utf8.RuneCountInString(r.Label))
			}
			width = min(width, 44)
			for _, r := range x.Rows {
				label := pad(r.Label, width) + " : "
				writeHanging(b, label, r.Val.Text)
			}
			b.WriteString("\n")
		case *paraBlock:
			for _, ln := range wrap(x.Text, textWidth) {
				b.WriteString(ln + "\n")
			}
			b.WriteString("\n")
		case *listBlock:
			for _, it := range x.Items {
				writeHanging(b, "- ", it.Text)
			}
			b.WriteString("\n")
		case *tableBlock:
			writeTable(b, x)
		case *section:
			writeSection(b, x)
		}
	}
}

// writeHanging writes text after a prefix, wrapping under the prefix.
func writeHanging(b *strings.Builder, prefix, text string) {
	indent := strings.Repeat(" ", utf8.RuneCountInString(prefix))
	lines := wrap(text, max(textWidth-utf8.RuneCountInString(prefix), 20))
	if len(lines) == 0 {
		lines = []string{""}
	}
	for i, ln := range lines {
		if i == 0 {
			b.WriteString(strings.TrimRight(prefix+ln, " ") + "\n")
		} else {
			b.WriteString(indent + ln + "\n")
		}
	}
}

// writeTable renders a table as aligned columns when they fit the width,
// and as one record per row when they do not — a half-wrapped table is
// worse than a list.
func writeTable(b *strings.Builder, t *tableBlock) {
	if t.Caption != "" {
		b.WriteString(t.Caption + ":\n")
	}
	defer b.WriteString("\n")
	widths := make([]int, len(t.Head))
	for i, h := range t.Head {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range t.Rows {
		for i := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], utf8.RuneCountInString(r[i].Text))
			}
		}
	}
	total := 2 * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	if total <= textWidth {
		line := func(cells []string) {
			parts := make([]string, len(widths))
			for i := range widths {
				v := ""
				if i < len(cells) {
					v = cells[i]
				}
				parts[i] = pad(v, widths[i])
			}
			b.WriteString(strings.TrimRight(strings.Join(parts, "  "), " ") + "\n")
		}
		line(t.Head)
		rule := make([]string, len(widths))
		for i, w := range widths {
			rule[i] = strings.Repeat("-", w)
		}
		line(rule)
		for _, r := range t.Rows {
			cells := make([]string, len(r))
			for i := range r {
				cells[i] = r[i].Text
			}
			line(cells)
		}
		return
	}
	for n, r := range t.Rows {
		if n > 0 {
			b.WriteString("\n")
		}
		for i := range r {
			switch {
			case i == 0:
				writeHanging(b, "* ", r[i].Text)
			case i >= len(t.Head) || r[i].Text == "":
			default:
				writeHanging(b, "    "+t.Head[i]+" : ", r[i].Text)
			}
		}
	}
}

func pad(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// wrap breaks text at word boundaries into lines of at most width runes
// (a word longer than the width stays whole on its own line).
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
