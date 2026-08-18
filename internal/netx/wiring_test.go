// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package netx_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
	"gopkg.in/yaml.v3"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestEveryOutboundPathUsesTheSharedTransport is the acceptance of FR-080
// and FR-081 together, and the reason this lot exists.
//
// It walks every outbound path Tobby has — the registry reads of the
// recipe engine, the registry writes of publication, the two unit-import
// paths (OCI and Helm chart repository), the desired-state document over
// both HTTP and OCI, and the trust root fetched by URL — inside a zone
// where direct egress does not work and the only route out is an
// authenticated proxy in front of a registry behind a private authority.
//
// A path that built its own transport would fail here on the name, which
// resolves nowhere; a path that somehow found the address would show up as
// a connection the proxy never opened. Either way it is a failure, not a
// slow success — which is the property the requirement is really asking
// for, because in a real zone a forgotten proxy does not error, it hangs.
func TestEveryOutboundPathUsesTheSharedTransport(t *testing.T) {
	z := newZone(t)
	requireBlockedDirectEgress(t, z.Addr)

	// Content the paths will read. Seeded over the wire, through the same
	// kind of transport, because there is no other way in.
	seedIndex(t, zoneEgress(t, z), z.Addr+"/library/nginx:1.25.0")

	local := openLocalStore(t)

	// Every path gets its own Egress rather than sharing one. Sharing
	// would pool connections: the second path would ride the first
	// path's tunnel, the proxy would see nothing new, and "did THIS path
	// use the transport?" would become unanswerable. One transport per
	// case makes each tunnel attributable to the path that opened it.

	t.Run("engine registry read (FR-036 remote access)", func(t *testing.T) {
		eg := zoneEgress(t, z)
		a, p := z.Snapshot()
		remotes, err := engine.NewRemotes(config.Registries{}, nil, eg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remotes.Get(t.Context(), z.Addr+"/library/nginx", "1.25.0"); err != nil {
			t.Fatalf("reading through the shared transport: %v", err)
		}
		if _, err := remotes.ListTags(t.Context(), z.Addr+"/library/nginx"); err != nil {
			t.Fatalf("listing tags through the shared transport: %v", err)
		}
		z.AssertRoutedThroughProxy(t, "engine registry read", a, p)
	})

	t.Run("engine registry write (recipe publication)", func(t *testing.T) {
		eg := zoneEgress(t, z)
		a, p := z.Snapshot()
		pub, err := engine.NewPublisher(config.Registries{}, nil, eg)
		if err != nil {
			t.Fatal(err)
		}
		res, err := pub.PublishRecipe(t.Context(), z.Addr+"/cookbook/wordpress:6.8.2", cookedRecipe(t))
		if err != nil {
			t.Fatalf("publishing through the shared transport: %v", err)
		}
		if res.Digest == "" {
			t.Error("publication reported no digest")
		}
		z.AssertRoutedThroughProxy(t, "recipe publication", a, p)
	})

	t.Run("retriever over HTTPS (FR-010)", func(t *testing.T) {
		z.Serve("/retriever.yaml", retrieverDocument(t))
		eg := zoneEgress(t, z)
		a, p := z.Snapshot()
		remotes, err := engine.NewRemotes(config.Registries{}, nil, eg)
		if err != nil {
			t.Fatal(err)
		}
		r, err := engine.LoadRetriever(t.Context(), remotes, z.URL("/retriever.yaml"))
		if err != nil {
			t.Fatalf("fetching the retriever through the shared transport: %v", err)
		}
		if r.Metadata.Name != "zone-source" {
			t.Errorf("retriever = %+v", r)
		}
		z.AssertRoutedThroughProxy(t, "retriever over HTTPS", a, p)
	})

	t.Run("retriever as an OCI artifact (FR-010)", func(t *testing.T) {
		pushRetrieverArtifact(t, zoneEgress(t, z), z.Addr+"/retrievers/zone:current", retrieverDocument(t))
		eg := zoneEgress(t, z)
		a, p := z.Snapshot()
		remotes, err := engine.NewRemotes(config.Registries{}, nil, eg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.LoadRetriever(t.Context(), remotes, "oci://"+z.Addr+"/retrievers/zone:current"); err != nil {
			t.Fatalf("fetching the OCI retriever through the shared transport: %v", err)
		}
		z.AssertRoutedThroughProxy(t, "retriever as an OCI artifact", a, p)
	})

	t.Run("trust root by URL (RECIPE-SPEC §12.3)", func(t *testing.T) {
		kp, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := kp.PublicPEM()
		if err != nil {
			t.Fatal(err)
		}
		z.Serve("/keys/root.pub", pub)

		a, p := z.Snapshot()
		tp, err := engine.LoadTrust(config.Trust{
			Roots: []config.TrustRoot{{Name: "zone", KeyURL: z.URL("/keys/root.pub")}},
		}, t.TempDir(), zoneEgress(t, z))
		if err != nil {
			t.Fatalf("resolving the trust root through the shared transport: %v", err)
		}
		if !tp.HasRoots() {
			t.Error("the fetched trust root produced no key material")
		}
		z.AssertRoutedThroughProxy(t, "trust root by URL", a, p)
	})

	t.Run("unit import inspection (FR-023)", func(t *testing.T) {
		sourcePolicy := importer.WithSourcePolicy(config.Registries{}, nil, zoneEgress(t, z))
		a, p := z.Snapshot()
		rep, err := importer.Inspect(t.Context(), z.Addr+"/library/nginx:1.25.0", local, sourcePolicy)
		if err != nil {
			t.Fatalf("inspecting through the shared transport: %v", err)
		}
		if len(rep.Platforms) == 0 {
			t.Error("the inspection reported no platform")
		}
		z.AssertRoutedThroughProxy(t, "unit import inspection", a, p)
	})

	t.Run("unit import transfer (FR-023)", func(t *testing.T) {
		sourcePolicy := importer.WithSourcePolicy(config.Registries{}, nil, zoneEgress(t, z))
		a, p := z.Snapshot()
		task := &tasks.Task{
			Type:      tasks.TypeUnitImport,
			Reference: z.Addr + "/library/nginx:1.25.0",
			Items:     []tasks.Item{{Name: "linux/amd64"}},
		}
		if err := runTask(t, importer.NewRunner(local, sourcePolicy), task); err != nil {
			t.Fatalf("transferring through the shared transport: %v", err)
		}
		z.AssertRoutedThroughProxy(t, "unit import transfer", a, p)
	})

	t.Run("Helm chart repository over HTTPS (FR-024)", func(t *testing.T) {
		sourcePolicy := importer.WithSourcePolicy(config.Registries{}, nil, zoneEgress(t, z))
		a, p := z.Snapshot()
		rep, err := importer.Inspect(t.Context(), z.URL("/charts/gitea"), local, sourcePolicy)
		if err != nil {
			t.Fatalf("inspecting the chart repository through the shared transport: %v", err)
		}
		if rep.Tag != "1.2.3" {
			t.Errorf("resolved chart version = %q, want 1.2.3", rep.Tag)
		}
		z.AssertRoutedThroughProxy(t, "Helm chart repository inspection", a, p)

		sourcePolicy = importer.WithSourcePolicy(config.Registries{}, nil, zoneEgress(t, z))
		a, p = z.Snapshot()
		task := &tasks.Task{
			Type:      tasks.TypeUnitImport,
			Reference: z.URL("/charts/gitea:1.2.3"),
			Items:     []tasks.Item{{Name: "chart"}},
		}
		if err := runTask(t, importer.NewRunner(local, sourcePolicy), task); err != nil {
			t.Fatalf("importing the chart through the shared transport: %v", err)
		}
		z.AssertRoutedThroughProxy(t, "Helm chart repository import", a, p)
	})

	t.Run("resumable large blob fetch (FR-029)", func(t *testing.T) {
		// The R-29 path is the only one that does NOT go through
		// go-containerregistry's fetch: it issues the blob GET itself so
		// it can carry a Range header. That is exactly the kind of path
		// this test exists to catch, so it is proved here rather than
		// asserted in a comment.
		dgst, size := seedLayer(t, zoneEgress(t, z), z.Addr+"/library/resumable:1")
		eg := zoneEgress(t, z)
		a, p := z.Snapshot()
		res := blobfetch.New(eg, nil, t.TempDir(), 1)
		repo, err := name.NewRepository(z.Addr + "/library/resumable")
		if err != nil {
			t.Fatal(err)
		}
		rc, err := res.Open(t.Context(), repo, dgst, size, nil)
		if err != nil {
			t.Fatalf("resuming through the shared transport: %v", err)
		}
		n, err := io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil || n != size {
			t.Fatalf("read %d of %d bytes: %v", n, size, err)
		}
		z.AssertRoutedThroughProxy(t, "resumable large blob fetch", a, p)
	})

	// The invariant over the whole run, independent of any single path:
	// the origin never accepted a connection the proxy did not open for
	// it. Nothing reached the registry by a route of its own.
	accepted, tunnels := z.Snapshot()
	if accepted != tunnels {
		t.Errorf("the origin accepted %d connections against %d proxy tunnels: %d reached it outside the shared transport",
			accepted, tunnels, accepted-tunnels)
	}
}

// TestOutboundPathsFailWithoutTheProxy is the other half of the proof. It
// is not enough that every path works when the transport is configured:
// the zone has to be one where a path could not have succeeded on its
// own, or the test above would pass just as well against a codebase that
// never wired anything.
//
// The same paths, the same content, the same private authority — only the
// proxy removed. Every one of them must fail.
func TestOutboundPathsFailWithoutTheProxy(t *testing.T) {
	z := newZone(t)
	requireBlockedDirectEgress(t, z.Addr)

	// Trust configured, proxy absent: the failures below are about the
	// route, not about certificates.
	direct, err := netx.New(&config.Network{TLS: config.ClientTLS{CA: z.CAPEM}})
	if err != nil {
		t.Fatal(err)
	}
	local := openLocalStore(t)
	sourcePolicy := importer.WithSourcePolicy(config.Registries{}, nil, direct)

	remotes, err := engine.NewRemotes(config.Registries{}, nil, direct)
	if err != nil {
		t.Fatal(err)
	}
	z.Serve("/retriever.yaml", retrieverDocument(t))

	cases := []struct {
		name string
		call func() error
	}{
		{"engine registry read", func() error {
			_, err := remotes.Get(t.Context(), z.Addr+"/library/nginx", "1.25.0")
			return err
		}},
		{"recipe publication", func() error {
			pub, perr := engine.NewPublisher(config.Registries{}, nil, direct)
			if perr != nil {
				return perr
			}
			_, perr = pub.PublishRecipe(t.Context(), z.Addr+"/cookbook/wordpress:6.8.2", cookedRecipe(t))
			return perr
		}},
		{"retriever over HTTPS", func() error {
			_, rerr := engine.LoadRetriever(t.Context(), remotes, z.URL("/retriever.yaml"))
			return rerr
		}},
		{"trust root by URL", func() error {
			_, terr := engine.LoadTrust(config.Trust{
				Roots: []config.TrustRoot{{Name: "zone", KeyURL: z.URL("/keys/root.pub")}},
			}, "", direct)
			return terr
		}},
		{"unit import inspection", func() error {
			_, ierr := importer.Inspect(t.Context(), z.Addr+"/library/nginx:1.25.0", local, sourcePolicy)
			return ierr
		}},
		{"Helm chart repository", func() error {
			_, ierr := importer.Inspect(t.Context(), z.URL("/charts/gitea"), local, sourcePolicy)
			return ierr
		}},
		{"resumable large blob fetch", func() error {
			repo, rerr := name.NewRepository(z.Addr + "/library/resumable")
			if rerr != nil {
				return rerr
			}
			d, derr := digest.Parse("sha256:" + strings.Repeat("ab", 32))
			if derr != nil {
				return derr
			}
			rc, oerr := blobfetch.New(direct, nil, t.TempDir(), 1).
				Open(t.Context(), repo, d, 4096, nil)
			if oerr == nil {
				_ = rc.Close()
			}
			return oerr
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("the path succeeded without the proxy: the zone does not block direct egress, so the wiring proof above proves nothing")
			}
		})
	}
	if accepted, _ := z.Snapshot(); accepted != 0 {
		t.Errorf("the origin accepted %d connections with no proxy configured", accepted)
	}
}

// TestPrivateAuthorityIsRequiredAndSufficient is the FR-081 acceptance:
// the private CA is what makes the registry reachable, and nothing else
// is — there is no configuration that would have worked by skipping
// verification instead, because Tobby has none.
func TestPrivateAuthorityIsRequiredAndSufficient(t *testing.T) {
	z := newZone(t)
	requireBlockedDirectEgress(t, z.Addr)

	t.Run("without the authority the handshake is refused", func(t *testing.T) {
		eg, err := netx.New(&config.Network{Proxy: proxyConfig(z)})
		if err != nil {
			t.Fatal(err)
		}
		remotes, err := engine.NewRemotes(config.Registries{}, nil, eg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = remotes.Get(t.Context(), z.Addr+"/library/nginx", "1.25.0")
		if err == nil {
			t.Fatal("the registry was reachable without its authority configured")
		}
		if !strings.Contains(err.Error(), "certificate") {
			t.Errorf("error = %v, want a certificate verification failure", err)
		}
	})

	t.Run("configuring the authority makes it reachable", func(t *testing.T) {
		eg := zoneEgress(t, z)
		seedIndex(t, eg, z.Addr+"/library/redis:7.2.0")
		remotes, err := engine.NewRemotes(config.Registries{}, nil, eg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remotes.Get(t.Context(), z.Addr+"/library/redis", "7.2.0"); err != nil {
			t.Fatalf("the registry is not reachable with its authority configured: %v", err)
		}
		if eg.ExtraRoots() != 1 {
			t.Errorf("ExtraRoots() = %d, want 1", eg.ExtraRoots())
		}
	})
}

// TestProxyFailuresNameNoCredential completes the FR-080 acceptance on
// the path where a leak would actually happen. A credential is not
// printed when everything works — it is printed when something breaks and
// a library formats the URL it was dialing into an error that a caller
// then logs.
//
// Two failures, both realistic: the proxy refuses the credentials, and
// the proxy is not there at all. Neither error, at any depth of the
// wrapped chain, may carry the password.
func TestProxyFailuresNameNoCredential(t *testing.T) {
	z := newZone(t)
	requireBlockedDirectEgress(t, z.Addr)

	const wrong = "this-password-is-wrong-and-must-not-be-printed"
	cases := map[string]config.Proxy{
		"refused credentials": {
			URL:      z.ProxyURL,
			Username: proxyUser,
			Password: config.NewSecret(wrong),
		},
		"unreachable proxy": {
			// A port nothing listens on: the transport fails at the
			// proxy hop, which is where it formats what it was dialing.
			URL:      "http://127.0.0.1:1",
			Username: proxyUser,
			Password: config.NewSecret(wrong),
		},
	}
	for name, proxy := range cases {
		t.Run(name, func(t *testing.T) {
			eg, err := netx.New(&config.Network{Proxy: proxy, TLS: config.ClientTLS{CA: z.CAPEM}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(eg.CloseIdleConnections)
			remotes, err := engine.NewRemotes(config.Registries{}, nil, eg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = remotes.Get(t.Context(), z.Addr+"/library/nginx", "1.25.0")
			if err == nil {
				t.Fatal("the fetch succeeded through a proxy that should have refused it")
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				if strings.Contains(e.Error(), wrong) {
					t.Fatalf("the error chain carries the proxy password: %v", e)
				}
			}
		})
	}
}

// zoneEgress builds the instance transport for the zone: the
// authenticated proxy and the private authority, which is the whole of
// what an operator configures.
func zoneEgress(t *testing.T, z *zone) *netx.Egress {
	t.Helper()
	eg, err := netx.New(&config.Network{
		Proxy: proxyConfig(z),
		TLS:   config.ClientTLS{CA: z.CAPEM},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eg.CloseIdleConnections)
	return eg
}

func proxyConfig(z *zone) config.Proxy {
	return config.Proxy{
		URL:      z.ProxyURL,
		Username: proxyUser,
		Password: config.NewSecret(proxyPass),
	}
}

// seedLayer pushes a single-layer image and returns its layer digest and
// size — what the resumable blob path is addressed by.
func seedLayer(t *testing.T, eg *netx.Egress, reference string) (dgst digest.Digest, size int64) {
	t.Helper()
	img, err := random.Image(64<<10, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img, remote.WithTransport(eg.RoundTripper())); err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	h, err := layers[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	size, err = layers[0].Size()
	if err != nil {
		t.Fatal(err)
	}
	return digest.NewDigestFromEncoded(digest.Algorithm(h.Algorithm), h.Hex), size
}

// seedIndex pushes a two-platform image to the origin, as a standard
// client would — over the same transport, since there is no other way in.
func seedIndex(t *testing.T, eg *netx.Egress, reference string) {
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	for _, p := range []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}} {
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &p}})
	}
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	ref, err := name.ParseReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx, remote.WithTransport(eg.RoundTripper())); err != nil {
		t.Fatal(err)
	}
}

// pushRetrieverArtifact publishes a desired-state document in the recipe
// envelope layout (RECIPE-SPEC §11.2 applied to a Retriever).
func pushRetrieverArtifact(t *testing.T, eg *netx.Egress, reference string, doc []byte) {
	t.Helper()
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, "application/vnd.tobby.retriever.config.v1+json")
	layer := static.NewLayer(doc, "application/vnd.tobby.retriever.layer.v1+yaml")
	img, err := mutate.AppendLayers(img, layer)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img, remote.WithTransport(eg.RoundTripper())); err != nil {
		t.Fatal(err)
	}
}

