// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// tfile is one file of a fabricated chart archive.
type tfile struct {
	name string
	data []byte
}

// buildTgz fabricates a gzip'd tar archive in memory.
func buildTgz(t *testing.T, files []tfile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// simpleChartTgz fabricates a dependency-free chart package.
func simpleChartTgz(t *testing.T, name, version string) []byte {
	t.Helper()
	return buildTgz(t, []tfile{
		{name + "/Chart.yaml", []byte(fmt.Sprintf("name: %s\nversion: %s\n", name, version))},
		{name + "/values.yaml", []byte("replicaCount: 1\n")},
	})
}

// chartIndexYAML renders a minimal Helm repository index.
func chartIndexYAML(entries map[string][]helmIndexEntry) []byte {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("apiVersion: v1\nentries:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s:\n", n)
		for _, e := range entries[n] {
			fmt.Fprintf(&b, "    - version: %q\n      urls:\n", e.Version)
			for _, u := range e.URLs {
				fmt.Fprintf(&b, "        - %q\n", u)
			}
			if e.Digest != "" {
				fmt.Fprintf(&b, "      digest: %q\n", e.Digest)
			}
		}
	}
	return []byte(b.String())
}

// sha256Hex returns the hex sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// serveChartRepo serves fabricated repository files over plain HTTP; the
// tests opt the host into registries.insecure (FR-075). The files map must
// be complete before the call — it is read concurrently afterwards.
func serveChartRepo(t *testing.T, files map[string][]byte) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestChartRepoInspectAndImport is the FR-024 acceptance: an
// https://<base>/<chart> reference resolves through index.yaml (highest
// stable version without an explicit one), converts to the standard OCI
// chart artifact, lands under the relocated path tagged with the version,
// and re-inspects as up-to-date (FR-026).
func TestChartRepoInspectAndImport(t *testing.T) {
	ctx := context.Background()
	newest := simpleChartTgz(t, "gitea", "1.2.3")
	older := simpleChartTgz(t, "gitea", "1.0.0")
	files := map[string][]byte{
		"/charts/gitea-1.2.3.tgz": newest,
		"/charts/gitea-1.0.0.tgz": older,
	}
	files["/charts/index.yaml"] = chartIndexYAML(map[string][]helmIndexEntry{
		"gitea": {
			{Version: "1.2.3", URLs: []string{"gitea-1.2.3.tgz"}, Digest: "sha256:" + sha256Hex(newest)},
			{Version: "1.0.0", URLs: []string{"gitea-1.0.0.tgz"}},
			{Version: "2.0.0-rc.1", URLs: []string{"gitea-2.0.0-rc.1.tgz"}},
		},
	})
	u := serveChartRepo(t, files)
	opts := WithInsecureHosts([]string{u.Host})
	dst := destStore(t)
	ref := u.String() + "/charts/gitea"

	rep, err := Inspect(ctx, ref, dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Kind != KindChart || rep.MultiArch || rep.Tag != "1.2.3" {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.HasSuffix(rep.Repository, "/charts/gitea") || !strings.Contains(rep.Repository, "_") {
		t.Errorf("relocated repository = %q, want <host>_<port>/charts/gitea", rep.Repository)
	}
	if len(rep.Platforms) != 1 {
		t.Fatalf("platforms = %+v", rep.Platforms)
	}
	p := rep.Platforms[0]
	if p.Name() != "artifact" || p.Status != StatusNew || p.SizeBytes <= 0 ||
		p.Digest != rep.IndexDigest || !strings.HasPrefix(p.Digest, "sha256:") {
		t.Errorf("platform = %+v (index digest %s)", p, rep.IndexDigest)
	}

	task := runTask(t, dst, ref, itemsFromReport(rep, nil), opts)
	if agg := task.Aggregate(); agg.Done != 1 || agg.Failed != 0 {
		t.Fatalf("aggregates = %+v (error %+v, items %+v)", agg, task.Error, task.Items)
	}

	// The converted artifact: the exact digest the inspection pinned, the
	// helm config and content media types, the archive bit-exact.
	payload, mediaType, dgst, err := dst.RawManifest(ctx, rep.Repository, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if dgst != rep.IndexDigest || !strings.Contains(mediaType, "oci.image.manifest") {
		t.Errorf("stored manifest digest %s (media type %s), want %s", dgst, mediaType, rep.IndexDigest)
	}
	var man struct {
		Config struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(payload, &man); err != nil {
		t.Fatal(err)
	}
	if man.Config.MediaType != helmConfigMediaType || len(man.Layers) != 1 ||
		man.Layers[0].MediaType != helmChartContentMediaType ||
		man.Layers[0].Digest != "sha256:"+sha256Hex(newest) {
		t.Errorf("converted manifest = %+v", man)
	}

	// Idempotence (FR-026): the conversion is deterministic, a
	// re-inspection sees the store up to date.
	rep2, err := Inspect(ctx, ref, dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Platforms[0].Status != StatusUpToDate {
		t.Errorf("re-inspection status = %s, want up-to-date", rep2.Platforms[0].Status)
	}

	// An explicit version picks its exact entry.
	rep3, err := Inspect(ctx, u.String()+"/charts/gitea:1.0.0", dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep3.Tag != "1.0.0" || rep3.Platforms[0].Status != StatusNew {
		t.Errorf("explicit version report = %+v", rep3)
	}
}

// TestChartRepoErrorTaxonomy locks the FR-024 failure mapping: unknown
// chart, unsatisfiable version, tampered archive (index digest check), and
// the FR-075 refusal of plain http without the per-host opt-in.
func TestChartRepoErrorTaxonomy(t *testing.T) {
	ctx := context.Background()
	good := simpleChartTgz(t, "gitea", "1.2.3")
	evil := simpleChartTgz(t, "gitea", "6.6.6")
	files := map[string][]byte{
		"/charts/gitea-1.2.3.tgz": good,
		// The served bytes do not match the digest the index announces.
		"/charts/tampered-1.0.0.tgz": evil,
	}
	files["/charts/index.yaml"] = chartIndexYAML(map[string][]helmIndexEntry{
		"gitea":    {{Version: "1.2.3", URLs: []string{"gitea-1.2.3.tgz"}}},
		"tampered": {{Version: "1.0.0", URLs: []string{"tampered-1.0.0.tgz"}, Digest: "sha256:" + sha256Hex(good)}},
	})
	u := serveChartRepo(t, files)
	opts := WithInsecureHosts([]string{u.Host})

	check := func(name, ref string, want taxonomy.Code, o ...Option) {
		t.Helper()
		_, err := Inspect(ctx, ref, nil, o...)
		var te *taxonomy.Error
		if !errors.As(err, &te) || te.Code() != want {
			t.Errorf("%s: err = %v, want %s", name, err, want)
		}
	}
	check("unknown chart", u.String()+"/charts/absent", taxonomy.CodeRefNotFound, opts)
	check("unsatisfiable version", u.String()+"/charts/gitea:9.9.9", taxonomy.CodeVersionResolve, opts)
	check("tampered archive", u.String()+"/charts/tampered", taxonomy.CodeDigestMismatch, opts)
	// Plain http without the per-host registries.insecure opt-in (FR-075).
	check("http refused", u.String()+"/charts/gitea", taxonomy.CodeBadReference)
	check("missing index", "http://127.0.0.1:1/charts/gitea", taxonomy.CodeRegistryUnreachable,
		WithInsecureHosts([]string{"127.0.0.1:1"}))
}
