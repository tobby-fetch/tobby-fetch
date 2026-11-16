// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import "testing"

func TestKnownMembers(t *testing.T) {
	for _, m := range Members() {
		if !Known(m) {
			t.Errorf("%s is not known", m)
		}
	}
	if !Known("tasks/0193c1.json") {
		t.Error("a task report under tasks/ is not known")
	}
	for _, name := range []string{"secrets.txt", "tasks", "../config.yaml", "tasks/../config.yaml", "/version.json"} {
		if Known(name) {
			t.Errorf("%q is known", name)
		}
	}
}

func TestManifestValidate(t *testing.T) {
	ok := Manifest{Format: FormatVersion, Members: []Member{{Name: MemberVersion}, {Name: "tasks/a.json"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Manifest{
		"format":    {Format: 99},
		"unknown":   {Format: FormatVersion, Members: []Member{{Name: "id_rsa"}}},
		"directory": {Format: FormatVersion, Members: []Member{{Name: MemberTasks}}},
		"twice":     {Format: FormatVersion, Members: []Member{{Name: MemberLogs}, {Name: MemberLogs}}},
	} {
		if m.Validate() == nil {
			t.Errorf("%s: a malformed manifest validates", name)
		}
	}
}
