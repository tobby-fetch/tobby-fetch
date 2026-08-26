// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"fmt"
	"html"
	"path"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Inline rendering. Every branch below either emits a tag this file wrote
// or html.EscapeString of a span of source text: there is no path by which
// corpus bytes reach the browser as markup (NFR-013).

// inline renders one span of Markdown text.
func (r *renderer) inline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			// A backslash escape is the author asking for the literal
			// character — "*\<role\>*" writes <role>, it does not open a tag.
			escapeByte(&b, s[i+1])
			i += 2
		case s[i] == '`':
			i = r.codeSpan(&b, s, i)
		case s[i] == '!' && i+1 < len(s) && s[i+1] == '[':
			i = r.image(&b, s, i)
		case s[i] == '[':
			i = r.link(&b, s, i)
		case strings.HasPrefix(s[i:], "**"):
			i = r.emphasis(&b, s, i, "**", "strong")
		case s[i] == '*':
			i = r.emphasis(&b, s, i, "*", "em")
		default:
			escapeByte(&b, s[i])
			i++
		}
	}
	return b.String()
}

// escapeByte writes one source BYTE, escaping the five characters that
// would otherwise be markup.
//
// One byte, never one rune: s[i] is a byte, and string(s[i]) would read it
// as a code point — turning every continuation byte of a UTF-8 sequence
// into its own Latin-1 character, so "métriques" reaches the reader as
// "mÃ©triques". The escaping is done here rather than by html.EscapeString
// for exactly that reason: the five characters at stake are ASCII, and
// every byte of a multibyte sequence is >= 0x80, so a byte that is not one
// of them travels through untouched and the sequence stays intact.
func escapeByte(b *strings.Builder, c byte) {
	switch c {
	case '<':
		b.WriteString("&lt;")
	case '>':
		b.WriteString("&gt;")
	case '&':
		b.WriteString("&amp;")
	case '"':
		b.WriteString("&#34;")
	case '\'':
		b.WriteString("&#39;")
	default:
		b.WriteByte(c)
	}
}

// codeSpan renders a backtick span. Its content is literal text — the
// corpus writes commands, configuration keys and digests in them, and none
// of that is markup.
func (r *renderer) codeSpan(b *strings.Builder, s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	fence := s[i : i+n]
	end := strings.Index(s[i+n:], fence)
	if end < 0 {
		b.WriteString(html.EscapeString(fence))
		return i + n
	}
	fmt.Fprintf(b, "<code>%s</code>", html.EscapeString(strings.TrimSpace(s[i+n:i+n+end])))
	return i + n + end + n
}

// emphasis renders **strong** or *em*. An unmatched delimiter is written
// literally rather than swallowing the rest of the line — "2 * 3" is
// arithmetic, not markup.
func (r *renderer) emphasis(b *strings.Builder, s string, i int, delim, tag string) int {
	rest := s[i+len(delim):]
	if rest == "" || rest[0] == ' ' {
		b.WriteString(html.EscapeString(delim))
		return i + len(delim)
	}
	end := strings.Index(rest, delim)
	if end < 0 {
		b.WriteString(html.EscapeString(delim))
		return i + len(delim)
	}
	fmt.Fprintf(b, "<%s>%s</%s>", tag, r.inline(rest[:end]), tag)
	return i + len(delim) + end + len(delim)
}

// bracketed returns the text and target of a "[text](target)" construct
// starting at i (after an optional "!").
func bracketed(s string, i int) (text, target string, next int, ok bool) {
	depth := 0
	closeIdx := -1
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				closeIdx = j
			}
		}
		if closeIdx >= 0 {
			break
		}
	}
	if closeIdx < 0 || closeIdx+1 >= len(s) || s[closeIdx+1] != '(' {
		return "", "", 0, false
	}
	endIdx := strings.IndexByte(s[closeIdx+2:], ')')
	if endIdx < 0 {
		return "", "", 0, false
	}
	return s[i+1 : closeIdx], s[closeIdx+2 : closeIdx+2+endIdx], closeIdx + 2 + endIdx + 1, true
}

// image renders "![alt](target)" as a figure: the alternative text is both
// the alt attribute and the visible caption (NFR-017 — a screenshot the
// reader cannot see must still say what it shows).
func (r *renderer) image(b *strings.Builder, s string, i int) int {
	alt, target, next, ok := bracketed(s, i+1)
	if !ok {
		b.WriteString("!")
		return i + 1
	}
	b.WriteString(r.imageHTML(alt, target))
	return next
}

