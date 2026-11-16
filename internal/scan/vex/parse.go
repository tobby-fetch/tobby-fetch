// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package vex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The bounds of one document (NFR-023). A site's VEX document lists the
// statements of a few images; these bounds are orders of magnitude above
// that and orders of magnitude below what would let a document exhaust
// the instance's memory at startup.
const (
	// MaxDocumentSize caps the bytes read, checked before decoding.
	MaxDocumentSize = 4 << 20
	// MaxDepth caps the JSON nesting depth; the specification's deepest
	// path (statements → products → subcomponents → identifiers) is 8.
	MaxDepth = 16
	// MaxStatements caps the statements of one document.
	MaxStatements = 10000
	// MaxProducts caps the products of one document, subcomponents
	// included.
	MaxProducts = 100000
	// MaxAliases caps the aliases of one vulnerability.
	MaxAliases = 32
	// maxIdentifier caps a vulnerability name or alias.
	maxIdentifier = 256
)

// knownKeys is every member name the specification defines, at any level.
// The token walk refuses any other name — exactly, case included — before
// the typed decoding places each one (DisallowUnknownFields).
var knownKeys = map[string]bool{
	// Document.
	"@context": true, "@id": true, "author": true, "role": true, "timestamp": true,
	"last_updated": true, "version": true, "tooling": true, "statements": true,
	// Statement.
	"vulnerability": true, "products": true, "status": true, "supplier": true,
	"status_notes": true, "justification": true, "impact_statement": true,
	"action_statement": true, "action_statement_timestamp": true,
	// Vulnerability.
	"name": true, "description": true, "aliases": true,
	// Component.
	"identifiers": true, "hashes": true, "subcomponents": true,
	"purl": true, "cpe22": true, "cpe23": true,
	"md5": true, "sha1": true, "sha-256": true, "sha-384": true, "sha-512": true,
	"sha3-224": true, "sha3-256": true, "sha3-384": true, "sha3-512": true,
	"blake2s-256": true, "blake2b-256": true, "blake2b-512": true,
}

// ReadFile reads and validates the local document at path. The file must
// be a regular file; at most MaxDocumentSize bytes are read.
func ReadFile(path string) (*Document, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the operator-declared scan.vex document is the feature
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxDocumentSize+1))
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse validates and decodes one OpenVEX v0.2.0 document.
func Parse(data []byte) (*Document, error) {
	if len(data) > MaxDocumentSize {
		return nil, fmt.Errorf("the document is larger than %d bytes", MaxDocumentSize)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the document is not valid UTF-8")
	}
	if err := walk(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the document does not have the OpenVEX v0.2.0 shape: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the document")
	}
	if err := d.validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// walk checks the token stream before any typed decoding: one top-level
// object, the depth bound, member names the specification defines (exact
// spelling) and no duplicate member within an object.
func walk(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []*frame
	first := true
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("the document is not well-formed JSON: %w", err)
		}
		if first {
			if d, ok := tok.(json.Delim); !ok || d != '{' {
				return errors.New("the document is not a JSON object")
			}
			first = false
		} else if len(stack) == 0 {
			return errors.New("trailing data after the document")
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				if len(stack) > 0 {
					markValue(stack[len(stack)-1])
				}
				continue
			}
			key, ok := tok.(string)
			if !ok {
				return errors.New("the document is not well-formed JSON")
			}
			if !knownKeys[key] {
				return fmt.Errorf("unknown field %q: OpenVEX v0.2.0 does not define it", key)
			}
			if top.keys[key] {
				return fmt.Errorf("duplicate field %q", key)
			}
			top.keys[key] = true
			top.wantKey = false
			continue
		}
		switch d, _ := tok.(json.Delim); d {
		case '{', '[':
			if len(stack) >= MaxDepth {
				return fmt.Errorf("the document nests deeper than %d levels", MaxDepth)
			}
			stack = append(stack, &frame{object: d == '{', keys: map[string]bool{}, wantKey: d == '{'})
		case ']':
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				markValue(stack[len(stack)-1])
			}
		default:
			if top != nil {
				markValue(top)
			}
		}
	}
	if first {
		return errors.New("the document is empty")
	}
	return nil
}

