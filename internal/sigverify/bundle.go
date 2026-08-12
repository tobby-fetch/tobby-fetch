// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sigstore bundle format (cosign 3.x default). Where the classic layout
// attaches a SimpleSigning payload under a "sha256-<hex>.sig" tag, the
// bundle layout publishes an OCI artifact that REFERS to the subject
// manifest: artifactType is the bundle media type, the single layer is
// the bundle JSON, and the manifest carries a "subject" descriptor.
//
// Such an artifact is discovered two ways, and this file tries both
// because registries differ: the OCI 1.1 Referrers API when the source
// can enumerate it, and the fallback tag "sha256-<hex>" — an index of
// referring manifests — which clients create when the API is absent.
// Tobby's own embedded registry has no Referrers API today, so a store
// that travelled carries the fallback tag; a public registry usually
// carries the API and nothing else.
//
// What is verified: the DSSE envelope's signature, computed over the
// pre-authentication encoding of (payloadType, payload), and the in-toto
// statement inside that payload, whose subject digest must be the digest
// being verified. Both conditions are required — a valid signature over
// a statement about ANOTHER artifact proves nothing about this one.

const (
	// mediaTypeBundlePrefix matches every published bundle revision
	// (v0.1 through v0.3) — the envelope inside is what the verifier
	// reads, and it has been stable across them.
	mediaTypeBundlePrefix = "application/vnd.dev.sigstore.bundle"
	// payloadTypeInToto is the DSSE payload type cosign uses for
	// container and artifact signatures.
	payloadTypeInToto = "application/vnd.in-toto+json"
	// statementType is the in-toto Statement version cosign emits.
	statementType = "https://in-toto.io/Statement/v1"
	// dsseV1 prefixes the DSSE pre-authentication encoding.
	dsseV1 = "DSSEv1"
)

// ReferrersLister is implemented by sources that can enumerate the
// manifests referring to a subject (OCI 1.1 Referrers API). It is
// OPTIONAL: a source that cannot enumerate still works, because the
// verifier also reads the fallback referrers tag.
type ReferrersLister interface {
	// Referrers returns the digests of the manifests referring to
	// repo@subjectDigest. An empty result is not an error.
	Referrers(ctx context.Context, repo, subjectDigest string) ([]string, error)
}

// referringManifest is the subset of a referring artifact manifest the
// verifier reads.
type referringManifest struct {
	ArtifactType string          `json:"artifactType"`
	Layers       []sigDescriptor `json:"layers"`
	Subject      *sigDescriptor  `json:"subject"`
}

// referrersIndex is the fallback tag's content: an index listing the
// manifests that refer to the subject.
type referrersIndex struct {
	Manifests []sigDescriptor `json:"manifests"`
}

// sigstoreBundle is the subset of a Sigstore bundle the verifier reads.
// Only the key-based paths are read: keyless material (certificate
// chains, Fulcio identities) is out of scope by ADR-0007, and a bundle
// carrying only those is reported as unverifiable rather than ignored.
type sigstoreBundle struct {
	MediaType            string `json:"mediaType"`
	VerificationMaterial struct {
		PublicKey *struct {
			Hint string `json:"hint"`
		} `json:"publicKey"`
		Certificate          *json.RawMessage `json:"certificate"`
		X509CertificateChain *json.RawMessage `json:"x509CertificateChain"`
	} `json:"verificationMaterial"`
	DSSEEnvelope     *dsseEnvelope `json:"dsseEnvelope"`
	MessageSignature *struct {
		MessageDigest struct {
			Algorithm string `json:"algorithm"`
			Digest    string `json:"digest"`
		} `json:"messageDigest"`
		Signature string `json:"signature"`
	} `json:"messageSignature"`
}

// dsseEnvelope is the signed envelope (DSSE, "Dead Simple Signing
// Envelope"): the signature covers the pre-authentication encoding of
// the payload, never the payload alone.
type dsseEnvelope struct {
	Payload     string `json:"payload"`
	PayloadType string `json:"payloadType"`
	Signatures  []struct {
		Sig string `json:"sig"`
	} `json:"signatures"`
}

// inTotoStatement is the subset of the in-toto Statement that binds the
// signature to a subject.
type inTotoStatement struct {
	Type    string `json:"_type"`
	Subject []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
}

