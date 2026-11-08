// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// countingReader yields n zero bytes, framed as one multipart file part,
// and counts how many the server consumed.
type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestUIRequestBodiesAreBounded pins B-034. The session gate's
// anti-forgery check and the sign-in form read their fields with
// PostFormValue, which accepts multipart and spills file parts above
// 32 MiB to temporary files with no bound at all — on /login, before any
// authentication. A body is now cut at maxUIRequestBody, on every route
// the UI mounts.
func TestUIRequestBodiesAreBounded(t *testing.T) {
	const sent = 4 * maxUIRequestBody
	for _, path := range []string{"/login", "/lang"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			mux := mount(newTestUI(t, false))
			const boundary = "b0undary"
			head := "--" + boundary + "\r\n" +
				`Content-Disposition: form-data; name="blob"; filename="x"` + "\r\n\r\n"
			body := &countingReader{r: io.MultiReader(strings.NewReader(head), io.LimitReader(zeros{}, sent))}
			r := httptest.NewRequest(http.MethodPost, path, body)
			r.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if body.read > maxUIRequestBody+(64<<10) {
				t.Fatalf("%s consumed %d bytes of a %d-byte body, want at most the %d-byte bound",
					path, body.read, sent, maxUIRequestBody)
			}
		})
	}
}
