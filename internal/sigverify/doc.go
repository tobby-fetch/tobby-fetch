// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package sigverify verifies cosign key-based attached signatures fully
// offline (FR-033, ADR-0007, RECIPE-SPEC §12).
//
// Tobby never talks to Fulcio or Rekor: keyless Sigstore depends on online
// services unreachable from restricted and air-gapped zones. Verification
// uses only configured trust roots (public keys, RECIPE-SPEC §12.3) and the
// signature artifacts that travel with the content (RECIPE-SPEC §12.2).
// Tobby also never signs anything in production (project decision no. 10:
// Tobby is the pallet truck, not the notary) — this package is verify-only,
// and the sigstore libraries are deliberately not imported: the cosign
// attached-signature format is small and stable enough to verify with the
// standard library alone.
//
// The verified format is the cosign "attached signature" convention: the
// signature of <repo>@sha256:<hex> is an OCI image manifest stored in the
// same repository under the tag "sha256-<hex>.sig". Each layer of that
// manifest is a SimpleSigning JSON payload
// (application/vnd.dev.cosign.simplesigning.v1+json) whose raw bytes are
// signed; the signature travels base64-encoded in the layer descriptor
// annotation "dev.cosignproject.cosign/signature". The payload pins the
// subject through critical.image.docker-manifest-digest.
//
// The verifier reads through the Manifests interface so the same checks run
// against a remote registry client at import time and against the embedded
// store destination-side (FR-052 replays verification after physical
// transport, when media contents are still untrusted).
package sigverify
