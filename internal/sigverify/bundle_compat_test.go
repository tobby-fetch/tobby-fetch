// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify_test

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
)

// Format-compatibility fixture for the Sigstore bundle layout — the DEFAULT
// output of cosign 3.x. Captured byte-for-byte from a real signing run
// (cosign v3.0.4 against a local registry) and cross-checked with
// `cosign verify --key` before capture; the four files under testdata/ are
// exactly what the registry served.
//
// Where cosign_compat_test.go pins the classic attached layout, this pins
// the bundle layout: an OCI artifact that REFERS to the subject, carrying a
// DSSE envelope whose in-toto statement names the signed digest. Together
// they prove sigverify reads both on-the-wire formats independently of
// sigtest's own builders. The milestone-3 crucible replays the same proof
// against a live cosign end to end (FR-033).
//
// The signing run used repository lab/subject; the tests below deliberately
// mount the artifact under other names too, because relocation rewrites
// repositories (FR-035, ADR-0013) and only the digest is authoritative.
const (
	// bundleFixtureRepo is the repository the artifact was signed under.
	bundleFixtureRepo = "lab/subject"

	// bundleFixtureSubject is the signed subject: an OCI image index.
	bundleFixtureSubject = "sha256:d7d5640c741c60b47fa4b5530daa20a6469b55754a89dd42adbadadb59f9093c"

	// bundleFixtureManifestDigest is the digest of the referring artifact
	// manifest, as recorded by the registry in the fallback referrers index.
	bundleFixtureManifestDigest = "sha256:aa34dbe1db5336a0a85e4cc7eced2f5dc91f0062322972fe29c4002372a7adff"

	// bundleFixtureBlobDigest is the digest of the bundle document layer.
	bundleFixtureBlobDigest = "sha256:adbf6372367b1d18204f9a60675cdd31c26e60b5cb664b937a69c448cfab6512"

	// bundleFixtureFingerprint is sha256 over the PKIX DER of
	// testdata/bundle-cosign.pub (ECDSA P-256).
	bundleFixtureFingerprint = "sha256:f7c9fdc1eec55a06368813da4eb5002a8ae45d976e64b72c448685e9cf358d07"

	// bundleFixtureHint is the verificationMaterial.publicKey.hint cosign
	// wrote into the captured bundle — base64 of the same SHA-256 over the
	// same DER, which is what makes it comparable to the fingerprint above.
	bundleFixtureHint = "98n9we7FWgY2iBPaTrUAKorkXZduZLcsRIaF6c81jQc="

	mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex    = "application/vnd.oci.image.index.v1+json"
)

// bundleFallbackTag is the referrers tag a registry without the OCI 1.1
// Referrers API serves for the subject: "sha256-<hex>", no ".sig" suffix.
var bundleFallbackTag = "sha256-" + strings.TrimPrefix(bundleFixtureSubject, "sha256:")

// bundleFixtureFS carries the captured files into the test binary, so the
// fixture bytes cannot drift with the working directory.
//
//go:embed testdata/bundle-cosign.pub testdata/bundle-referrers-index.json
//go:embed testdata/bundle-referring-manifest.json testdata/bundle-sigstore.json
var bundleFixtureFS embed.FS

// bundleFixtureFile reads one captured file from testdata/.
func bundleFixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := bundleFixtureFS.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// bundleFixtureKeys parses the captured cosign.pub trust root and checks it
// fingerprints the way cosign itself hinted at in the bundle.
func bundleFixtureKeys(t *testing.T) *sigverify.Keys {
	t.Helper()
	keys, err := sigverify.ParsePublicKeys(bundleFixtureFile(t, "bundle-cosign.pub"))
	if err != nil {
		t.Fatalf("ParsePublicKeys(bundle-cosign.pub): %v", err)
	}
	if got := keys.Fingerprints()[0]; got != bundleFixtureFingerprint {
		t.Fatalf("cosign.pub fingerprint = %s, want %s (fingerprint scheme drifted)", got, bundleFixtureFingerprint)
	}
	return keys
}