// frame is one open object or array of the token walk.
type frame struct {
	object bool
	keys   map[string]bool
	// wantKey is true when the next token of an object is a member name
	// (or its end), false while its value is awaited.
	wantKey bool
}

// markValue records that an object member received its value.
func markValue(f *frame) {
	if f.object {
		f.wantKey = true
	}
}

// validate applies the rules of the specification the typed shape cannot
// express, and Tobby's bounds.
func (d *Document) validate() error {
	if d.Context != Context {
		return fmt.Errorf("@context %q: this instance reads OpenVEX documents of context %s", d.Context, Context)
	}
	if strings.TrimSpace(d.ID) == "" {
		return errors.New("@id is required")
	}
	if strings.TrimSpace(d.Author) == "" {
		return errors.New("author is required")
	}
	if d.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	if d.Version < 1 {
		return errors.New("version is required and starts at 1")
	}
	if len(d.Statements) > MaxStatements {
		return fmt.Errorf("the document carries more than %d statements", MaxStatements)
	}
	products := 0
	for i := range d.Statements {
		s := &d.Statements[i]
		if err := s.validate(); err != nil {
			return fmt.Errorf("statements[%d]: %w", i, err)
		}
		for j := range s.Products {
			products += 1 + len(s.Products[j].Subcomponents)
		}
		if products > MaxProducts {
			return fmt.Errorf("the document names more than %d products", MaxProducts)
		}
	}
	return nil
}

func (s *Statement) validate() error {
	if err := identifier("vulnerability.name", s.Vulnerability.Name); err != nil {
		return err
	}
	if len(s.Vulnerability.Aliases) > MaxAliases {
		return fmt.Errorf("vulnerability.aliases: more than %d", MaxAliases)
	}
	for i, a := range s.Vulnerability.Aliases {
		if err := identifier(fmt.Sprintf("vulnerability.aliases[%d]", i), a); err != nil {
			return err
		}
	}
	if !s.Status.Valid() {
		return fmt.Errorf("status %q: valid values are not_affected, affected, fixed and under_investigation", s.Status)
	}
	if len(s.Products) == 0 {
		return errors.New("products is required: a statement about no product is about every product")
	}
	for i := range s.Products {
		p := &s.Products[i]
		if p.ID == "" && (p.Identifiers == nil || *p.Identifiers == (Identifiers{})) {
			return fmt.Errorf("products[%d]: an @id or an identifier is required", i)
		}
		for j := range p.Subcomponents {
			c := &p.Subcomponents[j]
			if c.ID == "" && (c.Identifiers == nil || *c.Identifiers == (Identifiers{})) {
				return fmt.Errorf("products[%d].subcomponents[%d]: an @id or an identifier is required", i, j)
			}
		}
	}
	if s.Justification != "" {
		if !s.Justification.Valid() {
			return fmt.Errorf("justification %q: valid values are %s, %s, %s, %s and %s", s.Justification,
				ComponentNotPresent, VulnerableCodeNotPresent, VulnerableCodeNotInExecutePath,
				VulnerableCodeCannotBeControlledByAdversary, InlineMitigationsAlreadyExist)
		}
		if s.Status != StatusNotAffected {
			return fmt.Errorf("justification is only defined for status not_affected, not %s", s.Status)
		}
	}
	switch s.Status {
	case StatusNotAffected:
		if s.Justification == "" && strings.TrimSpace(s.ImpactStatement) == "" {
			return errors.New("a not_affected statement requires a justification or an impact_statement")
		}
	case StatusAffected:
		if strings.TrimSpace(s.ActionStatement) == "" {
			return errors.New("an affected statement requires an action_statement")
		}
	}
	return nil
}

// identifier checks a vulnerability identifier: present, bounded, and
// printable without spaces — it is compared exactly with what the scanner
// reports and shown in every report.
func identifier(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(v) > maxIdentifier {
		return fmt.Errorf("%s is longer than %d bytes", field, maxIdentifier)
	}
	for _, r := range v {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return fmt.Errorf("%s %q carries a space or a control character", field, v)
		}
	}
	return nil
}
