// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package help serves Tobby's operator documentation from inside the
// binary (NFR-003, amendment 2026-08-11 / R-05).
//
// The destination zone is air-gapped by definition, so documentation that
// lives on a website is documentation the operator who needs it most
// cannot read. The guides therefore travel with the instance: the pages of
// website/src/content/docs are copied verbatim into corpus/ by
// tools/helpsync, embedded with go:embed, and rendered server-side into
// the same shell as every other screen (ADR-0010) — no Node toolchain, no
// pre-rendered HTML, no outbound request (NFR-019).
//
// The corpus is a byte-for-byte copy of the website sources rather than a
// second edition of them: `go run ./tools/helpsync -check` fails when the
// two diverge, so there is exactly one documentation to maintain (see
// website/README-docs.md).
//
// Markdown is rendered by the subset parser in markdown.go, which builds
// its output from typed pieces and escapes every span of source text —
// there is no raw-HTML passthrough anywhere in the path (NFR-013). The
// inline SVG diagrams of the corpus are the single structured exception:
// svg.go re-serializes them from a parsed tree through an element and
// attribute allowlist, so the bytes the browser receives are produced
// here, never copied from the source.
package help
