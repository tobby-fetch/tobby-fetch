// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command tobby is the Tobby binary: one static executable carrying the
// service, the embedded OCI registry, and the operator command line.
package main

import (
	"os"

	"github.com/tobby-fetch/tobby-fetch/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
