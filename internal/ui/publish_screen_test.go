// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// stubPublisher records what the screen handed to the engine and replays a
// scripted answer. The screen owns no publication rule of its own — the
// SDK and engine.Publisher do — so what these tests pin is that the screen
// forwards faithfully and renders the verdict, not that it re-derives one.
type stubPublisher struct {
	gotRef string
	gotDoc string
	res    *engine.PublishResult
	err    error
}

func (s *stubPublisher) PublishRecipe(_ context.Context, ref string, doc []byte) (*engine.PublishResult, error) {
	s.gotRef, s.gotDoc = ref, string(doc)
	return s.res, s.err
}

const sampleRecipe = "apiVersion: recipe.tobby.sh/v1alpha1\nkind: Recipe\nmetadata:\n  name: wordpress\n  version: 6.8.2\n"

// TestPublishScreenPrefillsTheDestination: the form opens on the cookbook
// this instance promotes to (FR-013, FR-034) — the prefix only, because
// the name and the version come from the document's own metadata (§11.3).
func TestPublishScreenPrefillsTheDestination(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{
		Destination: "registry.example.com",
		Cookbook:    "cookbook",
		Publisher:   &stubPublisher{},
	}, nil)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")

	w := get(t, mux, c, "/recipes/publish", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/recipes/publish = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`action="/recipes/publish"`,
		`name="reference"`,
		`name="document"`,
		`value="registry.example.com/cookbook/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the publication screen misses %q", want)
		}
	}
}

// TestPublishScreenInertWithoutAPublisher: an instance wired without a
// publishing side says so and disables the submit, rather than accepting a
// document that could not go anywhere.
func TestPublishScreenInertWithoutAPublisher(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{}, nil)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")

	body := get(t, mux, c, "/recipes/publish", nil).Body.String()
	if !strings.Contains(body, "disabled") {
		t.Error("the submit button is live on an instance with no publisher")
	}
}

// TestPublishSubmitShowsTheDigestAndTheSigningLine is the R-40 success
// path and the ADR-0007 guarantee in one: the screen shows what was
// published and hands over the cosign line — it never claims to sign.
func TestPublishSubmitShowsTheDigestAndTheSigningLine(t *testing.T) {
	pub := &stubPublisher{res: &engine.PublishResult{
		Reference: "registry.example.com/cookbook/wordpress:6.8.2",
		Digest:    "sha256:c0ffee",
	}}
	logs := &strings.Builder{}
	u := newTestUIWithOptions(t, &Options{Publisher: pub}, logs)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")

	form := url.Values{
		"csrf":      {csrfOf(t, u, c)},
		"reference": {"registry.example.com/cookbook/wordpress:6.8.2"},
		"document":  {sampleRecipe},
	}
	w := postForm(t, mux, c, "/recipes/publish", form.Encode(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}
	// The document reaches the engine byte for byte: a screen that
	// normalized the YAML would publish something the author never wrote.
	if pub.gotDoc != sampleRecipe {
		t.Errorf("the document was altered on the way: %q", pub.gotDoc)
	}
	if pub.gotRef != "registry.example.com/cookbook/wordpress:6.8.2" {
		t.Errorf("reference = %q", pub.gotRef)
	}
	body := w.Body.String()
	if !strings.Contains(body, "sha256:c0ffee") {
		t.Error("the published digest is not shown")
	}
	// The signing command targets the DIGEST, on the repository without
	// its tag: cosign signs digests, and a tag can be moved.
	if !strings.Contains(body, "cosign sign") ||
		!strings.Contains(body, "registry.example.com/cookbook/wordpress@sha256:c0ffee") {
		t.Errorf("the cosign line is missing or targets a tag: %s", body)
	}
	// FR-094: publishing is outbound writing and is recorded.
	trail := logs.String()
	if !strings.Contains(trail, `"action":"recipe.publish"`) ||
		!strings.Contains(trail, `"actor":"op"`) ||
		!strings.Contains(trail, `"outcome":"success"`) {
		t.Errorf("no FR-094 publication record: %s", trail)
	}
}

// TestPublishSubmitReportsTheNoOp: the same document twice is not a
// conflict (RECIPE-SPEC §8), and the screen says nothing moved instead of
// reporting a fresh publication.
func TestPublishSubmitReportsTheNoOp(t *testing.T) {
	pub := &stubPublisher{res: &engine.PublishResult{
		Reference: "registry.example.com/cookbook/wordpress:6.8.2",
		Digest:    "sha256:c0ffee",
		Unchanged: true,
	}}
	u := newTestUIWithOptions(t, &Options{Publisher: pub}, nil)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")

	form := "csrf=" + csrfOf(t, u, c) + "&reference=registry.example.com%2Fcookbook%2Fwordpress%3A6.8.2&document=" + url.QueryEscape(sampleRecipe)
	body := postForm(t, mux, c, "/recipes/publish", form, nil).Body.String()
	if !strings.Contains(body, "up-to-date") {
		t.Error("an unchanged republication does not read as one")
	}
}

// TestPublishRefusalsAreLegible walks the four refusals R-40 names. Each
// one is produced upstream — by the SDK, by the immutability rule, by the
// allowlist — and the screen's job is to render it with its stable code
// and the entry's real HTTP status, never to invent a message.
func TestPublishRefusalsAreLegible(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		code   string
		status int
	}{
		"draft, not cooked": {
			taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
				"file": "wordpress.yaml", "path": "spec.ingredients[0].digest",
				"constraint": "a cooked recipe pins every ingredient",
			}), "TBY-VAL-001", http.StatusUnprocessableEntity,
		},
		"name contradicts the destination": {
			taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
				"file": "wordpress.yaml", "path": "metadata.name",
				"constraint": "is redis but the reference publishes wordpress",
			}), "TBY-VAL-001", http.StatusUnprocessableEntity,
		},
		"tag already holds other content": {
			taxonomy.New(taxonomy.CodeTagImmutable, taxonomy.Params{
				"reference": "registry.example.com/cookbook/wordpress:6.8.2",
				"published": "sha256:aaa", "candidate": "sha256:bbb",
			}), "TBY-POL-004", http.StatusConflict,
		},
		"destination outside the allowlist": {
			taxonomy.New(taxonomy.CodeNotAllowlisted, taxonomy.Params{"host": "evil.example"}),
			"TBY-POL-001", http.StatusForbidden,
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs := &strings.Builder{}
			u := newTestUIWithOptions(t, &Options{Publisher: &stubPublisher{err: tc.err}}, logs)
			mux := mount(u)
			c := login(t, mux, "op", "pw-op")
			form := "csrf=" + csrfOf(t, u, c) + "&reference=registry.example.com%2Fcookbook%2Fwordpress%3A6.8.2&document=" + url.QueryEscape(sampleRecipe)
			w := postForm(t, mux, c, "/recipes/publish", form, nil)
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.code) {
				t.Errorf("the refusal does not carry %s", tc.code)
			}
			// The submission survives the refusal: an operator edits it
			// rather than retyping a 40-line document.
			if !strings.Contains(body, "kind: Recipe") {
				t.Error("the submitted document was dropped by the refusal")
			}
			if !strings.Contains(logs.String(), `"action":"recipe.publish"`) {
				t.Error("a refused publication left no FR-094 record")
			}
		})
	}
}

// TestPublishTransportFailureIsNamed: a registry that does not answer is
// a transport condition and gets the transport code, not the generic
// internal error that tells an operator nothing.
func TestPublishTransportFailureIsNamed(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{
		Publisher: &stubPublisher{err: errors.New("dial tcp: i/o timeout")},
	}, nil)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")
	form := "csrf=" + csrfOf(t, u, c) + "&reference=registry.example.com%2Fcookbook%2Fwordpress%3A6.8.2&document=" + url.QueryEscape(sampleRecipe)
	w := postForm(t, mux, c, "/recipes/publish", form, nil)
	body := w.Body.String()
	if !strings.Contains(body, "TBY-REG-002") || !strings.Contains(body, "registry.example.com") {
		t.Errorf("a transport failure did not name the host it could not reach: %d %s", w.Code, body)
	}
}

// TestPublishRejectsAnEmptySubmission: the two required inputs are checked
// before the engine is called at all — the screen never opens a
// connection to publish nothing.
func TestPublishRejectsAnEmptySubmission(t *testing.T) {
	pub := &stubPublisher{}
	u := newTestUIWithOptions(t, &Options{Publisher: pub}, nil)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")

	w := postForm(t, mux, c, "/recipes/publish", "csrf="+csrfOf(t, u, c)+"&reference=&document=", nil)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-VAL-001") {
		t.Errorf("empty submission = %d, want 422 TBY-VAL-001", w.Code)
	}
	if pub.gotRef != "" {
		t.Error("the engine was called with an empty submission")
	}
}

// TestPublishLinkedFromTheRecipesScreen: the screen exists only if it can
// be reached — and only operators are offered it (R-40 role floor).
func TestPublishLinkedFromTheRecipesScreen(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{Publisher: &stubPublisher{}}, nil)
	mux := mount(u)

	c := login(t, mux, "op", "pw-op")
	if !strings.Contains(get(t, mux, c, "/recipes", nil).Body.String(), `href="/recipes/publish"`) {
		t.Error("/recipes does not link to the publication screen for an operator")
	}
	cv := login(t, mux, "lecteur", "pw-view")
	if strings.Contains(get(t, mux, cv, "/recipes", nil).Body.String(), `href="/recipes/publish"`) {
		t.Error("/recipes offers the publication screen to a viewer")
	}
}
