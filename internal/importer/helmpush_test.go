// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
)

// helmManifest is a manifest payload served under an explicit media type.
type helmManifest struct {
	bytes     []byte
	mediaType types.MediaType
}

func (m helmManifest) RawManifest() ([]byte, error)        { return m.bytes, nil }
func (m helmManifest) MediaType() (types.MediaType, error) { return m.mediaType, nil }

// TestImportChartPushedByHelm (B-056): the unit import (FR-023) stores a
// chart as `helm push` publishes it — an OCI manifest without the optional
// top-level "mediaType" field — bit-exactly under its digest. Before the
// fix the manifest was stored under the empty type taken from that field,
// and the store refused it (TBY-STO-002, "unsupported manifest media type").
func TestImportChartPushedByHelm(t *testing.T) {
	host := upstream(t)
	dst := destStore(t)
	ctx := context.Background()

	// Reuse the chart content of chartImage, then republish its manifest
	// in helm's shape: same config and layer, no top-level mediaType.
	img := chartImage(t, true)
	ref := host + "/charts/wordpress:19.2.6"
	repo, err := name.NewRepository(host + "/charts/wordpress")
	if err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range layers {
		if err := remote.WriteLayer(repo, l); err != nil {
			t.Fatal(err)
		}
	}
	rawCfg, err := img.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteLayer(repo, static.NewLayer(rawCfg, helmConfigMediaType)); err != nil {
		t.Fatal(err)
	}
	man, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	man.MediaType = "" // what helm push writes
	payload, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`"mediaType":"application/vnd.oci.image.manifest`)) ||
		bytes.Contains(payload, []byte(`"mediaType":"application/vnd.docker.distribution.manifest`)) {
		t.Fatal("fixture: the manifest must omit its top-level mediaType")
	}
	tagged, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(tagged, helmManifest{bytes: payload, mediaType: types.OCIManifestSchema1}); err != nil {
		t.Fatal(err)
	}

	rep, err := Inspect(ctx, ref, dst)
	if err != nil {
		t.Fatal(err)
	}
	task := runTask(t, dst, ref, itemsFromReport(rep, nil))
	if agg := task.Aggregate(); agg.Done != 1 || agg.Failed != 0 {
		t.Fatalf("helm-pushed chart import = %+v / %+v", task.Error, task.Items)
	}
	got, _, dgst, err := dst.RawManifest(ctx, rep.Repository, "19.2.6")
	if err != nil {
		t.Fatal(err)
	}
	if want := digest.FromBytes(payload).String(); dgst != want || !bytes.Equal(got, payload) {
		t.Errorf("stored manifest %s, want the bit-exact helm payload %s", dgst, want)
	}
}