// cookedRecipe renders a minimal cooked recipe document — one pinned
// ingredient, which is what makes it publishable (RECIPE-SPEC §8).
func cookedRecipe(t *testing.T) []byte {
	t.Helper()
	raw, err := yaml.Marshal(spec.Recipe{
		APIVersion: spec.APIVersion,
		Kind:       spec.KindRecipe,
		Metadata:   spec.Metadata{Name: "wordpress", Version: "6.8.2"},
		Spec: spec.RecipeSpec{Ingredients: []spec.Ingredient{{
			Name:    "wordpress",
			Kind:    spec.IngredientContainerImage,
			Ref:     "docker.io/bitnami/wordpress",
			Version: "6.8.2",
			Digest:  "sha256:" + strings.Repeat("ab", 32),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// retrieverDocument renders a minimal valid desired-state document.
func retrieverDocument(t *testing.T) []byte {
	t.Helper()
	raw, err := yaml.Marshal(spec.Retriever{
		APIVersion: spec.APIVersion,
		Kind:       spec.KindRetriever,
		Metadata:   spec.Metadata{Name: "zone-source"},
		Spec: spec.RetrieverSpec{
			Cookbook: "registry.example.com/cookbook",
			Recipes:  []spec.RecipeSelector{{Name: "wordpress", Version: "6.8.2"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// openLocalStore is the destination side: an embedded store the import
// paths write into directly (ADR-0005).
func openLocalStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the local store: %v", err)
		}
	})
	return st
}

// runTask drives one task runner synchronously.
func runTask(t *testing.T, run tasks.Runner, task *tasks.Task) error {
	t.Helper()
	return run(t.Context(), task, slog.New(slog.DiscardHandler), func() {})
}
