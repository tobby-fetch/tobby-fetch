// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Request bodies at the API boundary (NFR-023, amendment 2026-11-16).
//
// Two bounds apply. maxAPIRequestBody caps every request of the surface
// before routing — a declared Content-Length over it is refused without a
// byte being read, an undeclared one is cut by http.MaxBytesReader — so no
// endpoint, present or future, can be made to buffer an unbounded body.
// Each endpoint that decodes a document then applies its own, tighter
// bound sized to its largest legitimate document, through decodeJSON.
// Both refusals answer TBY-SRV-004 (413) with the bound in bytes.
//
// decodeJSON is also where the documents are decoded STRICTLY: an unknown
// member is refused rather than silently ignored — a misspelt "prune" or
// "skip_destination" must not run the operation with its default — and so
// is anything after the JSON value, which a lenient decoder would never
// read.

// maxAPIRequestBody is the bound of every /api/v1 request body: the
// largest legitimate one is a recipe publication (cookbook document,
// 4 MiB, JSON-escaped) or a certificate replacement (two PEM documents),
// both well under it. The same value as the UI's bound (maxUIRequestBody)
// so the two surfaces refuse the same bodies (FR-061).
const maxAPIRequestBody = 16 << 20

// boundBodies caps every request body of the surface at maxAPIRequestBody.
func boundBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxAPIRequestBody {
			// Declared too large: refused before authentication even
			// reads a header twice, and before any byte of the body is.
			taxonomy.WriteProblem(w, lang(r), tooLarge(maxAPIRequestBody))
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, maxAPIRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// tooLarge is the TBY-SRV-004 refusal of a body over limit bytes.
func tooLarge(limit int64) *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeRequestTooLarge,
		taxonomy.Params{"limit": strconv.FormatInt(limit, 10)})
}

// errTrailingData is a body with something after its JSON document.
var errTrailingData = errors.New("unexpected data after the JSON document")

// decodeJSON decodes the request body into v, bounded by limit bytes,
// strictly: unknown members and trailing data are refused. On failure it
// answers the request itself and returns false — TBY-SRV-004 for a body
// over the bound, or the endpoint's own refusal, built by invalid from the
// decoding error, for anything else. emptyOK accepts an absent body (the
// endpoint's documented default), leaving v untouched.
func (a *API) decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any,
	emptyOK bool, invalid func(error) *taxonomy.Error) bool {
	if r.ContentLength > limit {
		a.Problem(w, r, tooLarge(limit))
		return false
	}
	if r.Body == nil || r.Body == http.NoBody {
		if emptyOK {
			return true
		}
		a.Problem(w, r, invalid(io.EOF))
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		// One document, nothing after it: a second value or stray bytes
		// would be read by no one, and a body is what was sent, in full.
		if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
			err = errTrailingData
			if errors.As(terr, new(*http.MaxBytesError)) {
				err = terr
			}
		}
	}
	var mbe *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &mbe):
		a.Problem(w, r, tooLarge(mbe.Limit))
	case errors.Is(err, io.EOF) && emptyOK:
		return true
	default:
		a.Problem(w, r, invalid(err))
	}
	return false
}

// bodyInvalid is the generic refusal of an undecodable body: the
// validation code, naming the body and what was wrong with it. The
// decoder's message describes the client's own input (an unknown member,
// a type mismatch, a syntax error at an offset), never server state.
func bodyInvalid(err error) *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
		"file": "the request body", "path": "-", "constraint": describeDecodeError(err),
	})
}

// describeDecodeError turns a decoding error into the sentence a client
// can act on.
func describeDecodeError(err error) string {
	if errors.Is(err, io.EOF) {
		return "the body is empty: a JSON object is required"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "the JSON document is truncated"
	}
	return fmt.Sprintf("the body must be one JSON object of the documented members: %v", err)
}
