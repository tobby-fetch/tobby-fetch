// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// MediaTypeSimpleSigning is the media type of cosign signature payload
	// layers (the SimpleSigning JSON document).
	MediaTypeSimpleSigning = "application/vnd.dev.cosign.simplesigning.v1+json"

	// AnnotationSignature is the layer-descriptor annotation carrying the
	// base64-encoded signature over the raw payload blob bytes.
	AnnotationSignature = "dev.cosignproject.cosign/signature"

	// MaxPayloadSize bounds signature payload blobs. SimpleSigning JSON
	// documents are a few hundred bytes; anything above 1 MiB is hostile
	// or corrupt and is refused before (and after) fetching.
	MaxPayloadSize = 1 << 20

	// payloadType is the required critical.type of a SimpleSigning image
	// signature payload.
	payloadType = "cosign container image signature"

	sha256Prefix = "sha256:"
	hexDigestLen = sha256.Size * 2
)

// ErrNotFound is the sentinel that Manifests implementations must return
// (wrapped or not) when a repository, reference, or blob does not exist,
// so the verifier can distinguish "no signature published" (ErrNoSignature)
// from transport failures.
var ErrNotFound = errors.New("sigverify: reference not found")

// ErrNoSignature reports that the subject has no attached signature: the
// cosign ".sig" tag does not resolve in the subject's repository. The
// caller maps it to the policy taxonomy (unsigned content is blocked by
// default, FR-033).
var ErrNoSignature = errors.New("sigverify: no signature found for subject")

// NoTrustedKeyError reports that a structurally valid signature pinning
// the right digest exists, but none of the configured trust roots verifies
// it. Tried lists the fingerprints of every key tried, as required by the
// FR-033 acceptance ("rejected with the key fingerprint(s) tried").
type NoTrustedKeyError struct {
	Tried []string
}

// Error implements error.
func (e *NoTrustedKeyError) Error() string {
	return fmt.Sprintf("sigverify: signature does not verify with any configured trust root (keys tried: %s)",
		strings.Join(e.Tried, ", "))
}

// BadSignatureError reports a malformed or non-matching signature
// artifact: unparseable manifest, missing signature annotation, payload
// digest mismatch, payload pinning a different subject digest, oversized
// payload, and similar. Fail closed: such artifacts block the subject
// (RECIPE-SPEC §12.3).
type BadSignatureError struct {
	Reason string
}

// Error implements error.
func (e *BadSignatureError) Error() string {
	return "sigverify: invalid signature artifact: " + e.Reason
}

// Manifests is the read surface sigverify needs — implemented elsewhere by
// the remote registry client and by the embedded store (the verifier is
// reusable offline on an opened store: FR-052 replays the same checks
// destination-side). Implementations return ErrNotFound (wrapped is fine)
// for unknown references and blobs.
type Manifests interface {
	// Manifest returns the raw manifest bytes and descriptor info of
	// repo@reference (reference = tag or "sha256:…" digest).
	Manifest(ctx context.Context, repo, reference string) (payload []byte, mediaType string, dgst string, err error)

	// Blob returns the raw bytes of a blob of repo. Signature payloads are
	// small (SimpleSigning JSON) — implementations perform a bounded read
	// and refuse blobs larger than MaxPayloadSize.
	Blob(ctx context.Context, repo, dgst string) ([]byte, error)
}

// sigManifest is the subset of the cosign signature image manifest the
// verifier reads. cosign emits OCI image manifests (older releases emitted
// Docker schema 2); the manifest media type is deliberately not enforced —
// only the layer list matters.
type sigManifest struct {
	Layers []sigDescriptor `json:"layers"`
}

// sigDescriptor is the subset of an OCI descriptor the verifier reads.
type sigDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

