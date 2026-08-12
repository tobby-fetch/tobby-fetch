// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
)

// referrersAPIStore is a registry that implements the OCI 1.1 Referrers API
// (sigverify.ReferrersLister) and serves NO fallback tag: on GHCR, Harbor
// and every modern registry, "sha256-<hex>" simply does not resolve and
// enumeration is the only way to find a referring signature. Hiding the tag
// is what makes the API load-bearing in the tests that use this type.
type referrersAPIStore struct {
	*sigtest.Store
	hiddenTag string
	referrers []string
	// listErr, when set, is what the Referrers API returns instead of a
	// listing — a registry refusing or failing the call.
	listErr error
}

var _ sigverify.ReferrersLister = (*referrersAPIStore)(nil)

// Manifest implements sigverify.Manifests, hiding the fallback tag.
func (r *referrersAPIStore) Manifest(ctx context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error) {
	if reference == r.hiddenTag {
		return nil, "", "", fmt.Errorf("manifest %s:%s: %w", repo, reference, sigverify.ErrNotFound)
	}
	return r.Store.Manifest(ctx, repo, reference)
}

// Referrers implements sigverify.ReferrersLister.
func (r *referrersAPIStore) Referrers(_ context.Context, _, _ string) ([]string, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.referrers, nil
}

// failingBlobStore is a registry whose manifests read fine but whose blobs
// fail with a transport error — the state of a registry that goes away
// mid-verification.
type failingBlobStore struct {
	*sigtest.Store
	err error
}

// Blob implements sigverify.Manifests.
func (f *failingBlobStore) Blob(_ context.Context, _, _ string) ([]byte, error) {
	return nil, f.err
}

// publishRawReferrer stores a hand-written referring manifest and lists it
// in the subject's fallback referrers index, for shapes sigtest's builder
// deliberately cannot produce.
func publishRawReferrer(t *testing.T, st *sigtest.Store, subject, manifest string) {
	t.Helper()
	dgst := st.AddManifest(testRepo, "", mediaTypeOCIManifest, []byte(manifest))
	st.AddManifest(testRepo, bundleTagFor(t, subject), mediaTypeOCIIndex,
		[]byte(`{"schemaVersion":2,"mediaType":"`+mediaTypeOCIIndex+`","manifests":[{"digest":"`+dgst+`"}]}`))
}

// bundleReferrerJSON assembles a referring artifact manifest announcing the
// bundle artifactType, with an arbitrary single layer descriptor.
func bundleReferrerJSON(subject, layerMediaType, layerDigest string, layerSize int) string {
	return `{"schemaVersion":2,"mediaType":"` + mediaTypeOCIManifest + `",` +
		`"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:` + strings.Repeat("00", 32) + `","size":2},` +
		`"layers":[{"mediaType":"` + layerMediaType + `","digest":"` + layerDigest + `","size":` + strconv.Itoa(layerSize) + `}],` +
		`"subject":{"mediaType":"` + mediaTypeOCIManifest + `","digest":"` + subject + `","size":2},` +
		`"artifactType":"` + sigtest.MediaTypeSigstoreBundle + `"}`
}

// mustSignedBundle builds the happy-path bundle document for dgst.
func mustSignedBundle(t *testing.T, dgst string, kp *sigtest.KeyPair) []byte {
	t.Helper()
	blob, err := sigtest.SignedBundle(dgst, kp)
	if err != nil {
		t.Fatalf("SignedBundle: %v", err)
	}
	return blob
}

// mustBundle marshals env, failing the test rather than the caller.
func mustBundle(t *testing.T, env sigtest.BundleEnvelope) []byte {
	t.Helper()
	blob, err := sigtest.DSSEBundle(env)
	if err != nil {
		t.Fatalf("DSSEBundle: %v", err)
	}
	return blob
}

// mustStatement builds the in-toto statement pinning dgst.
func mustStatement(t *testing.T, dgst string) []byte {
	t.Helper()
	st, err := sigtest.InTotoStatement(dgst)
	if err != nil {
		t.Fatalf("InTotoStatement: %v", err)
	}
	return st
}

