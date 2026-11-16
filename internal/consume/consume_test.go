// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package consume

import "testing"

func TestParseFormat(t *testing.T) {
	for _, f := range Formats() {
		got, err := ParseFormat(string(f))
		if err != nil || got != f {
			t.Errorf("ParseFormat(%q) = %q, %v", f, got, err)
		}
		if f.MediaType() == "" {
			t.Errorf("%s has no media type", f)
		}
	}
	if _, err := ParseFormat("xlsx"); err == nil {
		t.Error("an unknown format was accepted")
	}
	if FormatCSV.MediaType() == FormatJSON.MediaType() || FormatMarkdown.MediaType() == FormatJSON.MediaType() {
		t.Error("two formats share a media type")
	}
}
