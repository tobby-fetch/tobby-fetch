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

// Format-compatibility fixture: a genuine cosign attached-signature
// artifact, captured byte-for-byte from a real signing run (cosign v3.0.4,
// `cosign sign --key … --tlog-upload=false` against a local registry,
// classic attached format) and cross-checked with `cosign verify --key`
// before embedding. It pins sigverify to the on-the-wire format actually
// produced by cosign, independently of sigtest's own builder. The
// milestone-3 crucible replays the same proof against a live cosign
// end-to-end (FR-033).
//
// The subject was localhost:15381/tobby/fixture; the docker-reference
// inside the payload keeps that value on purpose — the identity is
// informative and MUST NOT be matched (relocation rewrites references,
// FR-035/ADR-0013), so verifying under a different repo name below is
// itself part of the test.
const (
	cosignFixtureRepo = "tobby/fixture"

	// cosignFixtureSubject is the signed subject manifest digest.
	cosignFixtureSubject = "sha256:f20c43161d73848408ef247f0ec7111b19fe58ffebc0cbcaa0d2c8bda4967268"

	// cosignFixturePub is the trust root (cosign.pub, ECDSA P-256).
	cosignFixturePub = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEA9oFvTJJuu0cszKBsiuqfFYN3NLV
meleaASYW5l+nqDxoUg4f8177owlgVD89pZQ9jUxP8dbibmDJpvcjBo9RQ==
-----END PUBLIC KEY-----
`

	// cosignFixtureFingerprint is sha256 over the PKIX DER of the key above.
	cosignFixtureFingerprint = "sha256:dc2bcfdb0402cd0845f59232655c3ab2a53d55849d4665579efe45c6cc753478"

	// cosignFixtureSigManifest is the signature image manifest served at
	// tag sha256-f20c…268.sig (exact bytes).
	cosignFixtureSigManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":233,"digest":"sha256:2122b6ad0794d3fdae43a4b477cf1d95d4a38d3c5f10631e199676039b876e37"},"layers":[{"mediaType":"application/vnd.dev.cosign.simplesigning.v1+json","size":245,"digest":"sha256:3a29d26a2fc848d25ccf9c2288194ca90599e70ef69087644f186e5e7331f28b","annotations":{"dev.cosignproject.cosign/signature":"MEUCIEB+pz4s+9yxG+Om/bukeAfwloKFEISUhxLvIYlP02P8AiEAz1oNkilC+cprxcrEHqN0vxM+zvP34ikHFzi8e7Sevls="}}]}`

	// cosignFixturePayload is the SimpleSigning payload blob (exact bytes,
	// digest sha256:3a29d…f28b — the signed message).
	cosignFixturePayload = `{"critical":{"identity":{"docker-reference":"localhost:15381/tobby/fixture"},"image":{"docker-manifest-digest":"sha256:f20c43161d73848408ef247f0ec7111b19fe58ffebc0cbcaa0d2c8bda4967268"},"type":"cosign container image signature"},"optional":null}`
)

// cosignFixtureStore loads the captured artifact into an in-memory store.
func cosignFixtureStore(t *testing.T) *sigtest.Store {
	t.Helper()
	payload := []byte(cosignFixturePayload)
	if got := sigtest.DigestOf(payload); got != "sha256:3a29d26a2fc848d25ccf9c2288194ca90599e70ef69087644f186e5e7331f28b" {
		t.Fatalf("fixture self-check: payload digest %s drifted from captured bytes", got)
	}
	st := sigtest.NewStore()
	st.AddBlob(cosignFixtureRepo, payload)
	tag := "sha256-" + strings.TrimPrefix(cosignFixtureSubject, "sha256:") + ".sig"
	st.AddManifest(cosignFixtureRepo, tag, "application/vnd.oci.image.manifest.v1+json", []byte(cosignFixtureSigManifest))
	return st
}

