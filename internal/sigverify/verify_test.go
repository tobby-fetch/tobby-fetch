// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
)

const testRepo = "cookbook/app"

// addSubject stores a distinct subject manifest and returns its digest.
func addSubject(t *testing.T, st *sigtest.Store, seed string) string {
	t.Helper()
	payload := []byte(`{"schemaVersion":2,"seed":"` + seed + `"}`)
	return st.AddManifest(testRepo, "v"+seed, "application/vnd.oci.image.manifest.v1+json", payload)
}

// sigTagFor rebuilds the cosign convention tag for assertions and
// hand-crafted artifacts.
func sigTagFor(t *testing.T, manifestDigest string) string {
	t.Helper()
	hexPart, ok := strings.CutPrefix(manifestDigest, "sha256:")
	if !ok {
		t.Fatalf("digest %q not sha256-prefixed", manifestDigest)
	}
	return "sha256-" + hexPart + ".sig"
}

func mustKeys(t *testing.T, kps ...*sigtest.KeyPair) *sigverify.Keys {
	t.Helper()
	pems := make([][]byte, len(kps))
	for i, kp := range kps {
		pems[i] = mustPEM(t, kp)
	}
	keys, err := sigverify.ParsePublicKeys(pems...)
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	return keys
}

func TestVerifyHappyPath(t *testing.T) {
	t.Parallel()

	for _, alg := range []sigtest.Algorithm{sigtest.ECDSAP256, sigtest.ECDSAP384, sigtest.Ed25519, sigtest.RSA2048} {
		t.Run(string(alg), func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, string(alg))
			kp := mustKeyPair(t, alg)
			if err := sigtest.SignManifest(st, testRepo, dgst, kp); err != nil {
				t.Fatalf("SignManifest: %v", err)
			}

			fp, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if want := mustFingerprint(t, kp); fp != want {
				t.Errorf("Verify fingerprint = %q, want %q", fp, want)
			}
		})
	}
}

// TestVerifyKeyRotation exercises rotation by overlap (RECIPE-SPEC §12.3):
// with both the outgoing and the incoming key configured, content signed
// by either verifies, and the fingerprint identifies which one did.
func TestVerifyKeyRotation(t *testing.T) {
	t.Parallel()

	outgoing := mustKeyPair(t, sigtest.ECDSAP256)
	incoming := mustKeyPair(t, sigtest.Ed25519)
	keys := mustKeys(t, outgoing, incoming)

	for _, tc := range []struct {
		name   string
		signer *sigtest.KeyPair
	}{
		{"signed_by_outgoing", outgoing},
		{"signed_by_incoming", incoming},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, tc.name)
			if err := sigtest.SignManifest(st, testRepo, dgst, tc.signer); err != nil {
				t.Fatalf("SignManifest: %v", err)
			}

			fp, err := sigverify.Verify(context.Background(), st, testRepo, dgst, keys)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if want := mustFingerprint(t, tc.signer); fp != want {
				t.Errorf("Verify fingerprint = %q, want %q", fp, want)
			}
		})
	}
}

func TestVerifyNoSignature(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	kp := mustKeyPair(t, sigtest.ECDSAP256)
	keys := mustKeys(t, kp)

	t.Run("subject_present_unsigned", func(t *testing.T) {
		t.Parallel()
		dgst := addSubject(t, st, "unsigned")
		_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, keys)
		if !errors.Is(err, sigverify.ErrNoSignature) {
			t.Fatalf("Verify error = %v, want ErrNoSignature", err)
		}
	})

	t.Run("repo_unknown", func(t *testing.T) {
		t.Parallel()
		dgst := "sha256:" + strings.Repeat("ab", 32)
		_, err := sigverify.Verify(context.Background(), st, "no/such/repo", dgst, keys)
		if !errors.Is(err, sigverify.ErrNoSignature) {
			t.Fatalf("Verify error = %v, want ErrNoSignature", err)
		}
	})
}

