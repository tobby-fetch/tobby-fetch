// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

// Commands reserved at the opening of milestone 7. Each track writes its
// command in its own file and adds ONE line to the AddCommand list of
// root.go (the only shared line; a merge conflict there is one line wide).
// Every command honours the R-08 contract: --output json with a published
// schema (clischema), the exit-code classes, no prompt.
//
//	Track A (7.1)   tobby consumption table [--recipe <name>] --format csv|json|markdown
//	                tobby consumption registries [--recipe <name>]       (consumption.go)
//	Track B (R-30)  tobby diagnostics bundle [-o <file>]                 (diagnostics.go)
//	Track B (R-27)  tobby state inspect                                  (statecmd.go)
//	Track C (R-15)  tobby scan exceptions                                (scancmd.go — scan.go is the serve wiring)
//
// Track C (R-17) adds no command: hardening.disableUnitImport makes
// `tobby fileset pack` refuse with TBY-POL-005 (filesetcmd.go).
