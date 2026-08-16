// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// trustEnv is the FR-033 fixture: one source registry holding one shared
// ingredient and three recipes — signed by the trusted key, signed by a
// foreign key, and unsigned.
func seedTrustCookbook(t *testing.T) (src *registry, imgDig string) {
	t.Helper()
	src = newRegistry(t)
	imgDig = seedImage(t, src, "library/app", "1.0.0")
	return src, imgDig
}

func trustRecipeYAML(t *testing.T, src *registry, recipeName, imgDig string) []byte {
	t.Helper()
	return cookedRecipeYAML(t, recipeName, "1.0.0", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/library/app", Version: "1.0.0", Digest: imgDig,
	}})
}

// TestSyncSignatureRejection locks FR-033 and §12.3 point 4: a recipe
// signed by a key outside the trust roots fails with TBY-SIG-001 naming
// the fingerprints tried; an unsigned recipe fails likewise; the failure
// is isolated per item — the trusted recipe still lands.
func TestSyncSignatureRejection(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	dst := openStore(t)
	trusted, foreign := newKeyPair(t), newKeyPair(t)

	goodDig := publishRecipe(t, src.st, "cookbook/good", "1.0.0", trustRecipeYAML(t, src, "good", imgDig))
	signManifest(t, src.st, "cookbook/good", goodDig, trusted)
	evilDig := publishRecipe(t, src.st, "cookbook/evil", "1.0.0", trustRecipeYAML(t, src, "evil", imgDig))
	signManifest(t, src.st, "cookbook/evil", evilDig, foreign)
	publishRecipe(t, src.st, "cookbook/bare", "1.0.0", trustRecipeYAML(t, src, "bare", imgDig))

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "evil", Version: "1.0.0"},
		{Name: "bare", Version: "1.0.0"},
		{Name: "good", Version: "1.0.0"},
	})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, trusted), retr, "", syncCfg())
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The foreign signature: rejected with the trusted fingerprints tried.
	evil := itemByName(t, task, "evil@1.0.0")
	if evil.Status != tasks.StatusFailed || evil.Error == nil || evil.Error.Code != taxonomy.CodeSignature {
		t.Fatalf("evil item = %+v, want failed TBY-SIG-001", evil)
	}
	fps, _ := evil.Error.Params["fingerprints"].(string)
	wantFP, err := trusted.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fps, wantFP) {
		t.Errorf("rejection fingerprints %q do not name the trusted key %s (FR-033)", fps, wantFP)
	}

	// The unsigned recipe under the strict default: rejected too.
	bare := itemByName(t, task, "bare@1.0.0")
	if bare.Status != tasks.StatusFailed || bare.Error == nil || bare.Error.Code != taxonomy.CodeSignature {
		t.Fatalf("bare item = %+v, want failed TBY-SIG-001", bare)
	}

	// Isolation (§12.3 point 4): the trusted recipe fully landed.
	for _, name := range []string{"good@1.0.0/app", "good@1.0.0/recipe"} {
		if it := itemByName(t, task, name); it.Status != tasks.StatusDone {
			t.Errorf("item %s = %s (error: %+v), want done despite sibling failures", name, it.Status, it.Error)
		}
	}
	recs, err := dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Name != "good" || !recs[0].Verified {
		t.Errorf("recipe records = %+v, want only the verified good recipe", recs)
	}
}