func TestVerifyNoTrustedKey(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	dgst := addSubject(t, st, "foreign")
	foreign := mustKeyPair(t, sigtest.ECDSAP256)
	if err := sigtest.SignManifest(st, testRepo, dgst, foreign); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	trusted := mustKeys(t, mustKeyPair(t, sigtest.ECDSAP256), mustKeyPair(t, sigtest.Ed25519))
	_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, trusted)

	var ntk *sigverify.NoTrustedKeyError
	if !errors.As(err, &ntk) {
		t.Fatalf("Verify error = %v, want *NoTrustedKeyError", err)
	}
	// FR-033 acceptance: the rejection reports the fingerprint(s) tried.
	want := trusted.Fingerprints()
	if len(ntk.Tried) != len(want) {
		t.Fatalf("Tried = %v, want %v", ntk.Tried, want)
	}
	for i := range want {
		if ntk.Tried[i] != want[i] {
			t.Errorf("Tried[%d] = %q, want %q", i, ntk.Tried[i], want[i])
		}
		if !strings.Contains(err.Error(), want[i]) {
			t.Errorf("error message %q does not include fingerprint %q", err, want[i])
		}
	}
}

func TestVerifyBadArtifacts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// publish installs a broken signature artifact for dgst signed
		// (when relevant) by the trusted key kp.
		publish    func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair)
		wantReason string
	}{
		{
			name: "payload_pins_other_digest",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// Correctly signed by the trusted key, but pinning a
				// different subject: crypto is valid, pin is not.
				other := "sha256:" + strings.Repeat("42", 32)
				payload, err := sigtest.SimpleSigningPayload("registry.example/"+testRepo, other)
				if err != nil {
					t.Fatal(err)
				}
				sig, err := kp.Sign(payload)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload, SignatureB64: sig}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "pins digest",
		},
		{
			name: "missing_signature_annotation",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				payload, err := sigtest.SimpleSigningPayload("registry.example/"+testRepo, dgst)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: sigverify.AnnotationSignature,
		},
		{
			name: "annotation_not_base64",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				payload, err := sigtest.SimpleSigningPayload("registry.example/"+testRepo, dgst)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload, SignatureB64: "%%not-base64%%"}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "base64",
		},
		{
			name: "payload_not_simplesigning_json",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				payload := []byte("not a json document")
				sig, err := kp.Sign(payload)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload, SignatureB64: sig}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "SimpleSigning JSON",
		},
		{
			name: "wrong_critical_type",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				payload := []byte(`{"critical":{"identity":{"docker-reference":"x"},"image":{"docker-manifest-digest":"` + dgst + `"},"type":"something else"},"optional":null}`)
				sig, err := kp.Sign(payload)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload, SignatureB64: sig}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "critical.type",
		},
		{
			name: "no_simplesigning_layer",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: []byte("x"), SignatureB64: "eA==", MediaType: "application/octet-stream"}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "no " + sigverify.MediaTypeSimpleSigning + " layer",
		},
		{
			name: "manifest_not_json",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				st.AddManifest(testRepo, sigTagFor(t, dgst), "application/vnd.oci.image.manifest.v1+json", []byte("certainly not json"))
			},
			wantReason: "not valid JSON",
		},
		{
			name: "oversized_payload",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				huge := bytes.Repeat([]byte("A"), sigverify.MaxPayloadSize+1)
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: huge, SignatureB64: "eA=="}); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "exceeds limit",
		},
		{
			name: "tampered_payload_blob",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				payload, err := sigtest.SimpleSigningPayload("registry.example/"+testRepo, dgst)
				if err != nil {
					t.Fatal(err)
				}
				sig, err := kp.Sign(payload)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload, SignatureB64: sig}); err != nil {
					t.Fatal(err)
				}
				// Corrupt the stored payload after signing: the blob no
				// longer matches its descriptor digest.
				tampered := append([]byte{}, payload...)
				tampered[0] ^= 0xff
				st.PutBlob(testRepo, sigtest.DigestOf(payload), tampered)
			},
			wantReason: "does not match descriptor digest",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, tc.name)
			kp := mustKeyPair(t, sigtest.ECDSAP256)
			tc.publish(t, st, dgst, kp)

			_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
			var bad *sigverify.BadSignatureError
			if !errors.As(err, &bad) {
				t.Fatalf("Verify error = %v, want *BadSignatureError", err)
			}
			if !strings.Contains(bad.Reason, tc.wantReason) {
				t.Errorf("Reason %q does not mention %q", bad.Reason, tc.wantReason)
			}
		})
	}
}

