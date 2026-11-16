// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The NFR-023 acceptance over the API route table: an oversized body sent
// to every state-changing endpoint receives the bounded refusal, and
// malformed references and path parameters are refused with their code.
// It lives beside the RBAC matrix because that matrix IS the enumerated
// route table, kept complete by TestRBACMatrixCoversEveryRoute.

package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// apiBodyBound mirrors api.maxAPIRequestBody (unexported there): the
// bound every /api/v1 body is cut at.
const apiBodyBound = 16 << 20

// jsonFlood is a JSON document that never ends: an object whose one
// string member runs for n bytes. A strict decoder has to read all of it
// before it can refuse anything, so it is what reaches the size bound.
type jsonFlood struct {
	head io.Reader
	n    int64
	read int64
}

func newJSONFlood(n int64) *jsonFlood {
	return &jsonFlood{head: strings.NewReader(`{"reference":"`), n: n}
}

func (f *jsonFlood) Read(p []byte) (int, error) {
	if n, err := f.head.Read(p); n > 0 || err != io.EOF {
		f.read += int64(n)
		return n, err
	}
	if f.read >= f.n {
		return 0, io.EOF
	}
	if rest := f.n - f.read; int64(len(p)) > rest {
		p = p[:rest]
	}
	for i := range p {
		p[i] = 'a'
	}
	f.read += int64(len(p))
	return len(p), nil
}

// problemCode extracts the taxonomy code of an RFC 9457 body.
func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p.Code
}

// TestOversizedBodyOnEveryStateChangingAPIRoute: a declared body over the
// bound is refused before a byte is read; an undeclared (chunked) one is
// cut at the bound by every endpoint that reads it — at its own, tighter
// bound when it decodes a document.
func TestOversizedBodyOnEveryStateChangingAPIRoute(t *testing.T) {
	env := newRBACEnv(t)
	checked, cut := 0, 0
	for i := range apiMatrix {
		row := &apiMatrix[i]
		if auth.SafeMethod(row.Method) {
			continue
		}
		checked++

		// Declared: Content-Length over the bound.
		flood := newJSONFlood(apiBodyBound + 1)
		r := httptest.NewRequest(row.Method, row.Path, flood)
		r.ContentLength = apiBodyBound + 1
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+env.tokens[auth.RoleAdmin])
		w := httptest.NewRecorder()
		env.mux.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge || problemCode(t, w) != "TBY-SRV-004" {
			t.Errorf("%s, declared oversize: %d %s, want 413 TBY-SRV-004", row.Pattern, w.Code, w.Body.String())
		}
		if flood.read != 0 {
			t.Errorf("%s read %d bytes of a body it had to refuse unread", row.Pattern, flood.read)
		}

		// Undeclared: the same flood, chunked.
		flood = newJSONFlood(apiBodyBound + 1)
		r = httptest.NewRequest(row.Method, row.Path, flood)
		r.ContentLength = -1
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+env.tokens[auth.RoleAdmin])
		w = httptest.NewRecorder()
		env.mux.ServeHTTP(w, r)
		if w.Code == http.StatusRequestEntityTooLarge {
			cut++
		}
		if flood.read > apiBodyBound+(64<<10) {
			t.Errorf("%s consumed %d bytes, past the %d-byte bound", row.Pattern, flood.read, apiBodyBound)
		}
		// An endpoint that started reading the document must have refused
		// it with the bound, not with a syntax or validation error. (One
		// that answered before reading — a surface not configured on this
		// test instance — read nothing, which the check above covers.)
		if flood.read > 0 && (w.Code != http.StatusRequestEntityTooLarge || problemCode(t, w) != "TBY-SRV-004") {
			t.Errorf("%s, chunked oversize: %d %s, want 413 TBY-SRV-004", row.Pattern, w.Code, w.Body.String())
		}
	}
	if checked < 20 {
		t.Fatalf("only %d state-changing API routes checked", checked)
	}
	// Twelve endpoints decode a document on this test instance; the
	// others answer before reading (not configured here) or take none.
	if cut < 12 {
		t.Errorf("only %d of %d state-changing routes cut the chunked flood at a bound, want at least 12", cut, checked)
	}
}

