// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package clischema ships the JSON Schema of the documents the command
// line writes under `--output json` (FR-066, amendment 2026-08-11 /
// R-08).
//
// R-08 asks for the schemas to be documented "alongside the OpenAPI
// document" (FR-060), so they travel the same way: embedded in the binary
// (NFR-003), served by a running instance, and readable offline. They
// live in a package of their own rather than beside either consumer
// because both need them and neither may import the other — the api
// package serves the document, the cli package validates its own reports
// against it in test, and cli already imports api.
//
// The document is the contract, not a description of one: a report that
// stops validating against it is a breaking change to every automation
// that reads it, and TestEveryReportedDocumentMatchesItsSchema fails
// before it ships.
package clischema

import _ "embed"

//go:embed cli-output.schema.json
var document []byte

// MediaType is what the schema is served as. RFC 9485 registers
// application/schema+json for JSON Schema documents; the charset is
// stated because the document carries non-ASCII prose.
const MediaType = "application/schema+json; charset=utf-8"

// Document returns the raw embedded JSON Schema.
func Document() []byte { return document }
