// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

// B-064: the referrers index is a hint, never an answer.
//
// Fallible before the fix (milestone-7 review, proofs R1 and R2): the
// forged entry below was served as is — invented artifact type and
// annotations on a manifest of the repository, e.g. a fake VEX document
// hiding CVEs from a scanner — and a GET with no index wrote one under
// meta/, which on a read-only medium answered 500 on every call.

// TestForgedReferrersIndexIsNotServed: an index written by hand — in the
// earlier format, annotations included — that rattaches an unrelated
// manifest to the subject and redescribes a genuine referrer is ignored in
// both respects: the unrelated manifest is dropped, the genuine referrer is
// described from its own payload.
func TestForgedReferrersIndexIsNotServed(t *testing.T) {
	f := newReferrersFixture(t)
	genuine := f.push(t, "application/vnd.example.sbom", "genuine")
	other := putTestManifest(t, f.st, "app", "v2", "other layer")

	forged := `{"repository":"app","referrers":{"` + f.subject + `":[` +
		`{"mediaType":"` + ociManifest + `","digest":"` + other + `","size":7,` +
		`"artifactType":"application/vnd.openvex+json","annotations":{"forged":"yes"}},` +
		`{"mediaType":"` + ociManifest + `","digest":"` + genuine.String() + `","size":1,` +
		`"artifactType":"application/vnd.openvex+json","annotations":{"forged":"yes"}}]}}`
	if err := os.WriteFile(referrerIndexPath(f.st.root, "app"), []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}

	_, body := f.do(t, http.MethodGet, "/v2/app/referrers/"+f.subject, nil, "")
	for _, bad := range []string{"forged", "openvex", other} {
		if strings.Contains(string(body), bad) {
			t.Errorf("the forged index leaks %q into the listing: %s", bad, body)
		}
	}
	_, l := f.list(t, f.subject, "")
	if len(l.Manifests) != 1 || l.Manifests[0].Digest != genuine.String() ||
		l.Manifests[0].ArtifactType != "application/vnd.example.sbom" || l.Manifests[0].Annotations["note"] != "genuine" {
		t.Errorf("the genuine referrer is not described from its payload: %+v", l.Manifests)
	}
}

// TestReferrersReadWritesNothing: a listing on a store with no index
// derives it in memory and writes nothing under meta/ — a medium may be
// mounted read-only, and a read of a covered tree must leave it as it was.
func TestReferrersReadWritesNothing(t *testing.T) {
	st, err := Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	subj := putTestManifest(t, st, "app", "v1", "layer")
	if err := os.RemoveAll(filepath.Join(st.root, "meta", "referrers")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := st.Referrers(context.Background(), "app", digest.Digest(subj), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(referrerIndexPath(st.root, "app")); err == nil {
		t.Fatalf("a referrers read wrote %s", referrerIndexPath(st.root, "app"))
	}
}

// TestReferrerScopeDecidesTheCandidates: under a scope, the index is not
// consulted at all — a referrer outside the scope is not listed even though
// the index names it, and one inside is listed even when the index omits it.
func TestReferrerScopeDecidesTheCandidates(t *testing.T) {
	f := newReferrersFixture(t)
	d := f.push(t, "application/vnd.example.sig", "signature")
	ctx := context.Background()

	got, err := f.st.Referrers(WithReferrerScope(ctx, func(string) []digest.Digest { return nil }),
		"app", digest.Digest(f.subject), "")
	if err != nil || len(got) != 0 {
		t.Errorf("a referrer outside the scope was listed: %+v (%v)", got, err)
	}

	empty := `{"repository":"app","referrers":{}}`
	if err := os.WriteFile(referrerIndexPath(f.st.root, "app"), []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = f.st.Referrers(WithReferrerScope(ctx, func(string) []digest.Digest {
		return []digest.Digest{digest.Digest(f.subject), d}
	}), "app", digest.Digest(f.subject), "")
	if err != nil || len(got) != 1 || got[0].Digest != d {
		t.Errorf("a referrer inside the scope that the index omits was not listed: %+v (%v)", got, err)
	}
}

// TestReferrersPerSubjectAreBounded (R5): the referrers of one subject are
// capped, the PUT past the cap is refused before anything is stored, and
// the index carries no annotations.
func TestReferrersPerSubjectAreBounded(t *testing.T) {
	old := maxReferrersPerSubject
	maxReferrersPerSubject = 2
	defer func() { maxReferrersPerSubject = old }()
	f := newReferrersFixture(t)
	f.push(t, "application/vnd.example.a", "one")
	f.push(t, "application/vnd.example.b", "two")

	raw, d := f.referrer(t, "application/vnd.example.c", "three")
	resp, body := f.do(t, http.MethodPut, "/v2/app/manifests/"+d.String(), raw, ociManifest)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "DENIED") {
		t.Fatalf("the PUT past the cap = %d %s, want 400 DENIED", resp.StatusCode, body)
	}
	if resp, _ := f.do(t, http.MethodGet, "/v2/app/manifests/"+d.String(), nil, ""); resp.StatusCode == http.StatusOK {
		t.Error("the refused referrer was stored anyway")
	}
	idx, err := os.ReadFile(referrerIndexPath(f.st.root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(idx), "annotations") {
		t.Errorf("the index stores annotations: %s", idx)
	}
}
