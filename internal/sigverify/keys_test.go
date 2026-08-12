// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
)

// Static negative-parse fixtures: pre-generated so tests never create weak
// key material at runtime.
const (
	// rsa1024PEM is a deliberately undersized RSA public key (< 2048 bits).
	rsa1024PEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDkw93+FhwiLm6Nx/6N70ptjjoC
Zcdb5cm9rPVAYWZ9qopmDWrgtw3g7bA4bD4GOsQ2ERe8RPoc5sJnXiIeyO84rzeR
vmgoPeDwyVWf2yfuzLudLtBtEQCFX83vopRmbHVjngn0g6zOU4e1cwQT3GuaxDIM
tVZ49zTvk8+wZIi7QwIDAQAB
-----END PUBLIC KEY-----
`
	// ecdsaP521PEM is an ECDSA key on a curve sigverify does not accept.
	ecdsaP521PEM = `-----BEGIN PUBLIC KEY-----
MIGbMBAGByqGSM49AgEGBSuBBAAjA4GGAAQBkRjFd3pb4JOYXguO3s5FwRslbBuk
XHhRsZXjmsNPPJ+n7OpmeMJGXNCKIou5gEbG5Cp3h1n8RSY28pQeIDWlXCYBbSl8
uTAKU8Hj0MKgKOgz83NO+Yu06IsA8L4+221naKkgBe5mppRjEsMj6H9d7ZzpeptY
XYVCOQNQBETdF8Vj3l0=
-----END PUBLIC KEY-----
`
)

var fingerprintRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func mustKeyPair(t *testing.T, alg sigtest.Algorithm) *sigtest.KeyPair {
	t.Helper()
	kp, err := sigtest.GenerateKeyPair(alg)
	if err != nil {
		t.Fatalf("GenerateKeyPair(%s): %v", alg, err)
	}
	return kp
}

func mustPEM(t *testing.T, kp *sigtest.KeyPair) []byte {
	t.Helper()
	p, err := kp.PublicPEM()
	if err != nil {
		t.Fatalf("PublicPEM: %v", err)
	}
	return p
}

func mustFingerprint(t *testing.T, kp *sigtest.KeyPair) string {
	t.Helper()
	fp, err := kp.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	return fp
}

func TestParsePublicKeys(t *testing.T) {
	t.Parallel()

	algs := []sigtest.Algorithm{sigtest.ECDSAP256, sigtest.ECDSAP384, sigtest.Ed25519, sigtest.RSA2048}
	for _, alg := range algs {
		t.Run("ok_"+string(alg), func(t *testing.T) {
			t.Parallel()
			kp := mustKeyPair(t, alg)
			keys, err := sigverify.ParsePublicKeys(mustPEM(t, kp))
			if err != nil {
				t.Fatalf("ParsePublicKeys: %v", err)
			}
			fps := keys.Fingerprints()
			if len(fps) != 1 {
				t.Fatalf("Fingerprints: got %d, want 1", len(fps))
			}
			if !fingerprintRe.MatchString(fps[0]) {
				t.Errorf("fingerprint %q does not match %q", fps[0], fingerprintRe)
			}
			if want := mustFingerprint(t, kp); fps[0] != want {
				t.Errorf("fingerprint %q, want %q", fps[0], want)
			}
		})
	}

	t.Run("concatenated_single_input", func(t *testing.T) {
		t.Parallel()
		a, b := mustKeyPair(t, sigtest.ECDSAP256), mustKeyPair(t, sigtest.Ed25519)
		joined := slices.Concat(mustPEM(t, a), mustPEM(t, b))
		keys, err := sigverify.ParsePublicKeys(joined)
		if err != nil {
			t.Fatalf("ParsePublicKeys: %v", err)
		}
		want := []string{mustFingerprint(t, a), mustFingerprint(t, b)}
		if got := keys.Fingerprints(); !slices.Equal(got, want) {
			t.Errorf("Fingerprints = %v, want %v (configuration order)", got, want)
		}
	})

	t.Run("multiple_inputs", func(t *testing.T) {
		t.Parallel()
		a, b := mustKeyPair(t, sigtest.ECDSAP256), mustKeyPair(t, sigtest.ECDSAP384)
		keys, err := sigverify.ParsePublicKeys(mustPEM(t, a), mustPEM(t, b))
		if err != nil {
			t.Fatalf("ParsePublicKeys: %v", err)
		}
		if got := keys.Fingerprints(); len(got) != 2 {
			t.Errorf("Fingerprints = %v, want 2 entries", got)
		}
	})

	errCases := []struct {
		name    string
		inputs  [][]byte
		wantSub string
	}{
		{"no_input", nil, "no public key"},
		{"empty_input", [][]byte{{}}, "no PEM"},
		{"not_pem", [][]byte{[]byte("definitely not a pem")}, "no PEM"},
		{"wrong_block_type", [][]byte{[]byte("-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n")}, "unsupported PEM block type"},
		{"garbage_der", [][]byte{[]byte("-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n")}, "parsing PKIX public key"},
		{"rsa_too_small", [][]byte{[]byte(rsa1024PEM)}, "RSA key too small"},
		{"unsupported_curve", [][]byte{[]byte(ecdsaP521PEM)}, "unsupported ECDSA curve"},
	}
	for _, tc := range errCases {
		t.Run("err_"+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := sigverify.ParsePublicKeys(tc.inputs...)
			if err == nil {
				t.Fatal("ParsePublicKeys: expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestUnion(t *testing.T) {
	t.Parallel()

	a, b := mustKeyPair(t, sigtest.ECDSAP256), mustKeyPair(t, sigtest.Ed25519)
	setA, err := sigverify.ParsePublicKeys(mustPEM(t, a))
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	setAB, err := sigverify.ParsePublicKeys(mustPEM(t, a), mustPEM(t, b))
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}

	merged := sigverify.Union(setA, nil, setAB)
	want := []string{mustFingerprint(t, a), mustFingerprint(t, b)}
	got := merged.Fingerprints()
	if !slices.Equal(got, want) {
		t.Errorf("Union fingerprints = %v, want deduplicated %v", got, want)
	}

	if got := sigverify.Union().Fingerprints(); len(got) != 0 {
		t.Errorf("Union() = %v, want empty set", got)
	}
}

func TestFingerprintsStable(t *testing.T) {
	t.Parallel()

	kp := mustKeyPair(t, sigtest.ECDSAP256)
	p := mustPEM(t, kp)
	first, err := sigverify.ParsePublicKeys(p)
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	second, err := sigverify.ParsePublicKeys(p)
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	if a, b := first.Fingerprints()[0], second.Fingerprints()[0]; a != b {
		t.Errorf("fingerprint unstable across parses: %q vs %q", a, b)
	}

	other := mustKeyPair(t, sigtest.ECDSAP256)
	otherKeys, err := sigverify.ParsePublicKeys(mustPEM(t, other))
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	if first.Fingerprints()[0] == otherKeys.Fingerprints()[0] {
		t.Error("two distinct keys share a fingerprint")
	}
}
