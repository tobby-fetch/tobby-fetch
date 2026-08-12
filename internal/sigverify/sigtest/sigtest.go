// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package sigtest fabricates cosign attached-signature fixtures — FOR
// TESTS ONLY.
//
// TOBBY NEVER SIGNS IN PRODUCTION (project decision no. 10: Tobby is the
// pallet truck, not the notary — it moves and verifies artifacts, it does
// not author trust; see ADR-0007). This package exists solely so tests of
// internal/sigverify — and of consumers such as the transfer engine — can
// build well-formed and deliberately malformed cosign attached-signature
// artifacts (RECIPE-SPEC §12.2) without importing sigstore. It MUST only
// ever be imported from _test.go files.
//
// It provides key-pair generation for every algorithm sigverify accepts,
// raw payload signing, a builder for complete ".sig" artifacts (manifest
// plus payload blobs), and Store, an in-memory sigverify.Manifests
// implementation to plug them into.
package sigtest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
)

// Algorithm selects the key type of a generated fixture key pair. The set
// mirrors what sigverify accepts (FR-033, ADR-0007).
type Algorithm string

// Supported fixture algorithms.
const (
	ECDSAP256 Algorithm = "ecdsa-p256" // cosign default
	ECDSAP384 Algorithm = "ecdsa-p384"
	Ed25519   Algorithm = "ed25519"
	RSA2048   Algorithm = "rsa-2048"
)

// KeyPair is a fixture signing key pair.
type KeyPair struct {
	alg    Algorithm
	signer crypto.Signer
}

// GenerateKeyPair creates a fresh key pair for alg.
func GenerateKeyPair(alg Algorithm) (*KeyPair, error) {
	var (
		signer crypto.Signer
		err    error
	)
	switch alg {
	case ECDSAP256:
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case ECDSAP384:
		signer, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case Ed25519:
		_, signer, err = ed25519.GenerateKey(rand.Reader)
	case RSA2048:
		signer, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		return nil, fmt.Errorf("sigtest: unknown algorithm %q", alg)
	}
	if err != nil {
		return nil, fmt.Errorf("sigtest: generating %s key: %w", alg, err)
	}
	return &KeyPair{alg: alg, signer: signer}, nil
}