// figure renders a paragraph that is nothing but one image.
func (r *renderer) figure(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "![") {
		return "", false
	}
	alt, target, next, ok := bracketed(trimmed, 1)
	if !ok || next != len(trimmed) {
		return "", false
	}
	return `<figure class="t-doc-figure">` + r.imageHTML(alt, target) +
		`<figcaption>` + r.inline(alt) + `</figcaption></figure>` + "\n", true
}

// imageHTML resolves an image target against the embedded screenshots. A
// target with no embedded file is a dangling image and is reported: an
// offline guide showing a broken frame is the failure NFR-003 exists to
// prevent.
func (r *renderer) imageHTML(alt, target string) string {
	name := path.Base(target)
	if _, _, ok := r.corpus.Asset(name); !ok {
		r.fail(target, "no embedded screenshot with that name")
		return ""
	}
	return `<img class="t-doc-shot" src="` + html.EscapeString(assetPath(name)) +
		`" alt="` + html.EscapeString(alt) + `" loading="lazy">`
}

// link renders "[text](target)". Internal targets are rewritten onto the
// /help namespace and verified; external ones keep their address and are
// marked, since an isolated zone cannot follow them (the reader copies
// them onto a connected machine — website/README-docs.md).
func (r *renderer) link(b *strings.Builder, s string, i int) int {
	text, target, next, ok := bracketed(s, i)
	if !ok {
		b.WriteString("[")
		return i + 1
	}
	href, external := r.resolveLink(target)
	if href == "" {
		// Unresolved: the text survives, the dead link does not.
		b.WriteString(r.inline(text))
		return next
	}
	if external {
		fmt.Fprintf(b, `<a class="t-doc-extlink" href="%s" rel="noreferrer noopener external">%s<span aria-hidden="true"> ↗</span></a>`,
			html.EscapeString(href), r.inline(text))
		return next
	}
	fmt.Fprintf(b, `<a href="%s">%s</a>`, html.EscapeString(href), r.inline(text))
	return next
}

// resolveLink maps one corpus link target onto a URL this instance serves.
// It returns "" for a target that resolves to nothing, after recording the
// issue — TestNoDanglingLink turns the whole set into a build failure.
func (r *renderer) resolveLink(target string) (href string, external bool) {
	switch {
	case target == "":
		r.fail(target, "empty link target")
		return "", false
	case strings.HasPrefix(target, "https://"), strings.HasPrefix(target, "http://"):
		return target, true
	case strings.HasPrefix(target, "#"):
		return target, false
	}
	// Relative links are written against the page's website URL,
	// "/docs/<section>/<slug>/" (website/README-docs.md). Resolving them
	// against that base — and never against a filesystem path — is what
	// makes "../" harmless here (NFR-011): the result is looked up in a
	// fixed key set, it never opens a file.
	link, frag, _ := strings.Cut(target, "#")
	resolved := path.Join("/docs/"+r.page.Key+"/", link)
	if rest, ok := strings.CutPrefix(resolved, "/assets/docs/"); ok {
		if _, _, found := r.corpus.Asset(rest); !found {
			r.fail(target, "no embedded screenshot with that name")
			return "", false
		}
		return assetPath(rest), false
	}
	key, ok := strings.CutPrefix(resolved, "/docs/")
	if !ok {
		r.fail(target, "target escapes the documentation tree")
		return "", false
	}
	// The error reference is not embedded: its content is the taxonomy
	// catalog this binary already carries, and /help renders it live
	// (FR-065). Its per-code anchors are the codes themselves.
	if key == "reference/errors" {
		if frag == "" {
			return "/help", false
		}
		code := taxonomy.Code(strings.ToUpper(frag))
		if _, known := taxonomy.Lookup(code); !known {
			r.fail(target, "no such error code in the taxonomy catalog")
			return "", false
		}
		return "/help#" + string(code), false
	}
	if _, _, found := r.corpus.Lookup(r.lang, key); !found {
		r.fail(target, "no such page in the embedded corpus")
		return "", false
	}
	if frag != "" {
		// The target page's headings are not known yet — a page links
		// forward as often as backward — so the anchor is recorded and
		// checked once the whole corpus is rendered (Corpus.CheckLinks).
		r.frags = append(r.frags, fragRef{
			lang: r.lang, fromKey: r.page.Key, targetKey: key, frag: frag, raw: target,
		})
		return pagePath(key) + "#" + frag, false
	}
	return pagePath(key), false
}
