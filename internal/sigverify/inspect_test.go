// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
)

// TestInspect locks the informational verdict an ingredient's own
// signature gets (FR-033 as amended for B-062): each of the three answers,
// in both layouts, and the honest answer of a zone where no key applies.
func TestInspect(t *testing.T) {
	t.Parallel()
	trusted := mustKeyPair(t, sigtest.ECDSAP256)
	foreign := mustKeyPair(t, sigtest.ECDSAP256)
	keys := mustKeys(t, trusted)

	cases := []struct {
		name string
		sign func(st *sigtest.Store, dgst string) error
		keys *sigverify.Keys
		want sigverify.State
		// fingerprint is the expected verifying key, tried the expected
		// keys tried, detail a substring of the expected detail.
		fingerprint string
		tried       []string
		detail      string
	}{
		{
			name: "verified_classic",
			sign: func(st *sigtest.Store, d string) error { return sigtest.SignManifest(st, testRepo, d, trusted) },
			keys: keys, want: sigverify.StateVerified, fingerprint: mustFingerprint(t, trusted),
		},
		{
			name: "verified_bundle",
			sign: func(st *sigtest.Store, d string) error { return sigtest.SignBundle(st, testRepo, d, trusted) },
			keys: keys, want: sigverify.StateVerified, fingerprint: mustFingerprint(t, trusted),
		},
		{
			name: "foreign_classic",
			sign: func(st *sigtest.Store, d string) error { return sigtest.SignManifest(st, testRepo, d, foreign) },
			keys: keys, want: sigverify.StateUnverifiable, tried: keys.Fingerprints(),
		},
		{
			name: "foreign_bundle",
			sign: func(st *sigtest.Store, d string) error { return sigtest.SignBundle(st, testRepo, d, foreign) },
			keys: keys, want: sigverify.StateUnverifiable, tried: keys.Fingerprints(),
		},
		{
			name: "none",
			sign: func(*sigtest.Store, string) error { return nil },
			keys: keys, want: sigverify.StateNone,
		},
		{
			name: "no_key_applies_signed",
			sign: func(st *sigtest.Store, d string) error { return sigtest.SignManifest(st, testRepo, d, foreign) },
			keys: nil, want: sigverify.StateUnverifiable, detail: "no trust root applies",
		},
		{
			name: "no_key_applies_unsigned",
			sign: func(*sigtest.Store, string) error { return nil },
			keys: nil, want: sigverify.StateNone,
		},
		{
			name: "malformed",
			sign: func(st *sigtest.Store, d string) error {
				_, err := sigtest.PublishSignature(st, testRepo, d, sigtest.Layer{Payload: []byte("{}"), SignatureB64: "!!"})
				return err
			},
			keys: keys, want: sigverify.StateUnverifiable, detail: "invalid signature artifact",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, tc.name)
			if err := tc.sign(st, dgst); err != nil {
				t.Fatalf("signing: %v", err)
			}
			in := sigverify.Inspect(context.Background(), st, testRepo, dgst, tc.keys)
			if in.State != tc.want {
				t.Fatalf("state = %q (%s), want %q", in.State, in, tc.want)
			}
			if in.Fingerprint != tc.fingerprint {
				t.Errorf("fingerprint = %q, want %q", in.Fingerprint, tc.fingerprint)
			}
			if strings.Join(in.Tried, ",") != strings.Join(tc.tried, ",") {
				t.Errorf("tried = %v, want %v", in.Tried, tc.tried)
			}
			if !strings.Contains(in.Detail, tc.detail) {
				t.Errorf("detail = %q, want it to contain %q", in.Detail, tc.detail)
			}
			if in.Verified() != (tc.want == sigverify.StateVerified) {
				t.Errorf("Verified() = %v for state %q", in.Verified(), in.State)
			}
		})
	}
}

// failingManifests is a store that cannot be read.
type failingManifests struct{}

func (failingManifests) Manifest(context.Context, string, string) (payload []byte, mediaType, dgst string, err error) {
	return nil, "", "", errors.New("disk on fire")
}

func (failingManifests) Blob(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("disk on fire")
}

// TestInspectNeverClaimsAbsenceOnAReadFailure: a store that cannot be
// read says nothing about whether a signature exists, so the answer is
// "unverifiable", with the cause — never "none".
func TestInspectNeverClaimsAbsenceOnAReadFailure(t *testing.T) {
	t.Parallel()
	in := sigverify.Inspect(context.Background(), failingManifests{}, testRepo,
		"sha256:"+strings.Repeat("ab", 32), mustKeys(t, mustKeyPair(t, sigtest.ECDSAP256)))
	if in.State != sigverify.StateUnverifiable || !strings.Contains(in.Detail, "disk on fire") {
		t.Fatalf("inspection = %+v, want unverifiable carrying the read failure", in)
	}
}

// TestInspectionString is the wording every log line and report row uses.
func TestInspectionString(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   sigverify.Inspection
		want string
	}{
		{sigverify.Inspection{State: sigverify.StateVerified, Fingerprint: "SHA256:abc"}, "verified by SHA256:abc"},
		{sigverify.Inspection{State: sigverify.StateUnverifiable, Tried: []string{"SHA256:a", "SHA256:b"}},
			"present, not verifiable by this zone's trust roots (keys tried: SHA256:a, SHA256:b)"},
		{sigverify.Inspection{State: sigverify.StateUnverifiable, Detail: "no trust root applies"},
			"present, not verifiable by this zone's trust roots (no trust root applies)"},
		{sigverify.Inspection{State: sigverify.StateNone}, "none"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