func TestVerifyGenuineCosignArtifact(t *testing.T) {
	t.Parallel()

	keys, err := sigverify.ParsePublicKeys([]byte(cosignFixturePub))
	if err != nil {
		t.Fatalf("ParsePublicKeys(cosign.pub): %v", err)
	}
	if got := keys.Fingerprints()[0]; got != cosignFixtureFingerprint {
		t.Fatalf("cosign.pub fingerprint = %s, want %s (fingerprint scheme drifted)", got, cosignFixtureFingerprint)
	}

	fp, err := sigverify.Verify(context.Background(), cosignFixtureStore(t), cosignFixtureRepo, cosignFixtureSubject, keys)
	if err != nil {
		t.Fatalf("Verify(genuine cosign artifact): %v", err)
	}
	if fp != cosignFixtureFingerprint {
		t.Errorf("Verify fingerprint = %s, want %s", fp, cosignFixtureFingerprint)
	}
}

// TestVerifyGenuineCosignArtifactWrongKey proves the genuine artifact is
// still rejected under a trust root that did not sign it.
func TestVerifyGenuineCosignArtifactWrongKey(t *testing.T) {
	t.Parallel()

	other := mustKeyPair(t, sigtest.ECDSAP256)
	keys := mustKeys(t, other)

	_, err := sigverify.Verify(context.Background(), cosignFixtureStore(t), cosignFixtureRepo, cosignFixtureSubject, keys)
	var ntk *sigverify.NoTrustedKeyError
	if !errors.As(err, &ntk) {
		t.Fatalf("Verify error = %v, want *NoTrustedKeyError", err)
	}
	if len(ntk.Tried) != 1 || ntk.Tried[0] != mustFingerprint(t, other) {
		t.Errorf("Tried = %v, want the foreign key's fingerprint", ntk.Tried)
	}
}

// TestSimpleSigningPayloadMatchesCosign proves sigtest reproduces the
// cosign payload byte-for-byte: same field order, same compact encoding,
// same "optional":null.
func TestSimpleSigningPayloadMatchesCosign(t *testing.T) {
	t.Parallel()

	got, err := sigtest.SimpleSigningPayload("localhost:15381/tobby/fixture", cosignFixtureSubject)
	if err != nil {
		t.Fatalf("SimpleSigningPayload: %v", err)
	}
	if string(got) != cosignFixturePayload {
		t.Errorf("sigtest payload diverges from genuine cosign payload:\n got: %s\nwant: %s", got, cosignFixturePayload)
	}
}

// TestPublishSignatureGoldenStructure pins sigtest's manifest builder to
// the documented schema byte-for-byte (deterministic inputs: the genuine
// payload bytes and a fixed signature string).
func TestPublishSignatureGoldenStructure(t *testing.T) {
	t.Parallel()

	st := sigtest.NewStore()
	if _, err := sigtest.PublishSignature(st, cosignFixtureRepo, cosignFixtureSubject,
		sigtest.Layer{Payload: []byte(cosignFixturePayload), SignatureB64: "c2lnbmF0dXJl"}); err != nil {
		t.Fatalf("PublishSignature: %v", err)
	}

	tag := "sha256-" + strings.TrimPrefix(cosignFixtureSubject, "sha256:") + ".sig"
	raw, mediaType, _, err := st.Manifest(context.Background(), cosignFixtureRepo, tag)
	if err != nil {
		t.Fatalf("Manifest(%s): %v", tag, err)
	}
	if want := "application/vnd.oci.image.manifest.v1+json"; mediaType != want {
		t.Errorf("mediaType = %q, want %q", mediaType, want)
	}

	// sha256("{}") — the sigtest config blob.
	const golden = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},` +
		`"layers":[{"mediaType":"application/vnd.dev.cosign.simplesigning.v1+json","digest":"sha256:3a29d26a2fc848d25ccf9c2288194ca90599e70ef69087644f186e5e7331f28b","size":245,` +
		`"annotations":{"dev.cosignproject.cosign/signature":"c2lnbmF0dXJl"}}]}`
	if string(raw) != golden {
		t.Errorf("sigtest signature manifest diverges from documented schema:\n got: %s\nwant: %s", raw, golden)
	}
}