// TestStrictAPIDocuments: an unknown member, or anything after the
// document, is refused rather than ignored — a misspelt option must not
// run the operation with its default.
func TestStrictAPIDocuments(t *testing.T) {
	env := newRBACEnv(t)
	for _, c := range []struct {
		method, path, body, code string
	}{
		{"POST", "/api/v1/tokens", `{"name":"ci","role":"viewer","rolee":"admin"}`, "TBY-VAL-001"},
		{"POST", "/api/v1/tokens", `{"name":"ci","role":"viewer"} {"name":"other"}`, "TBY-VAL-001"},
		{"POST", "/api/v1/accounts", `{"name":"x","role":"viewer","password":"p","admin":true}`, "TBY-AUTH-008"},
		{"POST", "/api/v1/import", `{"reference":"docker.io/library/alpine:3","platforms":[{"os":"linux","architecture":"amd64"}],"force":true}`, "TBY-REG-001"},
		{"POST", "/api/v1/oci-layout/export", `{"output":"/tmp/x","everything":true}`, "TBY-VAL-001"},
		{"POST", "/api/v1/account/password", `{"current":"x","new":"y","confirm":"y"}`, "TBY-VAL-001"},
		{"POST", "/api/v1/tokens", `not json`, "TBY-VAL-001"},
		// A validation failure is the client's: never the internal error.
		{"POST", "/api/v1/tokens", `{"name":"","role":"viewer"}`, "TBY-VAL-001"},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+env.tokens[auth.RoleAdmin])
		w := httptest.NewRecorder()
		env.mux.ServeHTTP(w, r)
		if got := problemCode(t, w); got != c.code || w.Code >= 500 {
			t.Errorf("%s %s %s: %d %s, want %s", c.method, c.path, c.body, w.Code, got, c.code)
		}
	}
}

// TestMalformedPathParameters: references and identifiers that do not
// match their grammar are refused with their code, before they reach a
// file-system path or a log record.
func TestMalformedPathParameters(t *testing.T) {
	env := newRBACEnv(t)
	for _, c := range []struct {
		method, path, body, code string
	}{
		{"GET", "/api/v1/tasks/..%2F..%2Fetc%2Fpasswd", "", "TBY-TSK-001"},
		{"GET", "/api/v1/tasks/%00", "", "TBY-TSK-001"},
		{"GET", "/api/v1/tasks/x/logs", "", "TBY-TSK-001"},
		{"DELETE", "/api/v1/accounts/..%2Fx", "", "TBY-AUTH-010"},
		{"PATCH", "/api/v1/accounts/%00", `{"role":"viewer"}`, "TBY-AUTH-010"},
		{"POST", "/api/v1/tokens/..%2F/revoke", "", "TBY-SRV-002"},
		{"POST", "/api/v1/import", `{"reference":"UPPER/Case:@@","platforms":[{"os":"linux","architecture":"amd64"}]}`, "TBY-REG-001"},
		{"POST", "/api/v1/import", `{"reference":"../../etc/passwd","platforms":[{"os":"linux","architecture":"amd64"}]}`, "TBY-REG-001"},
		{"GET", "/api/v1/import/inspect?reference=%00", "", "TBY-REG-001"},
		{"GET", "/api/v1/recipes/..%2F..%2Fx/mapping", "", "TBY-SRV-002"},
		{"GET", "/api/v1/media/slip?format=../../x", "", "TBY-VAL-001"},
		{"PUT", "/api/v1/retriever/interval", `{"interval":"forever"}`, ""},
	} {
		var body io.Reader = http.NoBody
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		r := httptest.NewRequest(c.method, c.path, body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+env.tokens[auth.RoleAdmin])
		w := httptest.NewRecorder()
		env.mux.ServeHTTP(w, r)
		got := problemCode(t, w)
		if w.Code < 400 || w.Code >= 500 || got == "" || (c.code != "" && got != c.code) {
			t.Errorf("%s %s: %d %q, want a 4xx %s", c.method, c.path, w.Code, got, c.code)
		}
	}
}
