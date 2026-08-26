// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"fmt"
	"html"
	"strings"
	"unicode"
)

// The Markdown subset of the documentation corpus, rendered server-side.
//
// A general Markdown library was not taken: the corpus is written by this
// project against website/README-docs.md, so its grammar is known and
// small — frontmatter, ATX headings, paragraphs, fenced code, GFM tables,
// lists, Starlight asides, links, images, and the inline SVG diagrams of
// the visual language. Rendering it here keeps the dependency surface of
// an air-gapped binary unchanged and, more importantly, keeps the output
// under this package's control: every span of source text is escaped as it
// is written (NFR-013), and the only structured markup that survives is
// the SVG allowlist of svg.go.
//
// A construct this parser does not know does not render as raw HTML: it
// renders as nothing, and TestNoUnknownBlock fails the build. The corpus
// cannot grow a construct the guides silently drop.

// renderer holds the state of one page rendering.
type renderer struct {
	corpus *Corpus
	page   *Page
	lang   string
	labels *Labels

	headings []Heading
	// slugs counts heading slugs so a repeated title gets "-1", "-2" …,
	// the disambiguation rule of the site generator.
	slugs map[string]int
	// issues collects every link that resolved to nothing. Production
	// rendering never sees one — the corpus is checked by
	// TestNoDanglingLink — but collecting rather than panicking is what
	// lets the test report all of them at once.
	issues []LinkIssue
	// frags holds the cross-page anchor links, verified once every page
	// has been rendered and its headings are known.
	frags []fragRef
}

// LinkIssue is one link of the corpus whose target does not exist.
type LinkIssue struct {
	// Lang and Key locate the page the link was written on.
	Lang string
	Key  string
	// Target is the link as written, Reason why it does not resolve.
	Target string
	Reason string
}

func (l LinkIssue) String() string {
	return fmt.Sprintf("%s/%s: %q — %s", l.Lang, l.Key, l.Target, l.Reason)
}

func (r *renderer) fail(target, reason string) {
	r.issues = append(r.issues, LinkIssue{
		Lang: r.lang, Key: r.page.Key, Target: target, Reason: reason,
	})
}

// blocks renders lines from index i to the end of the slice.
func (r *renderer) blocks(lines []string, depth int) string {
	var b strings.Builder
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			i++
		case strings.HasPrefix(trimmed, "```"):
			i = r.fence(&b, lines, i)
		case strings.HasPrefix(trimmed, ":::"):
			i = r.aside(&b, lines, i, depth)
		case strings.HasPrefix(trimmed, "#"):
			i = r.heading(&b, lines, i)
		case strings.HasPrefix(trimmed, "|"):
			i = r.table(&b, lines, i)
		case listMarker(line) != "":
			i = r.list(&b, lines, i, depth)
		case strings.HasPrefix(trimmed, "<") || strings.HasPrefix(trimmed, "import "):
			i = r.rawBlock(&b, lines, i)
		default:
			i = r.paragraph(&b, lines, i)
		}
	}
	return b.String()
}

// heading renders an ATX heading and records its anchor. The anchor is the
// slug the site generator computes, so a fragment written for the website
// resolves here too.
func (r *renderer) heading(b *strings.Builder, lines []string, i int) int {
	line := strings.TrimSpace(lines[i])
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	text := strings.TrimSpace(line[level:])
	if level > 6 {
		level = 6
	}
	id := r.slug(plain(text))
	r.headings = append(r.headings, Heading{ID: id, Text: plain(text), Level: level})
	fmt.Fprintf(b, `<h%d id="%s">%s</h%d>`, level, html.EscapeString(id), r.inline(text), level)
	b.WriteString("\n")
	return i + 1
}

// slug builds the heading anchor: lowercase, letters/digits/hyphens,
// spaces folded to hyphens, duplicates suffixed.
func (r *renderer) slug(text string) string {
	var sb strings.Builder
	for _, ru := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(ru) || unicode.IsDigit(ru):
			sb.WriteRune(ru)
		case ru == ' ' || ru == '-' || ru == '_':
			sb.WriteRune('-')
		}
	}
	base := strings.Trim(sb.String(), "-")
	if r.slugs == nil {
		r.slugs = map[string]int{}
	}
	n := r.slugs[base]
	r.slugs[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n)
}

// fence renders a fenced code block. The body is written verbatim and
// escaped: a code sample is text, never markup.
func (r *renderer) fence(b *strings.Builder, lines []string, i int) int {
	open := strings.TrimSpace(lines[i])
	lang := strings.TrimSpace(strings.TrimPrefix(open, "```"))
	var body []string
	i++
	for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
		body = append(body, lines[i])
		i++
	}
	if i < len(lines) {
		i++ // closing fence
	}
	b.WriteString(`<pre class="t-doc-code"`)
	if lang != "" {
		fmt.Fprintf(b, ` data-lang="%s"`, html.EscapeString(lang))
	}
	b.WriteString("><code>")
	b.WriteString(html.EscapeString(strings.Join(body, "\n")))
	b.WriteString("</code></pre>\n")
	return i
}