// PublicPEM returns the public key as a PKIX "PUBLIC KEY" PEM block — the
// cosign.pub format sigverify.ParsePublicKeys consumes.
func (kp *KeyPair) PublicPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(kp.signer.Public())
	if err != nil {
		return nil, fmt.Errorf("sigtest: marshalling public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// Fingerprint returns the key's sigverify fingerprint ("sha256:…" over the
// PKIX DER), for asserting which key verified.
func (kp *KeyPair) Fingerprint() (string, error) {
	der, err := x509.MarshalPKIXPublicKey(kp.signer.Public())
	if err != nil {
		return "", fmt.Errorf("sigtest: marshalling public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Sign signs the exact payload bytes the way cosign does for this key type
// (ECDSA: SHA-256/SHA-384 digest, ASN.1 DER signature; Ed25519: raw
// payload; RSA: PKCS#1 v1.5 over SHA-256) and returns the base64 form used
// in the signature annotation.
func (kp *KeyPair) Sign(payload []byte) (string, error) {
	var (
		digest []byte
		opts   crypto.SignerOpts
	)
	switch kp.alg {
	case ECDSAP256, RSA2048:
		sum := sha256.Sum256(payload)
		digest, opts = sum[:], crypto.SHA256
	case ECDSAP384:
		sum := sha512.Sum384(payload)
		digest, opts = sum[:], crypto.SHA384
	case Ed25519:
		digest, opts = payload, crypto.Hash(0)
	default:
		return "", fmt.Errorf("sigtest: unknown algorithm %q", kp.alg)
	}
	sig, err := kp.signer.Sign(rand.Reader, digest, opts)
	if err != nil {
		return "", fmt.Errorf("sigtest: signing: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// simpleSigning mirrors the payload document cosign marshals (field order
// matters for the byte-for-byte format compatibility test).
type simpleSigning struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// SimpleSigningPayload builds the SimpleSigning JSON document cosign
// produces for an image signature: dockerReference is the claimed identity
// (informative — sigverify does not match it, relocation rewrites it) and
// manifestDigest is the pinned subject digest.
func SimpleSigningPayload(dockerReference, manifestDigest string) ([]byte, error) {
	var p simpleSigning
	p.Critical.Identity.DockerReference = dockerReference
	p.Critical.Image.DockerManifestDigest = manifestDigest
	p.Critical.Type = "cosign container image signature"
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("sigtest: marshalling payload: %w", err)
	}
	return b, nil
}

// Layer describes one payload layer of a signature artifact under
// construction. The zero values exist to fabricate malformed artifacts.
type Layer struct {
	// Payload is the raw SimpleSigning JSON blob.
	Payload []byte
	// SignatureB64 is the base64 signature for the annotation. Empty means
	// the annotation is omitted entirely (malformed-artifact tests).
	SignatureB64 string
	// MediaType overrides the layer media type when non-empty (defaults to
	// sigverify.MediaTypeSimpleSigning).
	MediaType string
}

// ociDescriptor and ociManifest mirror the OCI image manifest subset
// cosign writes for signature artifacts.
type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

// mediaTypeOCIManifest is the manifest media type cosign writes.
const mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"

// PublishSignature assembles a cosign attached-signature artifact from the
// given layers and stores it in st under the cosign convention tag
// ("sha256-<hex>.sig" in repo) for manifestDigest. It returns the
// signature manifest digest.
func PublishSignature(st *Store, repo, manifestDigest string, layers ...Layer) (string, error) {
	hexPart, ok := strings.CutPrefix(manifestDigest, "sha256:")
	if !ok {
		return "", fmt.Errorf("sigtest: subject digest %q is not sha256-prefixed", manifestDigest)
	}

	configDigest := st.AddBlob(repo, []byte("{}"))
	m := ociManifest{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIManifest,
		Config: ociDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    configDigest,
			Size:      2,
		},
	}
	for _, l := range layers {
		desc := ociDescriptor{
			MediaType: l.MediaType,
			Digest:    st.AddBlob(repo, l.Payload),
			Size:      int64(len(l.Payload)),
		}
		if desc.MediaType == "" {
			desc.MediaType = sigverify.MediaTypeSimpleSigning
		}
		if l.SignatureB64 != "" {
			desc.Annotations = map[string]string{sigverify.AnnotationSignature: l.SignatureB64}
		}
		m.Layers = append(m.Layers, desc)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("sigtest: marshalling signature manifest: %w", err)
	}
	return st.AddManifest(repo, "sha256-"+hexPart+".sig", mediaTypeOCIManifest, raw), nil
}

// SignManifest is the happy-path fixture: it builds the SimpleSigning
// payload pinning manifestDigest, signs it with kp, and publishes the
// single-layer signature artifact into st.
func SignManifest(st *Store, repo, manifestDigest string, kp *KeyPair) error {
	payload, err := SimpleSigningPayload("registry.example/"+repo, manifestDigest)
	if err != nil {
		return err
	}
	sig, err := kp.Sign(payload)
	if err != nil {
		return err
	}
	_, err = PublishSignature(st, repo, manifestDigest, Layer{Payload: payload, SignatureB64: sig})
	return err
}

// Store is an in-memory sigverify.Manifests implementation backed by maps
// (digest → bytes), for tests of this package's consumers.
type Store struct {
	mu        sync.RWMutex
	manifests map[string]storedManifest // repo + "\x00" + reference
	blobs     map[string][]byte         // repo + "\x00" + digest
}

type storedManifest struct {
	payload   []byte
	mediaType string
	digest    string
}

// NewStore creates an empty Store.
func NewStore() *Store {
	return &Store{
		manifests: make(map[string]storedManifest),
		blobs:     make(map[string][]byte),
	}
}

func storeKey(repo, reference string) string { return repo + "\x00" + reference }

// DigestOf returns the "sha256:…" digest of data — the addressing scheme
// Store uses, exported so tests can locate blobs they intend to corrupt.
func DigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// AddBlob stores data as a blob of repo and returns its "sha256:…" digest.
func (s *Store) AddBlob(repo string, data []byte) string {
	dgst := DigestOf(data)
	s.PutBlob(repo, dgst, data)
	return dgst
}

// PutBlob stores data under an explicit digest WITHOUT verifying it — a
// corruption hook for tests exercising digest-mismatch handling.
func (s *Store) PutBlob(repo, dgst string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[storeKey(repo, dgst)] = data
}

// AddManifest stores payload as a manifest of repo, addressable by its
// digest and, when tag is non-empty, by tag. It returns the manifest
// digest.
func (s *Store) AddManifest(repo, tag, mediaType string, payload []byte) string {
	entry := storedManifest{payload: payload, mediaType: mediaType, digest: DigestOf(payload)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[storeKey(repo, entry.digest)] = entry
	if tag != "" {
		s.manifests[storeKey(repo, tag)] = entry
	}
	return entry.digest
}

// Manifest implements sigverify.Manifests.
func (s *Store) Manifest(_ context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.manifests[storeKey(repo, reference)]
	if !ok {
		return nil, "", "", fmt.Errorf("manifest %s@%s: %w", repo, reference, sigverify.ErrNotFound)
	}
	return entry.payload, entry.mediaType, entry.digest, nil
}

// Blob implements sigverify.Manifests, including the bounded read: blobs
// larger than sigverify.MaxPayloadSize are refused.
func (s *Store) Blob(_ context.Context, repo, dgst string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.blobs[storeKey(repo, dgst)]
	if !ok {
		return nil, fmt.Errorf("blob %s@%s: %w", repo, dgst, sigverify.ErrNotFound)
	}
	if len(data) > sigverify.MaxPayloadSize {
		return nil, fmt.Errorf("blob %s@%s: %d bytes exceeds bounded read limit %d",
			repo, dgst, len(data), sigverify.MaxPayloadSize)
	}
	return data, nil
}
