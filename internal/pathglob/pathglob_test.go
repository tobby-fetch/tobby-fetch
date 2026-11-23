// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package pathglob

import "testing"

// TestMatch locks the grammar shared by trust scopes and exception scopes.
func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"registry.example.com/cookbook/wordpress", "registry.example.com/cookbook/wordpress", true},
		{"registry.example.com/cookbook/wordpress", "registry.example.com/cookbook/mariadb", false},
		{"registry.example.com/cookbook/*", "registry.example.com/cookbook/wordpress", true},
		{"registry.example.com/cookbook/*", "registry.example.com/cookbook/a/b", false},
		{"registry.example.com/cookbook/*", "registry.example.com/cookbook", false},
		{"*", "registry.example.com", true},
		{"*", "registry.example.com/x", false},
		{"**", "a/b/c", true},
		{"registry.example.com/**", "registry.example.com/a/b/c", true},
		{"registry.example.com/**", "registry.example.com", true},
		{"**/wordpress", "a/b/wordpress", true},
		{"**/wordpress", "a/b/mariadb", false},
		{"a/*/c", "a/c", false},
		{"reg*.example.com/cook*", "docs.example.com/cookbook", false},
		{"c*ook*book", "cookandbook", true},
		{"c*ook*book", "cbook", false},
		{"docker.io/library/nginx", "docker.io/library/Nginx", false},
		{"docker.io/library/nginx", "docker.io/library/nginx/extra", false},
		{"", "", true},
		{"", "a", false},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.path); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}
