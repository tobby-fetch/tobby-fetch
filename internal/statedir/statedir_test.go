// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"strings"
	"testing"
)

// TestShippedLayoutIsConsistent keeps the published layout table sound:
// the table is the backup contract of FR-097, so a malformed row is a
// documentation bug that would lose data on a rebuild.
func TestShippedLayoutIsConsistent(t *testing.T) {
	if err := Check(Entries()); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(LayoutFile); !ok {
		t.Errorf("the layout file %s is not itself documented", LayoutFile)
	}
	for _, name := range []string{"accounts.yaml", "tls"} {
		e, ok := Lookup(name)
		if !ok || !e.Secret || e.Class != ClassIdentity {
			t.Errorf("%s must be a secret identity entry, got %+v (found %v)", name, e, ok)
		}
	}
	if _, ok := Lookup("no-such-entry"); ok {
		t.Error("Lookup found an entry that is not documented")
	}
	// Entries hands out a copy: the contract cannot be edited at run time.
	got := Entries()
	got[0].Name = "mutated"
	if Entries()[0].Name == "mutated" {
		t.Error("Entries exposes the layout table itself")
	}
}

// TestCheckRefusesMalformedTables covers each refusal of Check.
func TestCheckRefusesMalformedTables(t *testing.T) {
	ok := Entry{Name: "a", Kind: KindFile, Class: ClassIdentity, Purpose: "p"}
	cases := map[string]struct {
		list []Entry
		want string
	}{
		"no name":          {[]Entry{{Kind: KindFile, Class: ClassIdentity, Purpose: "p"}}, "no name"},
		"duplicate":        {[]Entry{ok, ok}, "twice"},
		"class":            {[]Entry{{Name: "b", Kind: KindFile, Class: "cache", Purpose: "p"}}, "unknown class"},
		"kind":             {[]Entry{{Name: "b", Kind: "socket", Class: ClassIdentity, Purpose: "p"}}, "unknown kind"},
		"secret discarded": {[]Entry{{Name: "b", Kind: KindFile, Class: ClassDiscardable, Secret: true, Purpose: "p"}}, "secret but discardable"},
		"no purpose":       {[]Entry{{Name: "b", Kind: KindFile, Class: ClassIdentity}}, "no documented purpose"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := Check(c.list)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Check = %v, want an error containing %q", err, c.want)
			}
		})
	}
}
