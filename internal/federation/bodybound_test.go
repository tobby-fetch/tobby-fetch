// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIdPResponsesAreBounded (NFR-023): a provider answering an endless
// document makes the read fail at the bound — it is neither buffered
// whole nor silently truncated into something that parses.
func TestIdPResponsesAreBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(endless{}, maxIDPResponse+10))
	}))
	defer srv.Close()
	c := &http.Client{Transport: boundedTransport{}}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil || n > maxIDPResponse {
		t.Errorf("read %d bytes, err %v: want a failure at the %d-byte bound", n, err, maxIDPResponse)
	}

	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"keys":[]}`)
	}))
	defer small.Close()
	resp, err = c.Get(small.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != `{"keys":[]}` {
		t.Errorf("an ordinary document: %q %v", body, err)
	}
}

// TestServiceClientIsBounded: the client the providers use is the bounded
// one, whatever transport the instance handed in.
func TestServiceClientIsBounded(t *testing.T) {
	s, err := New(&Options{Client: &http.Client{}})
	if err != nil {
		t.Skipf("constructing a bare service: %v", err)
	}
	if _, ok := s.client.Transport.(boundedTransport); !ok {
		t.Errorf("service transport is %T, want boundedTransport", s.client.Transport)
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// TestValidSubject: the identity key a provider asserts is bounded and
// free of control characters before it names a session (NFR-023).
func TestValidSubject(t *testing.T) {
	for s, want := range map[string]bool{
		"248289761001":             true,
		"alexis@example.org":       true,
		strings.Repeat("a", 255):   true,
		strings.Repeat("a", 256):   false,
		"evil\nforged log line":    false,
		"nul\x00byte":              false,
		string([]byte{0xff, 0xfe}): false,
	} {
		if got := validSubject(s); got != want {
			t.Errorf("validSubject(%q) = %v, want %v", s, got, want)
		}
	}
}