// asideKinds maps the Starlight aside types the corpus uses onto the
// project's own callout modifiers. An unknown type renders as a plain note
// rather than an unstyled block.
var asideKinds = map[string]string{
	"note": "note", "tip": "tip", "caution": "caution", "danger": "danger",
}

// aside renders a Starlight aside, ":::note[Title]" … ":::". Its content
// is Markdown and goes back through the block parser.
func (r *renderer) aside(b *strings.Builder, lines []string, i, depth int) int {
	open := strings.TrimSpace(lines[i])
	spec := strings.TrimPrefix(open, ":::")
	kind, title := spec, ""
	if name, rest, ok := strings.Cut(spec, "["); ok {
		kind = name
		title = strings.TrimSuffix(rest, "]")
	}
	class, known := asideKinds[strings.TrimSpace(kind)]
	if !known {
		class = "note"
	}
	var body []string
	i++
	for i < len(lines) && strings.TrimSpace(lines[i]) != ":::" {
		body = append(body, lines[i])
		i++
	}
	if i < len(lines) {
		i++ // closing marker
	}
	fmt.Fprintf(b, `<aside class="t-doc-aside t-doc-aside--%s">`, class)
	if title != "" {
		fmt.Fprintf(b, `<p class="t-doc-aside-title">%s</p>`, r.inline(title))
	}
	b.WriteString(r.blocks(body, depth+1))
	b.WriteString("</aside>\n")
	return i
}

// table renders a GFM table. The corpus writes them with a leading pipe on
// every row, so a row is recognised without lookahead.
func (r *renderer) table(b *strings.Builder, lines []string, i int) int {
	var rows [][]string
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
		rows = append(rows, splitRow(lines[i]))
		i++
	}
	if len(rows) == 0 {
		return i
	}
	head := rows[0]
	body := rows[1:]
	// The alignment row ("| --- | --- |") is separator, not data.
	if len(body) > 0 && isSeparatorRow(body[0]) {
		body = body[1:]
	}
	b.WriteString(`<div class="t-doc-tablewrap"><table class="t-table t-doc-table"><thead><tr>`)
	for _, cell := range head {
		fmt.Fprintf(b, "<th>%s</th>", r.inline(cell))
	}
	b.WriteString("</tr></thead><tbody>")
	for _, row := range body {
		b.WriteString("<tr>")
		for _, cell := range row {
			fmt.Fprintf(b, "<td>%s</td>", r.inline(cell))
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table></div>\n")
	return i
}

// splitRow splits one table row on unescaped pipes.
func splitRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	var cells []string
	var cur strings.Builder
	for j := 0; j < len(s); j++ {
		switch {
		case s[j] == '\\' && j+1 < len(s) && s[j+1] == '|':
			cur.WriteByte('|')
			j++
		case s[j] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[j])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func isSeparatorRow(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return len(cells) > 0
}

// listMarker returns the bullet or number marker a line starts with, or ""
// when the line starts no list item.
func listMarker(line string) string {
	s := strings.TrimLeft(line, " ")
	switch {
	case strings.HasPrefix(s, "- "), strings.HasPrefix(s, "* "):
		return s[:2]
	}
	for j := range len(s) {
		if s[j] >= '0' && s[j] <= '9' {
			continue
		}
		if j > 0 && s[j] == '.' && j+1 < len(s) && s[j+1] == ' ' {
			return s[:j+2]
		}
		break
	}
	return ""
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

// list renders one list and its nested lists. Items are separated at the
// indentation of the first marker; anything indented deeper belongs to the
// item and is re-parsed as blocks, which is what makes nesting work
// without a second code path.
func (r *renderer) list(b *strings.Builder, lines []string, i, depth int) int {
	base := indentOf(lines[i])
	ordered := !strings.HasPrefix(strings.TrimLeft(lines[i], " "), "- ") &&
		!strings.HasPrefix(strings.TrimLeft(lines[i], " "), "* ")
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	var items [][]string
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			// A blank line ends the list unless the next non-blank line is
			// still part of it (loose list).
			if j := i + 1; j < len(lines) && strings.TrimSpace(lines[j]) != "" &&
				indentOf(lines[j]) >= base && (listMarker(lines[j]) != "" || indentOf(lines[j]) > base) {
				items[len(items)-1] = append(items[len(items)-1], "")
				i++
				continue
			}
			break
		}
		switch {
		case indentOf(line) == base && listMarker(line) != "":
			marker := listMarker(line)
			items = append(items, []string{strings.TrimLeft(line, " ")[len(marker):]})
		case len(items) > 0 && (indentOf(line) > base || listMarker(line) == ""):
			items[len(items)-1] = append(items[len(items)-1], line)
		default:
			return r.emitList(b, tag, items, depth, i)
		}
		i++
	}
	return r.emitList(b, tag, items, depth, i)
}

// emitList renders the collected items, dedented to their own margin.
func (r *renderer) emitList(b *strings.Builder, tag string, items [][]string, depth, next int) int {
	fmt.Fprintf(b, "<%s class=\"t-doc-list\">", tag)
	for _, item := range items {
		b.WriteString("<li>")
		b.WriteString(r.itemContent(item, depth))
		b.WriteString("</li>")
	}
	fmt.Fprintf(b, "</%s>\n", tag)
	return next
}

// itemContent renders one list item: its lead paragraph inline (a tight
// item must not grow a <p>), then whatever follows as blocks.
func (r *renderer) itemContent(item []string, depth int) string {
	lead := []string{strings.TrimSpace(item[0])}
	rest := item[1:]
	for len(rest) > 0 {
		line := rest[0]
		if strings.TrimSpace(line) == "" || listMarker(line) != "" ||
			strings.HasPrefix(strings.TrimSpace(line), "```") ||
			strings.HasPrefix(strings.TrimSpace(line), ":::") ||
			strings.HasPrefix(strings.TrimSpace(line), "|") {
			break
		}
		lead = append(lead, strings.TrimSpace(line))
		rest = rest[1:]
	}
	out := r.inline(strings.Join(lead, " "))
	if body := r.blocks(dedent(rest), depth+1); body != "" {
		out += body
	}
	return out
}

// dedent removes the common leading indentation of a block.
func dedent(lines []string) []string {
	margin := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if n := indentOf(line); margin < 0 || n < margin {
			margin = n
		}
	}
	if margin <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		if len(line) >= margin {
			out[i] = line[margin:]
			continue
		}
		out[i] = strings.TrimLeft(line, " ")
	}
	return out
}