// TestVerifyRotationSurvivesBrokenLayer checks that one malformed layer
// does not mask a valid one: verification tries every (layer, key) pair.
func TestVerifyRotationSurvivesBrokenLayer(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	dgst := addSubject(t, st, "mixed")
	kp := mustKeyPair(t, sigtest.ECDSAP256)
	payload, err := sigtest.SimpleSigningPayload("registry.example/"+testRepo, dgst)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := kp.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sigtest.PublishSignature(st, testRepo, dgst,
		sigtest.Layer{Payload: []byte("broken layer")}, // no annotation
		sigtest.Layer{Payload: payload, SignatureB64: sig},
	); err != nil {
		t.Fatal(err)
	}

	fp, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if want := mustFingerprint(t, kp); fp != want {
		t.Errorf("Verify fingerprint = %q, want %q", fp, want)
	}
}

func TestVerifyInputValidation(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	kp := mustKeyPair(t, sigtest.ECDSAP256)
	keys := mustKeys(t, kp)
	goodDigest := "sha256:" + strings.Repeat("ab", 32)

	t.Run("nil_keys", func(t *testing.T) {
		t.Parallel()
		if _, err := sigverify.Verify(context.Background(), st, testRepo, goodDigest, nil); err == nil {
			t.Fatal("Verify with nil keys: expected error")
		}
	})

	t.Run("empty_key_set", func(t *testing.T) {
		t.Parallel()
		if _, err := sigverify.Verify(context.Background(), st, testRepo, goodDigest, sigverify.Union()); err == nil {
			t.Fatal("Verify with empty key set: expected fail-closed error")
		}
	})

	for _, bad := range []string{"", "latest", "sha512:" + strings.Repeat("ab", 32), "sha256:short", "sha256:" + strings.Repeat("zz", 32)} {
		t.Run("bad_digest_"+bad, func(t *testing.T) {
			t.Parallel()
			if _, err := sigverify.Verify(context.Background(), st, testRepo, bad, keys); err == nil {
				t.Fatalf("Verify(%q): expected error", bad)
			}
		})
	}
}

// TestStoreBlobBoundedRead pins the Manifests contract sigtest.Store
// implements: blobs above MaxPayloadSize are refused by the reader itself,
// independently of the verifier's own size checks.
func TestStoreBlobBoundedRead(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	huge := bytes.Repeat([]byte("B"), sigverify.MaxPayloadSize+1)
	dgst := st.AddBlob(testRepo, huge)

	if _, err := st.Blob(context.Background(), testRepo, dgst); err == nil {
		t.Fatal("Blob above MaxPayloadSize: expected bounded-read refusal")
	}

	small := []byte("small enough")
	smallDgst := st.AddBlob(testRepo, small)
	got, err := st.Blob(context.Background(), testRepo, smallDgst)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if !bytes.Equal(got, small) {
		t.Errorf("Blob = %q, want %q", got, small)
	}

	if _, err := st.Blob(context.Background(), testRepo, "sha256:"+strings.Repeat("cd", 32)); !errors.Is(err, sigverify.ErrNotFound) {
		t.Errorf("Blob(unknown) error = %v, want ErrNotFound", err)
	}
}
