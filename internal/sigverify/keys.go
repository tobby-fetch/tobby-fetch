// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"

	// crypto.SHA384 is used through the generic crypto.Hash registry for
	// ECDSA P-384 keys; the implementation must be linked in explicitly.
	_ "crypto/sha512"
)

// minRSABits is the smallest accepted RSA modulus. Shorter moduli are
// considered breakable and are rejected at parse time so a weak trust root
// is a configuration error, not a silent verification pass.
const minRSABits = 2048

// pemTypePublicKey is the PEM block type of a PKIX public key — the only
// form cosign emits (cosign.pub) and the only form accepted here.
const pemTypePublicKey = "PUBLIC KEY"

// trustedKey is one parsed trust root: its stable fingerprint plus a
// closure verifying a raw signature over raw payload bytes.
type trustedKey struct {
	fingerprint string
	verify      func(payload, sig []byte) bool
}

// Keys is the configured trust-root set. Multiple keys enable rotation by
// overlap (RECIPE-SPEC §12.3): during a rotation window both the outgoing
// and the incoming key are configured, and content signed by either
// verifies.
type Keys struct {
	keys []trustedKey
}

// ParsePublicKeys parses one or more PEM-encoded public keys (PKIX
// "PUBLIC KEY" blocks, possibly concatenated in one input — the cosign.pub
// format). Supported algorithms: ECDSA P-256/P-384 (SHA-256/SHA-384
// digest, ASN.1 DER signature — the cosign default), Ed25519 (signs the
// payload directly), RSA ≥ 2048 (PKCS#1 v1.5, SHA-256). Any input that
// contains no usable key, an unsupported key type, or a non-PKIX block is
// rejected: trust roots are configuration, and configuration errors must
// surface at load time (FR-033), not as spurious verification failures.
func ParsePublicKeys(pems ...[]byte) (*Keys, error) {
	ks := &Keys{}
	for i, input := range pems {
		found := 0
		for rest := input; ; {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type != pemTypePublicKey {
				return nil, fmt.Errorf("sigverify: key input %d: unsupported PEM block type %q (want PKIX %q)",
					i, block.Type, pemTypePublicKey)
			}
			key, err := newTrustedKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("sigverify: key input %d: %w", i, err)
			}
			ks.keys = append(ks.keys, key)
			found++
		}
		if found == 0 {
			return nil, fmt.Errorf("sigverify: key input %d: no PEM %q block found", i, pemTypePublicKey)
		}
	}
	if len(ks.keys) == 0 {
		return nil, errors.New("sigverify: no public key provided")
	}
	return ks, nil
}

// Fingerprints returns the SHA-256 PKIX-DER fingerprints (hex, "sha256:…"
// prefixed) of all keys, in configuration order. These are the identifiers
// reported when verification fails (FR-033 acceptance: the rejection names
// the key fingerprints tried).
func (k *Keys) Fingerprints() []string {
	fps := make([]string, len(k.keys))
	for i := range k.keys {
		fps[i] = k.keys[i].fingerprint
	}
	return fps
}

// Union merges key sets into one verification set — scope resolution can
// aggregate several named trust roots (RECIPE-SPEC §12.3). Keys are
// deduplicated by fingerprint, first occurrence wins; nil sets are
// skipped. The result may be empty, in which case Verify fails closed.
func Union(sets ...*Keys) *Keys {
	merged := &Keys{}
	seen := make(map[string]struct{})
	for _, s := range sets {
		if s == nil {
			continue
		}
		for _, key := range s.keys {
			if _, dup := seen[key.fingerprint]; dup {
				continue
			}
			seen[key.fingerprint] = struct{}{}
			merged.keys = append(merged.keys, key)
		}
	}
	return merged
}

// newTrustedKey parses one PKIX DER public key and builds its verifier.
func newTrustedKey(der []byte) (trustedKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return trustedKey{}, fmt.Errorf("parsing PKIX public key: %w", err)
	}

	// Fingerprint over the re-marshalled (canonical) PKIX DER, so two
	// encodings of the same key always fingerprint identically.
	canonical, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return trustedKey{}, fmt.Errorf("re-encoding public key: %w", err)
	}
	sum := sha256.Sum256(canonical)
	key := trustedKey{fingerprint: "sha256:" + hex.EncodeToString(sum[:])}

	switch pk := pub.(type) {
	case *ecdsa.PublicKey:
		var h crypto.Hash
		switch pk.Curve {
		case elliptic.P256():
			h = crypto.SHA256
		case elliptic.P384():
			h = crypto.SHA384
		default:
			return trustedKey{}, fmt.Errorf("unsupported ECDSA curve %q (want P-256 or P-384)", pk.Curve.Params().Name)
		}
		key.verify = func(payload, sig []byte) bool {
			return ecdsa.VerifyASN1(pk, hashSum(h, payload), sig)
		}
	case ed25519.PublicKey:
		key.verify = func(payload, sig []byte) bool {
			return ed25519.Verify(pk, payload, sig)
		}
	case *rsa.PublicKey:
		if pk.N.BitLen() < minRSABits {
			return trustedKey{}, fmt.Errorf("RSA key too small: %d bits (want ≥ %d)", pk.N.BitLen(), minRSABits)
		}
		key.verify = func(payload, sig []byte) bool {
			return rsa.VerifyPKCS1v15(pk, crypto.SHA256, hashSum(crypto.SHA256, payload), sig) == nil
		}
	default:
		return trustedKey{}, fmt.Errorf("unsupported public key type %T", pub)
	}
	return key, nil
}

// hashSum digests data with h. h must be one of the hashes linked above.
func hashSum(h crypto.Hash, data []byte) []byte {
	hasher := h.New()
	_, _ = hasher.Write(data) // never fails per hash.Hash contract
	return hasher.Sum(nil)
}