// bundleFixtureStore loads the captured artifact into an in-memory store
// under repo, with the fallback referrers tag but no Referrers API — the
// shape of a registry that predates OCI 1.1, and of Tobby's own embedded
// store. Every captured blob is checked against the digest the registry
// recorded, so a byte of drift in testdata/ fails loudly here instead of
// surfacing later as an unexplained verification failure.
func bundleFixtureStore(t *testing.T, repo string) *sigtest.Store {
	t.Helper()
	st := sigtest.NewStore()

	blob := bundleFixtureFile(t, "bundle-sigstore.json")
	if got := sigtest.DigestOf(blob); got != bundleFixtureBlobDigest {
		t.Fatalf("fixture self-check: bundle blob digest %s, want %s", got, bundleFixtureBlobDigest)
	}
	st.AddBlob(repo, blob)

	man := bundleFixtureFile(t, "bundle-referring-manifest.json")
	if got := sigtest.DigestOf(man); got != bundleFixtureManifestDigest {
		t.Fatalf("fixture self-check: referring manifest digest %s, want %s", got, bundleFixtureManifestDigest)
	}
	st.AddManifest(repo, "", mediaTypeOCIManifest, man)

	st.AddManifest(repo, bundleFallbackTag, mediaTypeOCIIndex, bundleFixtureFile(t, "bundle-referrers-index.json"))
	return st
}

// capturedEnvelope is the part of the captured bundle the forgery test
// re-emits.
type capturedEnvelope struct {
	Payload     string
	PayloadType string
	Signature   []byte
}