// mustPublishBundle publishes art as a referring artifact of subject.
func mustPublishBundle(t *testing.T, st *sigtest.Store, subject string, art sigtest.BundleArtifact) string {
	t.Helper()
	dgst, err := sigtest.PublishBundle(st, testRepo, subject, art)
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}
	return dgst
}

// signedEnvelope signs an arbitrary payload the DSSE way (over the
// pre-authentication encoding), for fixtures whose payload deviates from the
// well-formed statement while the signature over it stays genuine.
func signedEnvelope(t *testing.T, kp *sigtest.KeyPair, payloadType string, payload []byte) sigtest.BundleEnvelope {
	t.Helper()
	sig, err := kp.SignDSSE(payloadType, payload)
	if err != nil {
		t.Fatalf("SignDSSE: %v", err)
	}
	return sigtest.BundleEnvelope{
		PayloadB64:   base64.StdEncoding.EncodeToString(payload),
		PayloadType:  payloadType,
		SignatureB64: sig,
	}
}

func TestVerifyBundleHappyPath(t *testing.T) {
	t.Parallel()

	for _, alg := range []sigtest.Algorithm{sigtest.ECDSAP256, sigtest.ECDSAP384, sigtest.Ed25519, sigtest.RSA2048} {
		t.Run(string(alg), func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, "bundle-"+string(alg))
			kp := mustKeyPair(t, alg)
			if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
				t.Fatalf("SignBundle: %v", err)
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

// TestVerifyBundleKeyRotation exercises rotation by overlap (RECIPE-SPEC
// §12.3) on the bundle path: with both keys configured, a bundle signed by
// either verifies, and the fingerprint says which one did.
func TestVerifyBundleKeyRotation(t *testing.T) {
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
			dgst := addSubject(t, st, "bundle-rotation-"+tc.name)
			if err := sigtest.SignBundle(st, testRepo, dgst, tc.signer); err != nil {
				t.Fatalf("SignBundle: %v", err)
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

// TestVerifyBundleSubjectBinding is the central security property of the
// bundle layout. The DSSE signature covers a statement, not a manifest: a
// perfectly signed statement naming ANOTHER artifact is a valid signature
// that says nothing about this digest, and admitting it would let anyone
// re-point any signature they legitimately hold at any content.
func TestVerifyBundleSubjectBinding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// statement builds the in-toto payload, given the digest actually
		// being verified. Every case is signed genuinely by the trust root.
		statement  func(t *testing.T, dgst string) []byte
		wantReason string
	}{
		{
			name: "names_another_digest",
			statement: func(t *testing.T, _ string) []byte {
				t.Helper()
				// Signed by the trusted key, structurally impeccable, and
				// about a completely different artifact.
				return mustStatement(t, "sha256:"+strings.Repeat("42", 32))
			},
			wantReason: "does not name subject",
		},
		{
			name: "no_subject_at_all",
			statement: func(t *testing.T, _ string) []byte {
				t.Helper()
				return []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[],"predicateType":"x"}`)
			},
			wantReason: "does not name subject",
		},
		{
			name: "subject_digest_under_another_algorithm",
			statement: func(t *testing.T, dgst string) []byte {
				t.Helper()
				// The right hex, filed under sha512: the sha256 entry the
				// verifier compares is absent, so nothing is pinned.
				hexPart := strings.TrimPrefix(dgst, "sha256:")
				return []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"digest":{"sha512":"` + hexPart + `"}}],"predicateType":"x"}`)
			},
			wantReason: "does not name subject",
		},
		{
			name: "statement_type_unexpected",
			statement: func(t *testing.T, dgst string) []byte {
				t.Helper()
				hexPart := strings.TrimPrefix(dgst, "sha256:")
				return []byte(`{"_type":"https://in-toto.io/Statement/v0.1","subject":[{"digest":{"sha256":"` + hexPart + `"}}],"predicateType":"x"}`)
			},
			wantReason: "statement _type",
		},
		{
			name: "payload_is_not_json",
			statement: func(t *testing.T, _ string) []byte {
				t.Helper()
				return []byte("this is not a statement")
			},
			wantReason: "not an in-toto statement",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, "binding-"+tc.name)
			kp := mustKeyPair(t, sigtest.ECDSAP256)

			env := signedEnvelope(t, kp, sigtest.PayloadTypeInToto, tc.statement(t, dgst))
			mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: mustBundle(t, env)})

			_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
			var bad *sigverify.BadSignatureError
			if !errors.As(err, &bad) {
				t.Fatalf("Verify error = %v (%T), want *BadSignatureError", err, err)
			}
			if !strings.Contains(bad.Reason, tc.wantReason) {
				t.Errorf("Reason %q does not mention %q", bad.Reason, tc.wantReason)
			}
		})
	}
}

