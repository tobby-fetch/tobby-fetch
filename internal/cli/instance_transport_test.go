// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// TestTokenNeverCrossesTheNetworkInClear: the CLI sends its API token to
// an http:// instance only on this host (loopback); to another host it
// needs https://, or the explicit --allow-plaintext-token opt-in, stated
// on stderr (FR-075, B-052). Without a token there is nothing to protect.
func TestTokenNeverCrossesTheNetworkInClear(t *testing.T) {
	cfg := config.Default()
	for _, c := range []struct {
		url, token string
		optIn      bool
		ok         bool
		warn       bool
	}{
		{"http://tobby.example:8080", "tby_secret", false, false, false},
		{"http://192.0.2.10:8080", "tby_secret", false, false, false},
		{"http://[2001:db8::1]:8080", "tby_secret", false, false, false},
		{"ftp://tobby.example", "tby_secret", false, false, false},
		{"http://tobby.example:8080", "tby_secret", true, true, true},
		{"https://tobby.example:8443", "tby_secret", false, true, false},
		{"http://localhost:8080", "tby_secret", false, true, false},
		{"http://127.0.0.1:8080", "tby_secret", false, true, false},
		{"http://[::1]:8080", "tby_secret", false, true, false},
		{"http://tobby.example:8080", "", false, true, false},
	} {
		t.Run(c.url+" token="+c.token, func(t *testing.T) {
			t.Setenv(EnvAPIToken, c.token)
			var stderr bytes.Buffer
			cmd := &cobra.Command{Use: "probe"}
			cmd.SetErr(&stderr)
			f := &instanceFlags{url: c.url, plaintext: c.optIn}
			cl, err := newInstanceClient(cmd, &cfg, f)
			if cl != nil {
				defer cl.close()
			}
			if (err == nil) != c.ok {
				t.Fatalf("err = %v; want ok=%v", err, c.ok)
			}
			if got := strings.Contains(stderr.String(), "in clear"); got != c.warn {
				t.Errorf("stderr = %q; want the plaintext warning=%v", stderr.String(), c.warn)
			}
		})
	}
}
