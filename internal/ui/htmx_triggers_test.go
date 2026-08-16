// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// fromFind matches an hx-trigger source of the form `from:find <selector>`.
var fromFind = regexp.MustCompile(`from:find\s+([^,"]+)`)

// TestNoAmbiguousFromFind guards B-011.
//
// htmx resolves `from:find <selector>` to the FIRST matching descendant and
// binds the listener there. On a filter form that reads as "listen to the
// checkboxes" but means "listen to the first checkbox": every other kind
// badge toggled visually and requested nothing. The same attribute silently
// disabled the type filter of the tasks screen, where two selects sit side
// by side.
//
// The trap is that the wrong form is indistinguishable from the right one by
// reading it, and no server-side test can see it: the handler is correct,
// the browser simply never calls it. So the rule is enforced on the source
// instead — `from:find` is allowed only for a selector the file matches
// exactly once. Anything else must listen on the form, where a descendant's
// event bubbles up and no "first match" exists to get wrong.
func TestNoAmbiguousFromFind(t *testing.T) {
	err := fs.WalkDir(templatesFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		raw, err := fs.ReadFile(templatesFS, p)
		if err != nil {
			return err
		}
		body := string(raw)
		for _, m := range fromFind.FindAllStringSubmatch(body, -1) {
			selector := strings.TrimSpace(m[1])
			if n := countCandidates(body, selector); n > 1 {
				t.Errorf("%s: hx-trigger uses `from:find %s`, but the file has %d matching "+
					"elements — htmx binds the FIRST one and the others are inert (B-011). "+
					"Listen on the form instead: `change` catches every descendant.",
					p, selector, n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// countCandidates approximates how many elements a `from:find` selector
// would match in one template. It handles the selector shapes the templates
// actually use — a bare tag name, or a tag with an attribute filter — which
// is enough to tell "one element" from "a family of them".
func countCandidates(body, selector string) int {
	if tag, attr, ok := strings.Cut(selector, "["); ok {
		attr = strings.TrimSuffix(attr, "]")
		key, want, _ := strings.Cut(attr, "=")
		want = strings.Trim(want, `'"`)
		n := 0
		for _, tagOpen := range splitTags(body, tag) {
			if strings.Contains(tagOpen, key+`="`+want+`"`) || strings.Contains(tagOpen, key+`='`+want+`'`) {
				n++
			}
		}
		return n
	}
	return len(splitTags(body, selector))
}

// splitTags returns the opening tags of one element name found in body.
func splitTags(body, tag string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(body[i:], "<"+tag)
		if j < 0 {
			return out
		}
		i += j
		end := strings.IndexByte(body[i:], '>')
		if end < 0 {
			return out
		}
		// Reject a longer element name sharing the prefix ("<selective").
		if next := body[i+1+len(tag)]; next == '>' || next == ' ' || next == '\n' || next == '\t' {
			out = append(out, body[i:i+end])
		}
		i += end
	}
}

// TestFilterFormsListenToEveryControl is the positive half of B-011: each
// filter form must have a trigger that fires for any of its controls, not
// just the first. Read together with the guard above, a form either listens
// on itself or names a control that is unique in its page.
func TestFilterFormsListenToEveryControl(t *testing.T) {
	for _, page := range []string{"templates/pages/content-list.html", "templates/pages/tasks.html"} {
		raw, err := fs.ReadFile(templatesFS, page)
		if err != nil {
			t.Fatal(err)
		}
		trigger := hxTrigger(string(raw))
		if trigger == "" {
			t.Errorf("%s: no hx-trigger on the filter form", page)
			continue
		}
		if !hasBareChange(trigger) {
			t.Errorf("%s: hx-trigger %q has no unscoped `change`, so only the control it names "+
				"can submit the filters (B-011)", page, trigger)
		}
	}
}

func hxTrigger(body string) string {
	i := strings.Index(body, `hx-trigger="`)
	if i < 0 {
		return ""
	}
	rest := body[i+len(`hx-trigger="`):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// hasBareChange reports whether the trigger list contains a `change` event
// with no `from:` scope.
func hasBareChange(trigger string) bool {
	for _, part := range strings.Split(trigger, ",") {
		part = strings.TrimSpace(part)
		if part == "change" {
			return true
		}
	}
	return false
}