// TestVerifyBundleMalformed collects the artifacts that exist but must not
// admit the subject. Everything here fails closed (RECIPE-SPEC §12.3): a
// signature the verifier cannot fully check is a blocked subject, never a
// skipped one.
func TestVerifyBundleMalformed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// publish installs one broken bundle artifact for dgst; kp is the
		// configured trust root.
		publish    func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair)
		wantReason string
	}{
		{
			name: "referrer_subject_points_elsewhere",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// The manifest is listed as a referrer of dgst yet claims to
				// sign something else. Contradictory metadata is hostile,
				// not merely irrelevant.
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
					Blob:    mustSignedBundle(t, dgst, kp),
					Subject: "sha256:" + strings.Repeat("13", 32),
				})
			},
			wantReason: "refers to",
		},
		{
			name: "payload_type_unexpected",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// The payloadType is part of the signed pre-authentication
				// encoding, so accepting an unknown one would mean trusting a
				// document the verifier does not know how to read.
				env := signedEnvelope(t, kp, "application/vnd.dev.cosign.something+json", mustStatement(t, dgst))
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: mustBundle(t, env)})
			},
			wantReason: "payloadType",
		},
		{
			name: "payload_not_base64",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: mustBundle(t, sigtest.BundleEnvelope{
					PayloadB64:   "%%not base64%%",
					SignatureB64: "c2lnbmF0dXJl",
				})})
			},
			wantReason: "not valid base64",
		},
		{
			name: "bundle_not_json",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: []byte("{not a bundle")})
			},
			wantReason: "not valid JSON",
		},
		{
			name: "envelope_absent",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
					Blob: []byte(`{"mediaType":"` + sigtest.MediaTypeSigstoreBundle + `","verificationMaterial":{"publicKey":{"hint":"x"}}}`),
				})
			},
			wantReason: "no DSSE envelope",
		},
		{
			name: "envelope_without_signature",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: mustBundle(t, sigtest.BundleEnvelope{
					PayloadB64: base64.StdEncoding.EncodeToString(mustStatement(t, dgst)),
				})})
			},
			wantReason: "no decodable signature",
		},
		{
			name: "message_signature_bundle",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				// `cosign sign-blob` output: a real signature over bytes that
				// are nowhere in the registry. Nothing in it can bind this
				// manifest, so admitting it would be verifying nothing.
				blob, err := sigtest.MessageSignatureBundle(strings.Repeat("ab", 32), "c2lnbmF0dXJl")
				if err != nil {
					t.Fatalf("MessageSignatureBundle: %v", err)
				}
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: blob})
			},
			wantReason: "messageSignature",
		},
		{
			name: "keyless_bundle",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// Certificate-based Sigstore material: out of scope by
				// ADR-0007 (verification is key-based and offline, no Fulcio,
				// no Rekor). Reported explicitly rather than silently ignored,
				// so an operator sees why their artifact is refused.
				env := signedEnvelope(t, kp, sigtest.PayloadTypeInToto, mustStatement(t, dgst))
				env.Keyless = true
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: mustBundle(t, env)})
			},
			wantReason: "keyless",
		},
		{
			name: "blob_does_not_match_descriptor",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				blob := mustSignedBundle(t, dgst, kp)
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: blob})
				// Swap the content behind the descriptor after publication.
				tampered := bytes.Clone(blob)
				tampered[0] ^= 0xff
				st.PutBlob(testRepo, sigtest.DigestOf(blob), tampered)
			},
			wantReason: "does not match descriptor digest",
		},
		{
			name: "oversized_bundle",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				// Refused on the descriptor's own size claim, before a single
				// byte is fetched.
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
					Blob: bytes.Repeat([]byte("A"), sigverify.MaxPayloadSize+1),
				})
			},
			wantReason: "exceeds limit",
		},
		{
			name: "no_bundle_layer",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// artifactType announces a bundle but the layer media type
				// does not: the artifact claims to be a signature and carries
				// none.
				blob := mustSignedBundle(t, dgst, kp)
				publishRawReferrer(t, st, dgst, bundleReferrerJSON(dgst, "application/octet-stream", st.AddBlob(testRepo, blob), len(blob)))
			},
			wantReason: "no readable bundle layer",
		},
		{
			name: "layer_descriptor_digest_malformed",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				// The descriptor is the only thing binding the fetched bytes
				// to the manifest: an unparseable one cannot be checked, so
				// the layer cannot be trusted.
				publishRawReferrer(t, st, dgst, bundleReferrerJSON(dgst, sigtest.MediaTypeSigstoreBundle, "md5:whatever", 10))
			},
			wantReason: "descriptor digest",
		},
		{
			name: "bundle_blob_missing",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				publishRawReferrer(t, st, dgst, bundleReferrerJSON(dgst, sigtest.MediaTypeSigstoreBundle, "sha256:"+strings.Repeat("ee", 32), 10))
			},
			wantReason: "missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, "malformed-"+tc.name)
			kp := mustKeyPair(t, sigtest.ECDSAP256)
			tc.publish(t, st, dgst, kp)

			_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
			var bad *sigverify.BadSignatureError
			if !errors.As(err, &bad) {
				t.Fatalf("Verify error = %v (%T), want *BadSignatureError", err, err)
			}
			if !strings.Contains(bad.Reason, tc.wantReason) {
				t.Errorf("Reason %q does not mention %q", bad.Reason, tc.wantReason)
			}
			// The rendered message is what an operator reads, and it must
			// carry the reason rather than only the type name.
			if !strings.Contains(err.Error(), bad.Reason) {
				t.Errorf("error %q does not render the reason %q", err, bad.Reason)
			}
		})
	}
}