// simpleSigning is the SimpleSigning payload (containers/image simple
// signing spec, as emitted by cosign for container image signatures).
type simpleSigning struct {
	Critical struct {
		Identity struct {
			// DockerReference is the identity the signer claims for the
			// subject. It is deliberately NOT matched against the local
			// reference: relocation rewrites repositories per RECIPE-SPEC
			// §11.5 (FR-035, ADR-0013), so the nominal reference the
			// signer saw and the reference Tobby verifies legitimately
			// differ. The pinned digest below is the sole source of truth.
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// Verify checks the cosign signature of repo@manifestDigest against keys,
// in either published layout, and returns the fingerprint of the key that
// verified:
//
//   - the classic attached signature at "sha256-<hex>.sig" in the same
//     repository (RECIPE-SPEC §12.2), a SimpleSigning payload pinning the
//     subject digest;
//   - the Sigstore bundle layout (cosign 3.x default), an artifact
//     REFERRING to the subject, whose DSSE envelope carries an in-toto
//     statement naming it (bundle.go).
//
// Both are tried: publishers pick a format, and consumers should not have
// to. The subject is admitted as soon as one signature verifies against
// one trusted key; nothing else changes — an unverifiable signature still
// fails closed, per item.
//
// Failure modes: ErrNoSignature when NEITHER layout publishes anything,
// *NoTrustedKeyError when a well-formed signature exists but no configured
// key verifies it, *BadSignatureError when an artifact is malformed or
// names a different digest. Transport errors pass through unchanged.
func Verify(ctx context.Context, src Manifests, repo, manifestDigest string, keys *Keys) (fingerprint string, err error) {
	if keys == nil || len(keys.keys) == 0 {
		return "", errors.New("sigverify: no trust roots configured")
	}
	subjectHex, err := parseSHA256Digest(manifestDigest)
	if err != nil {
		return "", fmt.Errorf("sigverify: subject digest: %w", err)
	}
	sigTag := "sha256-" + subjectHex + ".sig"

	// Classic layout first: it is what RECIPE-SPEC §12.2 pins, so it is
	// the common case for content produced by the documented toolchain.
	raw, _, _, legacyErr := src.Manifest(ctx, repo, sigTag)
	if legacyErr != nil && !errors.Is(legacyErr, ErrNotFound) {
		return "", fmt.Errorf("sigverify: reading signature manifest %s:%s: %w", repo, sigTag, legacyErr)
	}
	if legacyErr != nil {
		// No classic signature: the bundle layout is the remaining
		// possibility, and its absence is what makes the subject unsigned.
		fp, found, verifiable, reasons, err := verifyBundles(ctx, src, repo, manifestDigest, subjectHex, keys)
		switch {
		case err != nil:
			return "", err
		case fp != "":
			return fp, nil
		case !found:
			return "", fmt.Errorf("%w: %s@%s (neither %q nor a referring bundle resolves)",
				ErrNoSignature, repo, manifestDigest, sigTag)
		case verifiable:
			// A well-formed signature exists and no configured key matches:
			// that verdict, with the fingerprints, outranks whatever other
			// candidates were malformed (FR-033 acceptance).
			return "", &NoTrustedKeyError{Tried: keys.Fingerprints()}
		default:
			return "", &BadSignatureError{Reason: strings.Join(reasons, "; ")}
		}
	}

	var manifest sigManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", &BadSignatureError{Reason: fmt.Sprintf("signature manifest %s:%s is not valid JSON: %v", repo, sigTag, err)}
	}

	// Try every payload layer; each verifiable payload is tried against
	// every trusted key (rotation by overlap, RECIPE-SPEC §12.3).
	var reasons []string
	sawVerifiable := false // ≥1 well-formed layer pinning the right digest
	candidates := 0
	for i, layer := range manifest.Layers {
		if layer.MediaType != MediaTypeSimpleSigning {
			continue
		}
		candidates++

		blob, reason, fatal := signaturePayload(ctx, src, repo, layer)
		if fatal != nil {
			return "", fatal
		}
		if reason != "" {
			reasons = append(reasons, fmt.Sprintf("layer %d: %s", i, reason))
			continue
		}
		var payload simpleSigning
		if err := json.Unmarshal(blob, &payload); err != nil {
			reasons = append(reasons, fmt.Sprintf("layer %d: payload is not valid SimpleSigning JSON: %v", i, err))
			continue
		}
		if payload.Critical.Type != payloadType {
			reasons = append(reasons, fmt.Sprintf("layer %d: payload critical.type %q, want %q", i, payload.Critical.Type, payloadType))
			continue
		}
		if payload.Critical.Image.DockerManifestDigest != manifestDigest {
			reasons = append(reasons, fmt.Sprintf("layer %d: payload pins digest %q, want %q",
				i, payload.Critical.Image.DockerManifestDigest, manifestDigest))
			continue
		}

		sig, decErr := base64.StdEncoding.DecodeString(layer.Annotations[AnnotationSignature])
		if decErr != nil {
			reasons = append(reasons, fmt.Sprintf("layer %d: signature annotation is not valid base64: %v", i, decErr))
			continue
		}
		// The signature covers the exact raw payload bytes, not any
		// re-serialization of them.
		sawVerifiable = true
		for _, key := range keys.keys {
			if key.verify(blob, sig) {
				return key.fingerprint, nil
			}
		}
	}

	// The classic artifact exists but did not verify. A publisher may have
	// re-signed in the bundle layout since — try it before failing, so a
	// stale .sig alongside a fresh bundle does not mask a valid signature.
	fp, _, bundleVerifiable, bundleReasons, err := verifyBundles(ctx, src, repo, manifestDigest, subjectHex, keys)
	if err != nil {
		return "", err
	}
	if fp != "" {
		return fp, nil
	}
	reasons = append(reasons, bundleReasons...)

	// Either layout having published a well-formed signature makes this a
	// trust-root verdict, not a malformed-artifact one: the operator needs
	// the fingerprints tried, whichever layout carried the signature.
	if sawVerifiable || bundleVerifiable {
		return "", &NoTrustedKeyError{Tried: keys.Fingerprints()}
	}
	if candidates == 0 {
		return "", &BadSignatureError{Reason: fmt.Sprintf("signature manifest %s:%s has no %s layer", repo, sigTag, MediaTypeSimpleSigning)}
	}
	return "", &BadSignatureError{Reason: strings.Join(reasons, "; ")}
}

// signaturePayload validates one signature layer's shape and fetches its
// payload blob. It returns exactly one of: the payload bytes, a reason
// string describing why the layer cannot verify (fail-closed but the next
// layer may still succeed), or a fatal transport error.
func signaturePayload(ctx context.Context, src Manifests, repo string, layer sigDescriptor) (blob []byte, reason string, fatal error) {
	if layer.Annotations[AnnotationSignature] == "" {
		return nil, fmt.Sprintf("missing %q annotation", AnnotationSignature), nil
	}
	if layer.Size > MaxPayloadSize {
		return nil, fmt.Sprintf("payload size %d exceeds limit %d", layer.Size, MaxPayloadSize), nil
	}
	layerHex, err := parseSHA256Digest(layer.Digest)
	if err != nil {
		return nil, fmt.Sprintf("payload descriptor digest: %v", err), nil
	}
	blob, err = src.Blob(ctx, repo, layer.Digest)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Sprintf("payload blob %s missing", layer.Digest), nil
		}
		return nil, "", fmt.Errorf("sigverify: reading signature payload %s@%s: %w", repo, layer.Digest, err)
	}
	// Defense in depth: the Manifests implementation already bounds the
	// read, but never trust it blindly (FR-052: store contents are
	// untrusted until verified).
	if len(blob) > MaxPayloadSize {
		return nil, fmt.Sprintf("payload blob %d bytes exceeds limit %d", len(blob), MaxPayloadSize), nil
	}
	sum := sha256.Sum256(blob)
	if hex.EncodeToString(sum[:]) != layerHex {
		return nil, fmt.Sprintf("payload blob does not match descriptor digest %s", layer.Digest), nil
	}
	return blob, "", nil
}

// parseSHA256Digest validates a "sha256:<64 hex>" digest string and
// returns its hex part.
func parseSHA256Digest(d string) (string, error) {
	hexPart, ok := strings.CutPrefix(d, sha256Prefix)
	if !ok {
		return "", fmt.Errorf("digest %q is not sha256-prefixed", d)
	}
	if len(hexPart) != hexDigestLen {
		return "", fmt.Errorf("digest %q: want %d hex characters", d, hexDigestLen)
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", fmt.Errorf("digest %q: %w", d, err)
	}
	return strings.ToLower(hexPart), nil
}
