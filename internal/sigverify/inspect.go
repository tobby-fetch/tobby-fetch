// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package sigverify

import (
	"context"
	"errors"
	"strings"
)

// State is what an optional signature turned out to be.
type State string

// The three answers an informational verification gives (FR-033 as
// amended, RECIPE-SPEC §12.2).
const (
	// StateVerified: a signature verifies against one of the keys.
	StateVerified State = "verified"
	// StateUnverifiable: a signature is published, but none of the keys
	// verifies it — a publisher's key this zone does not know, or an
	// artifact that cannot be verified at all.
	StateUnverifiable State = "unverifiable"
	// StateNone: no signature is published, in either layout.
	StateNone State = "none"
)

// Inspection is the informational verdict on a signature whose presence
// is optional — an ingredient's own (RECIPE-SPEC §12.2: "verifying them
// is a consumer policy decision"). It is a record, not a refusal: the
// caller decides what, if anything, it blocks. Set whole and never
// mutated in place once recorded.
type Inspection struct {
	State State `json:"state"`
	// Fingerprint is the key that verified, when State is verified.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Tried lists the fingerprints of the keys tried, when a well-formed
	// signature exists and none of them verifies it.
	Tried []string `json:"tried,omitempty"`
	// Detail says why the signature is unverifiable when the answer is
	// not a question of keys: a malformed artifact, no key applying at
	// all, a store that could not be read.
	Detail string `json:"detail,omitempty"`
}

// Inspect verifies repo@manifestDigest's signature for information. It
// never fails: every outcome, including a transport error, is an
// Inspection. A nil or empty key set is allowed — the subject is then
// reported as unsigned or as carrying a signature nothing here can
// verify, which is the honest answer of a zone with no applicable root.
func Inspect(ctx context.Context, src Manifests, repo, manifestDigest string, keys *Keys) Inspection {
	if keys == nil {
		keys = &Keys{}
	}
	fp, err := verify(ctx, src, repo, manifestDigest, keys)
	if err == nil {
		return Inspection{State: StateVerified, Fingerprint: fp}
	}
	if errors.Is(err, ErrNoSignature) {
		return Inspection{State: StateNone}
	}
	var noKey *NoTrustedKeyError
	if errors.As(err, &noKey) {
		in := Inspection{State: StateUnverifiable, Tried: noKey.Tried}
		if len(noKey.Tried) == 0 {
			in.Detail = "no trust root applies"
		}
		return in
	}
	var bad *BadSignatureError
	if errors.As(err, &bad) {
		return Inspection{State: StateUnverifiable, Detail: "invalid signature artifact: " + bad.Reason}
	}
	// Transport or store failure: whether a signature exists is unknown,
	// and "unverifiable" is the only claim that stays true.
	return Inspection{State: StateUnverifiable, Detail: "signature lookup failed: " + err.Error()}
}

// String renders the inspection the way a log line or a report row reads
// it: "verified by <fingerprint>", "present, not verifiable by this
// zone's trust roots (…)" or "none".
func (in Inspection) String() string {
	switch in.State {
	case StateVerified:
		return "verified by " + in.Fingerprint
	case StateUnverifiable:
		var why []string
		if len(in.Tried) > 0 {
			why = append(why, "keys tried: "+strings.Join(in.Tried, ", "))
		}
		if in.Detail != "" {
			why = append(why, in.Detail)
		}
		s := "present, not verifiable by this zone's trust roots"
		if len(why) > 0 {
			s += " (" + strings.Join(why, "; ") + ")"
		}
		return s
	case StateNone:
		return "none"
	}
	return string(in.State)
}

// Verified reports whether the inspection found a verifying signature.
func (in Inspection) Verified() bool { return in.State == StateVerified }