// TestSyncDeclaredScope locks the declared-scope relaxation of FR-033: an
// unsigned recipe is admitted only inside a matching allowUnsigned scope,
// and the admission is visible everywhere (record, resolution row) —
// never silent.
func TestSyncDeclaredScope(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	publishRecipe(t, src.st, "cookbook/bare", "1.0.0", trustRecipeYAML(t, src, "bare", imgDig))
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "bare", Version: "1.0.0"},
	})
	// Trust scopes match the CANONICAL nominal repository: ":" folds to
	// "_" per ADR-0013.
	canonicalRepo := strings.ReplaceAll(src.addr, ":", "_") + "/cookbook/*"

	t.Run("matching scope admits", func(t *testing.T) {
		dst := openStore(t)
		tp := trustFor(t, []config.TrustScope{
			{Name: "lab", Repositories: []string{canonicalRepo}, AllowUnsigned: true},
		}) // no roots at all
		task, err := runSync(t, New(dst, newRemotes(t, nil), tp, retr, "", syncCfg()))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if agg := task.Aggregate(); agg.Failed != 0 || agg.Done != 2 {
			t.Fatalf("aggregates = %+v (items %s), want app+recipe done", agg, itemNames(task))
		}
		recs, err := dst.RecipeRecords()
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].Verified || recs[0].TrustScope != "lab" {
			t.Errorf("record = %+v, want Verified=false TrustScope=lab (never silent)", recs)
		}
		if row := resolutionFor(t, task, "bare@1.0.0", ""); row.TrustScope != "lab" {
			t.Errorf("resolution row = %+v, want TrustScope=lab", row)
		}
	})

	t.Run("matching scope with roots configured", func(t *testing.T) {
		// Same admission when trust roots exist but the recipe is unsigned:
		// the ErrNoSignature + AllowUnsigned branch.
		dst := openStore(t)
		tp := trustFor(t, []config.TrustScope{
			{Name: "lab", Repositories: []string{canonicalRepo}, AllowUnsigned: true},
		}, newKeyPair(t))
		task, err := runSync(t, New(dst, newRemotes(t, nil), tp, retr, "", syncCfg()))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if agg := task.Aggregate(); agg.Failed != 0 {
			t.Fatalf("aggregates = %+v (items %s), want no failure", agg, itemNames(task))
		}
		recs, err := dst.RecipeRecords()
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].Verified || recs[0].TrustScope != "lab" {
			t.Errorf("record = %+v, want Verified=false TrustScope=lab", recs)
		}
	})

	t.Run("non-matching pattern refuses", func(t *testing.T) {
		dst := openStore(t)
		tp := trustFor(t, []config.TrustScope{
			{Name: "elsewhere", Repositories: []string{"other.example.com/**"}, AllowUnsigned: true},
		})
		task, err := runSync(t, New(dst, newRemotes(t, nil), tp, retr, "", syncCfg()))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		it := itemByName(t, task, "bare@1.0.0")
		if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
			t.Errorf("item = %+v, want failed TBY-SIG-001 outside the declared scope", it)
		}
		recs, err := dst.RecipeRecords()
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 0 {
			t.Errorf("records = %+v, want none for a refused recipe", recs)
		}
	})
}