// bundleFixtureEnvelope extracts the captured DSSE envelope.
func bundleFixtureEnvelope(t *testing.T) capturedEnvelope {
	t.Helper()
	var doc struct {
		DSSEEnvelope struct {
			Payload     string `json:"payload"`
			PayloadType string `json:"payloadType"`
			Signatures  []struct {
				Sig string `json:"sig"`
			} `json:"signatures"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(bundleFixtureFile(t, "bundle-sigstore.json"), &doc); err != nil {
		t.Fatalf("parsing captured bundle: %v", err)
	}
	if len(doc.DSSEEnvelope.Signatures) != 1 {
		t.Fatalf("captured bundle has %d signatures, want 1", len(doc.DSSEEnvelope.Signatures))
	}
	sig, err := base64.StdEncoding.DecodeString(doc.DSSEEnvelope.Signatures[0].Sig)
	if err != nil {
		t.Fatalf("decoding captured signature: %v", err)
	}
	return capturedEnvelope{
		Payload:     doc.DSSEEnvelope.Payload,
		PayloadType: doc.DSSEEnvelope.PayloadType,
		Signature:   sig,
	}
}

// TestVerifyGenuineSigstoreBundle verifies the captured artifact discovered
// through the fallback referrers tag: no Referrers API, exactly what an
// offline store carries after transport (FR-052).
func TestVerifyGenuineSigstoreBundle(t *testing.T) {
	t.Parallel()

	st := bundleFixtureStore(t, bundleFixtureRepo)
	fp, err := sigverify.Verify(context.Background(), st, bundleFixtureRepo, bundleFixtureSubject, bundleFixtureKeys(t))
	if err != nil {
		t.Fatalf("Verify(genuine cosign bundle): %v", err)
	}
	if fp != bundleFixtureFingerprint {
		t.Errorf("Verify fingerprint = %s, want %s", fp, bundleFixtureFingerprint)
	}
}

// TestVerifyGenuineSigstoreBundleViaReferrersAPI verifies the same artifact
// on a registry that enumerates referrers (OCI 1.1) and serves NO fallback
// tag — GHCR, Harbor, and every modern registry. Neither discovery path may
// be load-bearing on its own.
func TestVerifyGenuineSigstoreBundleViaReferrersAPI(t *testing.T) {
	t.Parallel()

	src := &referrersAPIStore{
		Store:     bundleFixtureStore(t, bundleFixtureRepo),
		hiddenTag: bundleFallbackTag,
		referrers: []string{bundleFixtureManifestDigest},
	}
	// Guard the premise: with the tag hidden and the API ignored there would
	// be nothing left to find.
	if _, _, _, err := src.Manifest(context.Background(), bundleFixtureRepo, bundleFallbackTag); !errors.Is(err, sigverify.ErrNotFound) {
		t.Fatalf("fallback tag error = %v, want ErrNotFound (the API must be the only route)", err)
	}

	fp, err := sigverify.Verify(context.Background(), src, bundleFixtureRepo, bundleFixtureSubject, bundleFixtureKeys(t))
	if err != nil {
		t.Fatalf("Verify(via Referrers API): %v", err)
	}
	if fp != bundleFixtureFingerprint {
		t.Errorf("Verify fingerprint = %s, want %s", fp, bundleFixtureFingerprint)
	}
}

// TestVerifyGenuineSigstoreBundleRelocated verifies the captured artifact
// under a repository name that is NOT the one it was signed under. The
// in-toto statement names a digest, never a repository, so relocation
// (FR-035, ADR-0013) must not break verification.
func TestVerifyGenuineSigstoreBundleRelocated(t *testing.T) {
	t.Parallel()

	const relocated = "zone-b/mirror/relocated-subject"
	st := bundleFixtureStore(t, relocated)
	fp, err := sigverify.Verify(context.Background(), st, relocated, bundleFixtureSubject, bundleFixtureKeys(t))
	if err != nil {
		t.Fatalf("Verify(relocated to %s): %v", relocated, err)
	}
	if fp != bundleFixtureFingerprint {
		t.Errorf("Verify fingerprint = %s, want %s", fp, bundleFixtureFingerprint)
	}
}

// TestVerifyGenuineSigstoreBundleWrongKey proves the genuine artifact is
// still rejected under a trust root that did not sign it, and that the
// rejection names the fingerprint tried (FR-033 acceptance).
func TestVerifyGenuineSigstoreBundleWrongKey(t *testing.T) {
	t.Parallel()

	foreign := mustKeyPair(t, sigtest.ECDSAP256)
	st := bundleFixtureStore(t, bundleFixtureRepo)

	_, err := sigverify.Verify(context.Background(), st, bundleFixtureRepo, bundleFixtureSubject, mustKeys(t, foreign))
	var ntk *sigverify.NoTrustedKeyError
	if !errors.As(err, &ntk) {
		t.Fatalf("Verify error = %v (%T), want *NoTrustedKeyError", err, err)
	}
	if want := mustFingerprint(t, foreign); !slices.Equal(ntk.Tried, []string{want}) {
		t.Errorf("Tried = %v, want [%s]", ntk.Tried, want)
	}
}

// TestVerifyGenuineSigstoreBundleTampered alters the captured bundle blob in
// place — the descriptor still claims the original digest — and requires a
// clean, explicit refusal for every mutation. Garbage in the store must
// never panic the verifier: after physical transport the store is untrusted
// input (FR-052).
func TestVerifyGenuineSigstoreBundleTampered(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(captured []byte) []byte
	}{
		{
			name: "one_byte_flipped",
			mutate: func(captured []byte) []byte {
				c := slices.Clone(captured)
				c[len(c)/2] ^= 0x01
				return c
			},
		},
		{
			name:   "truncated",
			mutate: func(captured []byte) []byte { return slices.Clone(captured[:len(captured)/2]) },
		},
		{
			name:   "emptied",
			mutate: func([]byte) []byte { return nil },
		},
		{
			name:   "replaced_by_garbage",
			mutate: func([]byte) []byte { return []byte("\x00\xff not a bundle at all") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := bundleFixtureStore(t, bundleFixtureRepo)
			st.PutBlob(bundleFixtureRepo, bundleFixtureBlobDigest, tc.mutate(bundleFixtureFile(t, "bundle-sigstore.json")))

			_, err := sigverify.Verify(context.Background(), st, bundleFixtureRepo, bundleFixtureSubject, bundleFixtureKeys(t))
			var bad *sigverify.BadSignatureError
			if !errors.As(err, &bad) {
				t.Fatalf("Verify error = %v (%T), want *BadSignatureError", err, err)
			}
			// The descriptor digest is the first line of defence, so it is
			// the reason every mutation must produce.
			if !strings.Contains(bad.Reason, "does not match descriptor digest") {
				t.Errorf("Reason %q does not name the descriptor digest mismatch", bad.Reason)
			}
		})
	}
}

// TestVerifyGenuineSigstoreBundleForgedSignature republishes the captured
// envelope with one signature byte flipped and every descriptor recomputed,
// so the artifact is internally coherent and only the cryptography is
// wrong. This is the forgery a verifier that checks structure but not
// signatures would admit.
func TestVerifyGenuineSigstoreBundleForgedSignature(t *testing.T) {
	t.Parallel()

	env := bundleFixtureEnvelope(t)
	forgedSig := slices.Clone(env.Signature)
	forgedSig[len(forgedSig)-1] ^= 0x01

	blob, err := sigtest.DSSEBundle(sigtest.BundleEnvelope{
		PayloadB64:   env.Payload, // the genuine statement, still pinning the subject
		PayloadType:  env.PayloadType,
		SignatureB64: base64.StdEncoding.EncodeToString(forgedSig),
	})
	if err != nil {
		t.Fatalf("DSSEBundle: %v", err)
	}

	st := sigtest.NewStore()
	if _, err := sigtest.PublishBundle(st, bundleFixtureRepo, bundleFixtureSubject, sigtest.BundleArtifact{Blob: blob}); err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}

	_, err = sigverify.Verify(context.Background(), st, bundleFixtureRepo, bundleFixtureSubject, bundleFixtureKeys(t))
	var ntk *sigverify.NoTrustedKeyError
	if !errors.As(err, &ntk) {
		t.Fatalf("Verify(forged signature) error = %v (%T), want *NoTrustedKeyError", err, err)
	}
}

// TestInTotoStatementMatchesCosign proves sigtest reproduces the statement
// cosign signs byte for byte — same field order, same compact encoding,
// same empty annotations object. Without it, every fabricated bundle
// fixture would only prove sigverify agrees with sigtest.
func TestInTotoStatementMatchesCosign(t *testing.T) {
	t.Parallel()

	env := bundleFixtureEnvelope(t)
	want, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatalf("decoding captured payload: %v", err)
	}

	got, err := sigtest.InTotoStatement(bundleFixtureSubject)
	if err != nil {
		t.Fatalf("InTotoStatement: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("sigtest statement diverges from the genuine cosign statement:\n got: %s\nwant: %s", got, want)
	}
	if env.PayloadType != sigtest.PayloadTypeInToto {
		t.Errorf("captured payloadType = %q, want %q", env.PayloadType, sigtest.PayloadTypeInToto)
	}
}

// TestBundleHintMatchesFingerprint records why the two identifiers agree:
// cosign's verificationMaterial.publicKey.hint is base64 of SHA-256 over
// the PKIX DER, which is exactly how sigverify fingerprints a trust root.
// The verifier does not read the hint — it tries every configured key — but
// an operator reading a bundle by hand needs the two to line up.
func TestBundleHintMatchesFingerprint(t *testing.T) {
	t.Parallel()

	raw, err := base64.StdEncoding.DecodeString(bundleFixtureHint)
	if err != nil {
		t.Fatalf("decoding captured hint: %v", err)
	}
	if got := "sha256:" + hex.EncodeToString(raw); got != bundleFixtureFingerprint {
		t.Errorf("hint = %s, want the trust-root fingerprint %s", got, bundleFixtureFingerprint)
	}
}
