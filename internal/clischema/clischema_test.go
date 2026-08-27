// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package clischema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/clischema"
)

// TestDocumentIsAUsableSchema: the document is embedded, so a syntax
// error in it is a defect that ships. It is the contract of every
// `--output json` report (FR-066, R-08) and it is served to callers, so
// it has to parse and to declare the draft it is written against — a
// consumer picks its validator from $schema.
func TestDocumentIsAUsableSchema(t *testing.T) {
	raw := clischema.Document()
	if len(raw) == 0 {
		t.Fatal("the schema document is empty")
	}
	var doc struct {
		Schema string                     `json:"$schema"`
		ID     string                     `json:"$id"`
		Defs   map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the embedded schema is not JSON: %v", err)
	}
	if !strings.Contains(doc.Schema, "json-schema.org") {
		t.Errorf("$schema = %q, want a JSON Schema dialect", doc.Schema)
	}
	if doc.ID == "" {
		t.Error("the document has no $id: a schema nobody can reference by URL is hard to consume")
	}
	// One entry per reporting command, keyed by command path. The
	// exhaustive check — that the set matches the command tree and that
	// every report validates — lives beside the commands themselves, in
	// internal/cli: this package holds the bytes, not the knowledge of
	// what a command is.
	if len(doc.Defs) == 0 {
		t.Fatal("the schema declares no $defs")
	}
	for _, want := range []string{"tobby version", "tobby sync"} {
		if _, ok := doc.Defs[want]; !ok {
			t.Errorf("$defs has no entry for %q", want)
		}
	}
}

// TestMediaTypeIsAnnounced: the document is served over HTTP beside the
// OpenAPI one, and a schema served as text/plain is a schema tooling will
// not pick up.
func TestMediaTypeIsAnnounced(t *testing.T) {
	if !strings.HasPrefix(clischema.MediaType, "application/schema+json") {
		t.Errorf("MediaType = %q, want application/schema+json", clischema.MediaType)
	}
}