// TestVerifyBundleBlobTransportFailure proves a broken registry is not
// mistaken for a broken signature: the transport error passes through, so
// the caller retries instead of concluding the content is unsigned.
func TestVerifyBundleBlobTransportFailure(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	dgst := addSubject(t, st, "bundle-transport")
	kp := mustKeyPair(t, sigtest.ECDSAP256)
	if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}
	boom := errors.New("connection reset by peer")

	_, err := sigverify.Verify(context.Background(), &failingBlobStore{Store: st, err: boom}, testRepo, dgst, mustKeys(t, kp))
	if !errors.Is(err, boom) {
		t.Fatalf("Verify error = %v, want the transport error to surface", err)
	}
	var bad *sigverify.BadSignatureError
	if errors.As(err, &bad) {
		t.Errorf("a registry failure was reported as an invalid artifact: %v", err)
	}
}

// TestVerifyBundleNoTrustedKey pins the difference that matters to an
// operator: a well-formed bundle pinning the right digest but signed by an
// unknown key is "not signed by anyone you trust" — reported with the
// fingerprints tried (FR-033) — not "malformed artifact".
func TestVerifyBundleNoTrustedKey(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	dgst := addSubject(t, st, "bundle-foreign")
	if err := sigtest.SignBundle(st, testRepo, dgst, mustKeyPair(t, sigtest.ECDSAP256)); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}

	trusted := mustKeys(t, mustKeyPair(t, sigtest.ECDSAP256), mustKeyPair(t, sigtest.Ed25519))
	_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, trusted)

	var ntk *sigverify.NoTrustedKeyError
	if !errors.As(err, &ntk) {
		t.Fatalf("Verify error = %v (%T), want *NoTrustedKeyError", err, err)
	}
	for _, want := range trusted.Fingerprints() {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not report the fingerprint %q tried", err, want)
		}
	}
}