// paragraph renders one paragraph: consecutive lines up to a blank line or
// the start of another block.
func (r *renderer) paragraph(b *strings.Builder, lines []string, i int) int {
	var body []string
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "```") ||
			strings.HasPrefix(trimmed, ":::") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "<") ||
			listMarker(lines[i]) != "" {
			break
		}
		body = append(body, trimmed)
		i++
	}
	if len(body) == 0 {
		return i + 1
	}
	// A paragraph made of a single image is a figure, not a line of text.
	if fig, ok := r.figure(strings.Join(body, " ")); ok {
		b.WriteString(fig)
		return i
	}
	fmt.Fprintf(b, "<p>%s</p>\n", r.inline(strings.Join(body, " ")))
	return i
}

// rawBlock handles the three non-Markdown constructs the corpus is allowed
// to carry: MDX imports (dropped — the components they name are rendered
// here, not by a bundler), HTML comments (dropped, they are authoring
// notes), and the inline SVG diagrams (allowlisted by svg.go). Anything
// else is dropped and reported by TestNoUnknownBlock: a construct the
// guides would silently swallow is a corpus defect, not a runtime one.
func (r *renderer) rawBlock(b *strings.Builder, lines []string, i int) int {
	trimmed := strings.TrimSpace(lines[i])
	switch {
	case strings.HasPrefix(trimmed, "import "):
		return i + 1
	case strings.HasPrefix(trimmed, "<!--"):
		for i < len(lines) && !strings.Contains(lines[i], "-->") {
			i++
		}
		return i + 1
	case strings.HasPrefix(trimmed, "<svg"):
		var body []string
		for i < len(lines) {
			body = append(body, lines[i])
			done := strings.Contains(lines[i], "</svg>")
			i++
			if done {
				break
			}
		}
		if out, err := sanitizeSVG(strings.Join(body, "\n")); err == nil {
			b.WriteString(out)
			b.WriteString("\n")
		} else {
			r.fail("<svg>", "rejected by the SVG allowlist: "+err.Error())
		}
		return i
	case strings.HasPrefix(trimmed, "<StatusTable"):
		b.WriteString(r.statusTable(strings.Contains(trimmed, "securityOnly")))
		return i + 1
	}
	r.fail(trimmed, "unknown block-level construct")
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
		i++
	}
	return i
}

// plain strips inline markup from a heading so the anchor slug and the
// table of contents carry text, not syntax.
func plain(s string) string {
	repl := strings.NewReplacer("`", "", "**", "", "*", "", "\\", "")
	out := repl.Replace(s)
	// Links keep their text and lose their target.
	for {
		open := strings.Index(out, "[")
		if open < 0 {
			break
		}
		mid := strings.Index(out[open:], "](")
		if mid < 0 {
			break
		}
		end := strings.Index(out[open+mid:], ")")
		if end < 0 {
			break
		}
		out = out[:open] + out[open+1:open+mid] + out[open+mid+end+1:]
	}
	return strings.TrimSpace(out)
}