// verifyBundles looks for bundle-format signatures of repo@subject and
// returns the fingerprint of the key that verified one. It reports two
// distinct facts the caller needs to answer honestly:
//
//   - found: some candidate artifact exists, so the subject is not simply
//     unsigned in this layout;
//   - verifiable: at least one well-formed bundle pinning this digest
//     exists and only the trust roots are wrong — the verdict FR-033
//     requires to name the fingerprints tried, never "malformed artifact".
func verifyBundles(ctx context.Context, src Manifests, repo, subjectDigest, subjectHex string, keys *Keys) (fingerprint string, found, verifiable bool, reasons []string, fatal error) {
	digests, err := bundleCandidates(ctx, src, repo, subjectDigest, subjectHex)
	if err != nil {
		return "", false, false, nil, err
	}
	for _, d := range digests {
		fp, ok, wellFormed, why, err := verifyOneBundle(ctx, src, repo, subjectDigest, d, keys)
		if err != nil {
			return "", found, verifiable, reasons, err
		}
		if ok {
			return fp, true, false, nil, nil
		}
		if wellFormed {
			// A signature IS published for this digest; only the trust
			// roots are wrong.
			found = true
			verifiable = true
		}
		if why != "" {
			found = true
			reasons = append(reasons, fmt.Sprintf("%s: %s", d, why))
		}
	}
	return "", found, verifiable, reasons, nil
}

// bundleCandidates collects the digests of manifests referring to the
// subject, from the Referrers API when available and from the fallback
// tag, deduplicated and order-stable.
func bundleCandidates(ctx context.Context, src Manifests, repo, subjectDigest, subjectHex string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}

	if lister, ok := src.(ReferrersLister); ok {
		digests, err := lister.Referrers(ctx, repo, subjectDigest)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("sigverify: listing referrers of %s@%s: %w", repo, subjectDigest, err)
		}
		for _, d := range digests {
			add(d)
		}
	}

	// Fallback tag: an index of referring manifests, created by clients
	// pushing to a registry without the Referrers API.
	raw, _, _, err := src.Manifest(ctx, repo, "sha256-"+subjectHex)
	switch {
	case err == nil:
		var idx referrersIndex
		if json.Unmarshal(raw, &idx) == nil {
			for _, m := range idx.Manifests {
				add(m.Digest)
			}
		}
	case errors.Is(err, ErrNotFound):
		// No fallback tag: normal on a registry serving the API.
	default:
		return nil, fmt.Errorf("sigverify: reading referrers tag of %s@%s: %w", repo, subjectDigest, err)
	}
	return out, nil
}

// verifyOneBundle verifies one candidate referring artifact. It returns
// exactly one of: ok=true with the verifying key's fingerprint;
// verifiable=true when the artifact carries a well-formed bundle pinning
// this digest that none of the configured keys signs (the caller reports
// the fingerprints tried, not a malformed artifact); a reason describing
// why this candidate does not verify; or nothing at all, meaning the
// artifact is simply not a signature bundle and was skipped.
func verifyOneBundle(ctx context.Context, src Manifests, repo, subjectDigest, manifestDigest string, keys *Keys) (fingerprint string, ok, verifiable bool, reason string, fatal error) {
	raw, _, _, err := src.Manifest(ctx, repo, manifestDigest)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, false, "", nil // listed but absent: skip
		}
		return "", false, false, "", fmt.Errorf("sigverify: reading referring manifest %s@%s: %w", repo, manifestDigest, err)
	}
	var man referringManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return "", false, false, "", nil // not a manifest we understand
	}
	if !strings.HasPrefix(man.ArtifactType, mediaTypeBundlePrefix) {
		return "", false, false, "", nil // an attestation, an SBOM…: not our business
	}
	// The referring artifact must name the subject it signs; a referrer
	// pointing elsewhere says nothing about this digest.
	if man.Subject != nil && man.Subject.Digest != "" && man.Subject.Digest != subjectDigest {
		return "", false, false, fmt.Sprintf("bundle refers to %s, not %s", man.Subject.Digest, subjectDigest), nil
	}

	for _, layer := range man.Layers {
		if !strings.HasPrefix(layer.MediaType, mediaTypeBundlePrefix) {
			continue
		}
		blob, why, err := bundleBlob(ctx, src, repo, layer)
		if err != nil {
			return "", false, false, "", err
		}
		if why != "" {
			return "", false, false, why, nil
		}
		fp, signed, why := verifyBundleBlob(blob, subjectDigest, keys)
		if signed {
			return fp, true, false, "", nil
		}
		if why != "" {
			return "", false, false, why, nil
		}
		// Well-formed, pinning this digest, signed by nobody configured:
		// a trust-root problem, which the caller must not dress up as a
		// malformed artifact.
		verifiable = true
	}
	if verifiable {
		return "", false, true, "", nil
	}
	return "", false, false, "carries no readable bundle layer", nil
}