// TestVerifyBundleIgnoredReferrers covers referrers that are none of the
// verifier's business or simply unusable. They must be skipped silently:
// reporting "invalid signature" for someone else's SBOM would block content
// that is merely unsigned, and the taxonomy distinction drives what an
// operator is told (FR-033).
func TestVerifyBundleIgnoredReferrers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		publish func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair)
	}{
		{
			name: "other_artifact_type",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// An SBOM attestation referring to the same subject. Not a
				// signature, and the only referrer there is.
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
					Blob:         mustSignedBundle(t, dgst, kp),
					ArtifactType: "application/spdx+json",
				})
			},
		},
		{
			name: "dangling_referrer",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
				t.Helper()
				// Listed in the index, absent from the registry: a partially
				// garbage-collected store, not an attack.
				mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
					Blob:     mustSignedBundle(t, dgst, kp),
					Dangling: true,
				})
			},
		},
		{
			name: "fallback_index_malformed",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				st.AddManifest(testRepo, bundleTagFor(t, dgst), mediaTypeOCIIndex, []byte("]not an index["))
			},
		},
		{
			name: "fallback_index_lists_nothing",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				st.AddManifest(testRepo, bundleTagFor(t, dgst), mediaTypeOCIIndex,
					[]byte(`{"schemaVersion":2,"mediaType":"`+mediaTypeOCIIndex+`","manifests":[]}`))
			},
		},
		{
			name: "referring_manifest_not_json",
			publish: func(t *testing.T, st *sigtest.Store, dgst string, _ *sigtest.KeyPair) {
				t.Helper()
				manifestDigest := st.AddManifest(testRepo, "", mediaTypeOCIManifest, []byte("definitely not a manifest"))
				st.AddManifest(testRepo, bundleTagFor(t, dgst), mediaTypeOCIIndex,
					[]byte(`{"schemaVersion":2,"mediaType":"`+mediaTypeOCIIndex+`","manifests":[{"digest":"`+manifestDigest+`"}]}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := sigtest.NewStore()
			dgst := addSubject(t, st, "ignored-"+tc.name)
			kp := mustKeyPair(t, sigtest.ECDSAP256)
			tc.publish(t, st, dgst, kp)

			_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
			if !errors.Is(err, sigverify.ErrNoSignature) {
				t.Fatalf("Verify error = %v (%T), want ErrNoSignature", err, err)
			}
		})
	}
}

// TestVerifyBundleValidAmongIgnorable proves a genuine bundle still wins
// when it shares the subject with referrers the verifier skips — the normal
// state of a repository carrying an SBOM, an attestation and a signature.
func TestVerifyBundleValidAmongIgnorable(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	dgst := addSubject(t, st, "bundle-among-others")
	kp := mustKeyPair(t, sigtest.ECDSAP256)

	// Published first, so the valid signature is only reached by continuing
	// past both of them.
	mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
		Blob:         []byte(`{"spdxVersion":"SPDX-2.3"}`),
		ArtifactType: "application/spdx+json",
	})
	mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
		Blob:     mustSignedBundle(t, dgst, kp),
		Dangling: true,
	})
	if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}

	fp, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if want := mustFingerprint(t, kp); fp != want {
		t.Errorf("Verify fingerprint = %q, want %q", fp, want)
	}
}

// TestVerifyBundleDiscovery pins both discovery routes and their union. A
// store that travelled carries the fallback tag; a public registry carries
// the Referrers API; neither may be required.
func TestVerifyBundleDiscovery(t *testing.T) {
	t.Parallel()

	t.Run("referrers_api_only", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "discovery-api")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		// NoFallbackTag: nothing resolves under "sha256-<hex>", so the
		// enumeration is the only route to the artifact.
		manifestDigest := mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
			Blob:          mustSignedBundle(t, dgst, kp),
			NoFallbackTag: true,
		})
		src := &referrersAPIStore{Store: st, hiddenTag: bundleTagFor(t, dgst), referrers: []string{manifestDigest}}

		if _, err := sigverify.Verify(context.Background(), src, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(Referrers API only): %v", err)
		}
	})

	t.Run("fallback_tag_only", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "discovery-tag")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}
		// A Store implements no Referrers API at all.
		if _, ok := any(st).(sigverify.ReferrersLister); ok {
			t.Fatal("sigtest.Store unexpectedly implements ReferrersLister; the fallback route is no longer exercised")
		}

		if _, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(fallback tag only): %v", err)
		}
	})

	t.Run("both_routes_same_artifact", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "discovery-both")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		manifestDigest := mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{Blob: mustSignedBundle(t, dgst, kp)})
		// The same digest from both routes must be verified once, not twice.
		src := &referrersAPIStore{Store: st, referrers: []string{manifestDigest, manifestDigest}}

		if _, err := sigverify.Verify(context.Background(), src, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(both routes): %v", err)
		}
	})

	t.Run("referrers_api_not_found_is_not_an_error", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "discovery-api-404")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}
		// A registry answering 404 to the Referrers API must fall back to the
		// tag rather than fail the whole verification.
		src := &referrersAPIStore{Store: st, listErr: sigverify.ErrNotFound}

		if _, err := sigverify.Verify(context.Background(), src, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(Referrers API returning ErrNotFound): %v", err)
		}
	})

	t.Run("referrers_api_failure_is_fatal", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "discovery-api-broken")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}
		// A transport failure is not "no signature": fail closed and pass the
		// cause through, rather than silently downgrading to the fallback tag.
		boom := errors.New("registry unreachable")
		src := &referrersAPIStore{Store: st, listErr: boom}

		_, err := sigverify.Verify(context.Background(), src, testRepo, dgst, mustKeys(t, kp))
		if !errors.Is(err, boom) {
			t.Fatalf("Verify error = %v, want the transport error to surface", err)
		}
	})
}

// TestVerifyBundleRelocated repeats FR-035/ADR-0013 on fabricated fixtures:
// the referring artifact and its subject travel to another repository, and
// only the digest is authoritative.
func TestVerifyBundleRelocated(t *testing.T) {
	t.Parallel()

	const origin, mirror = "vendor/upstream", "zone-b/internal/mirror"
	kp := mustKeyPair(t, sigtest.ECDSAP256)
	dgst := sigtest.DigestOf([]byte(`{"schemaVersion":2,"seed":"relocated"}`))

	st := sigtest.NewStore()
	if err := sigtest.SignBundle(st, mirror, dgst, kp); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}
	// Nothing under the origin name: the artifact only exists relocated.
	if _, err := sigverify.Verify(context.Background(), st, origin, dgst, mustKeys(t, kp)); !errors.Is(err, sigverify.ErrNoSignature) {
		t.Fatalf("Verify(origin) error = %v, want ErrNoSignature", err)
	}
	if _, err := sigverify.Verify(context.Background(), st, mirror, dgst, mustKeys(t, kp)); err != nil {
		t.Fatalf("Verify(relocated): %v", err)
	}
}

// TestVerifyBundleOmittedSubjectDescriptor covers a referring manifest with
// no "subject" descriptor. The OCI metadata is then uninformative, but the
// in-toto statement inside still binds the digest, so verification neither
// crashes on the missing field nor accepts anything it should not.
func TestVerifyBundleOmittedSubjectDescriptor(t *testing.T) {
	t.Parallel()

	kp := mustKeyPair(t, sigtest.ECDSAP256)

	t.Run("statement_pins_the_subject", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "no-subject-ok")
		mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
			Blob:        mustSignedBundle(t, dgst, kp),
			OmitSubject: true,
		})
		if _, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})

	t.Run("statement_pins_something_else", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "no-subject-ko")
		other := "sha256:" + strings.Repeat("77", 32)
		mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
			Blob:        mustSignedBundle(t, other, kp),
			OmitSubject: true,
		})
		_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
		var bad *sigverify.BadSignatureError
		if !errors.As(err, &bad) {
			t.Fatalf("Verify error = %v (%T), want *BadSignatureError", err, err)
		}
	})
}

// --- Cohabitation of the two published layouts -------------------------
//
// Publishers pick a format and consumers should not have to: a subject is
// admitted as soon as ONE signature verifies against one trusted key, in
// either layout, and blocked when neither does.

// TestVerifyLayoutCohabitation walks every combination of the classic
// attached signature and the Sigstore bundle.
func TestVerifyLayoutCohabitation(t *testing.T) {
	t.Parallel()

	// publishInvalidLegacy installs a structurally broken attached signature
	// (payload pinning another digest, genuinely signed by kp).
	publishInvalidLegacy := func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
		t.Helper()
		payload, err := sigtest.SimpleSigningPayload("registry.example/"+testRepo, "sha256:"+strings.Repeat("91", 32))
		if err != nil {
			t.Fatalf("SimpleSigningPayload: %v", err)
		}
		sig, err := kp.Sign(payload)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if _, err := sigtest.PublishSignature(st, testRepo, dgst, sigtest.Layer{Payload: payload, SignatureB64: sig}); err != nil {
			t.Fatalf("PublishSignature: %v", err)
		}
	}
	// publishInvalidBundle installs a bundle whose statement names another
	// digest, likewise genuinely signed.
	publishInvalidBundle := func(t *testing.T, st *sigtest.Store, dgst string, kp *sigtest.KeyPair) {
		t.Helper()
		mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
			Blob: mustSignedBundle(t, "sha256:"+strings.Repeat("92", 32), kp),
		})
	}

	t.Run("legacy_only", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-legacy")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignManifest(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignManifest: %v", err)
		}
		if _, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(legacy only): %v", err)
		}
	})

	t.Run("bundle_only", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-bundle")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}
		if _, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(bundle only): %v", err)
		}
	})

	t.Run("both_valid", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-both")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignManifest(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignManifest: %v", err)
		}
		if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}
		fp, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
		if err != nil {
			t.Fatalf("Verify(both layouts): %v", err)
		}
		if want := mustFingerprint(t, kp); fp != want {
			t.Errorf("Verify fingerprint = %q, want %q", fp, want)
		}
	})

	t.Run("stale_legacy_fresh_bundle", func(t *testing.T) {
		t.Parallel()
		// A repository re-signed with a rotated key: the old ".sig" no longer
		// verifies but a fresh bundle does. Stopping at the first layout
		// would block content that IS correctly signed.
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-stale-legacy")
		retired := mustKeyPair(t, sigtest.ECDSAP256)
		current := mustKeyPair(t, sigtest.Ed25519)
		if err := sigtest.SignManifest(st, testRepo, dgst, retired); err != nil {
			t.Fatalf("SignManifest: %v", err)
		}
		if err := sigtest.SignBundle(st, testRepo, dgst, current); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}

		fp, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, current))
		if err != nil {
			t.Fatalf("Verify(stale legacy, valid bundle): %v", err)
		}
		if want := mustFingerprint(t, current); fp != want {
			t.Errorf("Verify fingerprint = %q, want the bundle signer %q", fp, want)
		}
	})

	t.Run("malformed_legacy_fresh_bundle", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-broken-legacy")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		publishInvalidLegacy(t, st, dgst, kp)
		if err := sigtest.SignBundle(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignBundle: %v", err)
		}
		if _, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(malformed legacy, valid bundle): %v", err)
		}
	})

	t.Run("valid_legacy_broken_bundle", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-broken-bundle")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		if err := sigtest.SignManifest(st, testRepo, dgst, kp); err != nil {
			t.Fatalf("SignManifest: %v", err)
		}
		publishInvalidBundle(t, st, dgst, kp)
		if _, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp)); err != nil {
			t.Fatalf("Verify(valid legacy, broken bundle): %v", err)
		}
	})

	t.Run("both_invalid_reports_both", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-both-broken")
		kp := mustKeyPair(t, sigtest.ECDSAP256)
		publishInvalidLegacy(t, st, dgst, kp)
		publishInvalidBundle(t, st, dgst, kp)

		_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
		var bad *sigverify.BadSignatureError
		if !errors.As(err, &bad) {
			t.Fatalf("Verify error = %v (%T), want *BadSignatureError", err, err)
		}
		// Diagnosing a doubly-broken repository requires both stories, not
		// whichever layout happened to be inspected first.
		if !strings.Contains(bad.Reason, "pins digest") {
			t.Errorf("Reason %q does not report the attached signature's failure", bad.Reason)
		}
		if !strings.Contains(bad.Reason, "does not name subject") {
			t.Errorf("Reason %q does not report the bundle's failure", bad.Reason)
		}
	})

	// A well-formed signature that no configured key verifies is a
	// TRUST-ROOT verdict, and FR-033 requires naming the fingerprints
	// tried. That must hold whichever layout carried it — including when
	// the other layout left a malformed artifact behind, which would
	// otherwise have the operator chasing a corruption that is not there.
	t.Run("malformed_legacy_untrusted_bundle_reports_trust_roots", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-broken-legacy-foreign-bundle")
		trusted := mustKeyPair(t, sigtest.ECDSAP256)
		foreign := mustKeyPair(t, sigtest.ECDSAP256)

		publishInvalidLegacy(t, st, dgst, trusted)
		mustPublishBundle(t, st, dgst, sigtest.BundleArtifact{
			Blob: mustSignedBundle(t, dgst, foreign),
		})

		_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, trusted))
		var noKey *sigverify.NoTrustedKeyError
		if !errors.As(err, &noKey) {
			t.Fatalf("Verify error = %v (%T), want *NoTrustedKeyError", err, err)
		}
		if len(noKey.Tried) == 0 {
			t.Error("the verdict names no fingerprint, which FR-033 requires")
		}
	})

	t.Run("neither_layout_names_both_routes", func(t *testing.T) {
		t.Parallel()
		st := sigtest.NewStore()
		dgst := addSubject(t, st, "coexist-none")
		kp := mustKeyPair(t, sigtest.ECDSAP256)

		_, err := sigverify.Verify(context.Background(), st, testRepo, dgst, mustKeys(t, kp))
		if !errors.Is(err, sigverify.ErrNoSignature) {
			t.Fatalf("Verify error = %v, want ErrNoSignature", err)
		}
		// An operator seeing "unsigned" must be able to tell what was looked
		// for, in both layouts.
		if !strings.Contains(err.Error(), sigTagFor(t, dgst)) {
			t.Errorf("error %q does not name the attached-signature tag", err)
		}
		if !strings.Contains(err.Error(), "referring bundle") {
			t.Errorf("error %q does not name the bundle route", err)
		}
	})
}

// bundleTagFor rebuilds the fallback referrers tag — "sha256-<hex>", with
// no ".sig" suffix, which is what distinguishes it from the attached
// signature tag.
func bundleTagFor(t *testing.T, manifestDigest string) string {
	t.Helper()
	hexPart, ok := strings.CutPrefix(manifestDigest, "sha256:")
	if !ok {
		t.Fatalf("digest %q not sha256-prefixed", manifestDigest)
	}
	return "sha256-" + hexPart
}