// TestMatchPattern locks the scope glob semantics: "*" within one path
// segment, "**" across segments, whole-path anchoring.
func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"registry.example.com/cookbook/wordpress", "registry.example.com/cookbook/wordpress", true},
		{"registry.example.com/cookbook/wordpress", "registry.example.com/cookbook/mariadb", false},
		{"registry.example.com/cookbook/*", "registry.example.com/cookbook/wordpress", true},
		{"registry.example.com/cookbook/*", "registry.example.com/cookbook/a/b", false}, // "*" stays in its segment
		{"registry.example.com/cookbook/*", "registry.example.com/cookbook", false},
		{"*", "registry.example.com", true},
		{"*", "registry.example.com/x", false},
		{"**", "a/b/c", true},
		{"**", "a", true},
		{"registry.example.com/**", "registry.example.com/a/b/c", true},
		{"registry.example.com/**", "registry.example.com", true}, // "**" absorbs zero segments
		{"**/wordpress", "a/b/wordpress", true},
		{"**/wordpress", "a/b/mariadb", false},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/c", false},
		{"reg*.example.com/cook*", "registry.example.com/cookbook", true},
		{"reg*.example.com/cook*", "docs.example.com/cookbook", false},
		{"*-lab/**", "zone-lab/x/y", true},
		{"*-lab/**", "zonelab/x", false},
		{"c*ook*book", "cookandbook", true}, // multiple "*" within one segment
		{"c*ook*book", "cbook", false},
		{"a*", "abc", true},
		{"*z", "abc", false},
	}
	for _, tc := range cases {
		if got := matchPattern(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// TestSignatureTag locks the cosign attached-signature tag convention
// (ADR-0007).
func TestSignatureTag(t *testing.T) {
	got := SignatureTag("sha256:0a1b2c")
	if got != "sha256-0a1b2c.sig" {
		t.Errorf("SignatureTag = %q, want sha256-0a1b2c.sig", got)
	}
}

// TestLoadTrustSources locks the three trust-root forms of §12.3 (inline,
// file, URL-with-cache) and the scope surfaces.
func TestLoadTrustSources(t *testing.T) {
	kp := newKeyPair(t)
	pem, err := kp.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("key file", func(t *testing.T) {
		path := writeTempFile(t, "root.pub", pem)
		tp, err := LoadTrust(config.Trust{Roots: []config.TrustRoot{{Name: "f", KeyFile: path}}}, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !tp.HasRoots() {
			t.Error("HasRoots() = false after loading a key file")
		}
	})

	t.Run("key URL cached at configuration time", func(t *testing.T) {
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits++
			_, _ = w.Write(pem)
		}))
		cacheDir := t.TempDir()
		cfg := config.Trust{Roots: []config.TrustRoot{{Name: "url-root", KeyURL: srv.URL + "/cosign.pub"}}}
		if _, err := LoadTrust(cfg, cacheDir, nil); err != nil {
			t.Fatal(err)
		}
		if hits != 1 {
			t.Fatalf("key server hits = %d, want 1", hits)
		}
		// The key server going away degrades to the cached copy, not an
		// outage (§12.3: fetched at configuration time only).
		srv.Close()
		tp, err := LoadTrust(cfg, cacheDir, nil)
		if err != nil {
			t.Fatalf("LoadTrust with unreachable key server and warm cache: %v", err)
		}
		if !tp.HasRoots() {
			t.Error("HasRoots() = false on the cached key")
		}
	})

	t.Run("key URL without a cache directory", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/cosign.pub" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(pem)
		}))
		t.Cleanup(srv.Close)
		got, err := fetchCachedKey(srv.Client(), srv.URL+"/cosign.pub", "")
		if err != nil || !bytes.Equal(got, pem) {
			t.Errorf("fetchCachedKey without cache = %q, %v", got, err)
		}
		_, err = fetchCachedKey(srv.Client(), srv.URL+"/missing", "")
		if err == nil {
			t.Error("fetchCachedKey without cache accepted a failing fetch")
		}
	})

	t.Run("http error falls back to the cached copy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		cache := writeTempFile(t, "root.pub", pem)
		got, err := fetchCachedKey(srv.Client(), srv.URL+"/cosign.pub", cache)
		if err != nil || !bytes.Equal(got, pem) {
			t.Errorf("HTTP-error fallback = %q, %v, want the cached key", got, err)
		}
		// Same failure with a COLD cache: a hard error naming the fetch.
		if _, err := fetchCachedKey(srv.Client(), srv.URL+"/cosign.pub", cache+".cold"); err == nil {
			t.Error("cold-cache HTTP error did not surface")
		}
	})

	t.Run("invalid inline key refuses at load time", func(t *testing.T) {
		_, err := LoadTrust(config.Trust{Roots: []config.TrustRoot{{Name: "bad", Key: "not-a-pem"}}}, t.TempDir(), nil)
		if err == nil {
			t.Error("LoadTrust accepted a non-PEM trust root")
		}
	})

	t.Run("relaxed scopes are visible", func(t *testing.T) {
		tp := trustFor(t, []config.TrustScope{
			{Name: "lab", Repositories: []string{"**"}, AllowUnsigned: true},
			{Name: "strict", Repositories: []string{"**"}},
		}, kp)
		if got := tp.RelaxedScopes(); len(got) != 1 || got[0] != "lab" {
			t.Errorf("RelaxedScopes() = %v, want [lab]", got)
		}
	})

	t.Run("scope root restriction", func(t *testing.T) {
		kp2 := newKeyPair(t)
		tp := trustFor(t, []config.TrustScope{
			{Name: "narrow", Repositories: []string{"narrow.example.com/**"}, Roots: []string{rootName(0)}},
		}, kp, kp2)
		d := tp.Decide("narrow.example.com/cookbook/x")
		if d.Scope != "narrow" || d.Keys == nil || len(d.Keys.Fingerprints()) != 1 {
			t.Errorf("restricted-scope decision = %+v (fingerprints %v), want the single named root",
				d, fingerprintsOf(d))
		}
		// Outside the scope: the full root set.
		d = tp.Decide("other.example.com/cookbook/x")
		if d.Scope != "" || d.AllowUnsigned || len(d.Keys.Fingerprints()) != 2 {
			t.Errorf("default decision = %+v (fingerprints %v), want strict with both roots", d, fingerprintsOf(d))
		}
	})
}

func fingerprintsOf(d Decision) []string {
	if d.Keys == nil {
		return nil
	}
	return d.Keys.Fingerprints()
}

// The embedded store must keep satisfying the engine's write surface.
var _ MetaStore = (*store.Store)(nil)

// TestEngineSurfaces: the convenience accessors used by the UI layer.
func TestEngineSurfaces(t *testing.T) {
	tp := trustFor(t, []config.TrustScope{{Name: "lab", Repositories: []string{"**"}, AllowUnsigned: true}})
	e := New(openStore(t), newRemotes(t, nil), tp, "/some/retriever.yaml", "", syncCfg())
	if e.Source() != "/some/retriever.yaml" {
		t.Errorf("Source() = %q", e.Source())
	}
	if got := e.RelaxedScopes(); len(got) != 1 || got[0] != "lab" {
		t.Errorf("RelaxedScopes() = %v, want [lab]", got)
	}
}
