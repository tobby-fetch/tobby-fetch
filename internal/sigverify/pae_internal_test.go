// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// This file tests unexported internals and therefore lives in package
// sigverify rather than sigverify_test. It must not import sigtest, which
// imports sigverify.

package sigverify

import (
	"bytes"
	"strings"
	"testing"
)

// TestPreAuthEncoding pins the DSSE pre-authentication encoding byte for
// byte on hand-assembled vectors. It is the cryptographic core of the
// bundle path: the signature covers these bytes and nothing else, so a
// length prefix off by one, a separator that is not a single space, or any
// normalisation of the payload would silently change what every signature
// in the bundle layout means.
func TestPreAuthEncoding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		payloadType string
		payload     string
		// want is assembled by hand from the spec:
		//   "DSSEv1" SP len(payloadType) SP payloadType SP len(payload) SP payload
		want string
	}{
		{
			// The combination cosign actually produces:
			// len("application/vnd.in-toto+json") = 28,
			// len(`{"hello":"world"}`) = 17.
			name:        "cosign_payload_type",
			payloadType: payloadTypeInToto,
			payload:     `{"hello":"world"}`,
			want:        `DSSEv1 28 application/vnd.in-toto+json 17 {"hello":"world"}`,
		},
		{
			// An empty payload still contributes its length and its
			// separator: the encoding ends on a trailing space, it is not
			// trimmed.
			name:        "empty_payload",
			payloadType: "t",
			payload:     "",
			want:        "DSSEv1 1 t 0 ",
		},
		{
			// Lengths count bytes, not runes: "é" is two bytes of UTF-8.
			name:        "multibyte_payload",
			payloadType: "t",
			payload:     "é",
			want:        "DSSEv1 1 t 2 é",
		},
		{
			// Payload and type bytes are copied verbatim — embedded spaces
			// and newlines are neither escaped nor collapsed, which is
			// precisely why the length prefixes are load-bearing.
			name:        "separators_inside_the_fields",
			payloadType: "a b",
			payload:     "x y\nz",
			want:        "DSSEv1 3 a b 5 x y\nz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := preAuthEncoding(tc.payloadType, []byte(tc.payload))
			if string(got) != tc.want {
				t.Errorf("preAuthEncoding(%q, %q) =\n %q\nwant %q", tc.payloadType, tc.payload, got, tc.want)
			}
		})
	}
}

// TestPreAuthEncodingIsUnambiguous checks the property the length prefixes
// exist for: two different (payloadType, payload) pairs can never encode to
// the same bytes. Without it, a signature over one document would be a
// signature over another.
func TestPreAuthEncodingIsUnambiguous(t *testing.T) {
	t.Parallel()

	// Concatenating type and payload the other way round yields the same
	// characters in the same order — only the framing tells them apart.
	first := preAuthEncoding("a b", []byte("x"))
	second := preAuthEncoding("a", []byte("b x"))
	if bytes.Equal(first, second) {
		t.Fatalf("distinct (payloadType, payload) pairs share the encoding %q", first)
	}
	if !strings.HasPrefix(string(first), dsseV1+" ") || !strings.HasPrefix(string(second), dsseV1+" ") {
		t.Errorf("encodings %q / %q are not %q-prefixed", first, second, dsseV1)
	}
}