// bundleBlob fetches and integrity-checks one bundle layer.
func bundleBlob(ctx context.Context, src Manifests, repo string, layer sigDescriptor) (blob []byte, reason string, fatal error) {
	if layer.Size > MaxPayloadSize {
		return nil, fmt.Sprintf("bundle size %d exceeds limit %d", layer.Size, MaxPayloadSize), nil
	}
	layerHex, err := parseSHA256Digest(layer.Digest)
	if err != nil {
		return nil, fmt.Sprintf("bundle descriptor digest: %v", err), nil
	}
	blob, err = src.Blob(ctx, repo, layer.Digest)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Sprintf("bundle blob %s missing", layer.Digest), nil
		}
		return nil, "", fmt.Errorf("sigverify: reading bundle %s@%s: %w", repo, layer.Digest, err)
	}
	if len(blob) > MaxPayloadSize {
		return nil, fmt.Sprintf("bundle blob %d bytes exceeds limit %d", len(blob), MaxPayloadSize), nil
	}
	sum := sha256.Sum256(blob)
	if hex.EncodeToString(sum[:]) != layerHex {
		return nil, fmt.Sprintf("bundle blob does not match descriptor digest %s", layer.Digest), nil
	}
	return blob, "", nil
}

// verifyBundleBlob verifies one bundle against the trust roots: the DSSE
// signature over the pre-authentication encoding, and the in-toto
// subject digest binding it to what is being verified.
func verifyBundleBlob(blob []byte, subjectDigest string, keys *Keys) (fingerprint string, ok bool, reason string) {
	var b sigstoreBundle
	if err := json.Unmarshal(blob, &b); err != nil {
		return "", false, fmt.Sprintf("bundle is not valid JSON: %v", err)
	}
	switch {
	case b.DSSEEnvelope == nil && b.MessageSignature != nil:
		// sign-blob style bundles carry the signature over raw message
		// bytes that are not in the registry: nothing here can bind them
		// to this manifest, so admitting them would be verifying nothing.
		return "", false, "bundle carries a messageSignature (blob signature), which does not bind a manifest digest"
	case b.DSSEEnvelope == nil:
		return "", false, "bundle carries no DSSE envelope"
	}
	if b.VerificationMaterial.PublicKey == nil &&
		(b.VerificationMaterial.Certificate != nil || b.VerificationMaterial.X509CertificateChain != nil) {
		// Keyless material: out of scope by ADR-0007 (verification must
		// work offline against configured keys), reported rather than
		// silently skipped.
		return "", false, "bundle is keyless (certificate-based); this build verifies key-based signatures only"
	}

	env := b.DSSEEnvelope
	if env.PayloadType != payloadTypeInToto {
		return "", false, fmt.Sprintf("payloadType %q, want %q", env.PayloadType, payloadTypeInToto)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return "", false, fmt.Sprintf("envelope payload is not valid base64: %v", err)
	}
	if len(payload) > MaxPayloadSize {
		return "", false, fmt.Sprintf("envelope payload %d bytes exceeds limit %d", len(payload), MaxPayloadSize)
	}

	// Bind the statement to the digest under verification BEFORE trusting
	// anything: a perfectly signed statement about another artifact must
	// not admit this one.
	var st inTotoStatement
	if err := json.Unmarshal(payload, &st); err != nil {
		return "", false, fmt.Sprintf("envelope payload is not an in-toto statement: %v", err)
	}
	if st.Type != statementType {
		return "", false, fmt.Sprintf("statement _type %q, want %q", st.Type, statementType)
	}
	wantHex, err := parseSHA256Digest(subjectDigest)
	if err != nil {
		return "", false, fmt.Sprintf("subject digest: %v", err)
	}
	pinned := false
	for _, s := range st.Subject {
		if strings.EqualFold(s.Digest["sha256"], wantHex) {
			pinned = true
			break
		}
	}
	if !pinned {
		return "", false, fmt.Sprintf("statement does not name subject sha256:%s", wantHex)
	}

	pae := preAuthEncoding(env.PayloadType, payload)
	tried := false
	for _, s := range env.Signatures {
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			continue
		}
		tried = true
		for _, key := range keys.keys {
			if key.verify(pae, sig) {
				return key.fingerprint, true, ""
			}
		}
	}
	if !tried {
		return "", false, "envelope carries no decodable signature"
	}
	// Well-formed and correctly bound, but signed by nobody configured. No
	// reason is returned: the caller turns this into "no trusted key",
	// which names the fingerprints tried (FR-033).
	return "", false, ""
}

// preAuthEncoding builds the DSSE pre-authentication encoding — what the
// signature actually covers:
//
//	"DSSEv1" SP len(payloadType) SP payloadType SP len(payload) SP payload
func preAuthEncoding(payloadType string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString(dsseV1)
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(len(payloadType)))
	b.WriteByte(' ')
	b.WriteString(payloadType)
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(len(payload)))
	b.WriteByte(' ')
	b.Write(payload)
	return b.Bytes()
}
