// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestUnitImportResumesALargeLayer is the wiring proof for R-29 on the
// import path: the mechanism is only worth anything if the runner
// actually reaches it, and a package that resumes perfectly while nobody
// calls it is the failure mode this test exists to prevent.
//
// The source is a real registry behind a front that cuts the first blob
// response in half — the ordinary enterprise-link failure. The assertion
// is arithmetic on what the origin put on the wire: the layer crossed
// once, not one and a half times.
func TestUnitImportResumesALargeLayer(t *testing.T) {
	const rawSize = 1 << 20
	src := newCuttingRegistry(t, rawSize/2)
	ref := src.host + "/library/big:1"

	img, err := random.Image(rawSize, 1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(parsed, img); err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	layerDigest, err := layers[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	// The transferred blob is the COMPRESSED layer, which is what the
	// manifest declares and what the resume is measured against.
	layerSize, err := layers[0].Size()
	if err != nil {
		t.Fatal(err)
	}
	src.arm(layerDigest.String())

	dst := destStore(t)
	resume := blobfetch.New(netx.Direct(), nil, t.TempDir(), 1)
	task := runTask(t, dst, ref, []tasks.Item{{Name: "artifact"}}, WithResume(resume))

	if task.Items[0].Status != tasks.StatusDone {
		t.Fatalf("item = %+v, want done", task.Items[0])
	}
	if served, want := src.blobBytes(), layerSize; served != want {
		t.Errorf("the origin served %d bytes for a %d-byte layer: %d were re-downloaded",
			served, want, served-want)
	}

	// The mechanism is reported, not merely performed (FR-029): the task
	// detail must say which blob moved, that it finished, and that it
	// resumed rather than restarted.
	var row *tasks.BlobProgress
	for i := range task.Items[0].Blobs {
		if task.Items[0].Blobs[i].Digest == layerDigest.String() {
			row = &task.Items[0].Blobs[i]
		}
	}
	if row == nil {
		t.Fatalf("the resumed layer is not on the task item: %+v", task.Items[0].Blobs)
	}
	if !row.Done {
		t.Errorf("blob row = %+v, want Done", row)
	}
	if !row.Resumed {
		t.Errorf("blob row = %+v, want Resumed: the screen would show a restart", row)
	}
	if row.ReceivedBytes != layerSize {
		t.Errorf("received = %d, want %d", row.ReceivedBytes, layerSize)
	}
}

// TestUnitImportWithoutAResumerIsUnchanged: the default path must stay
// exactly what it was. No resumer, no spool, no per-blob rows — and the
// import still works.
func TestUnitImportWithoutAResumerIsUnchanged(t *testing.T) {
	host := upstream(t)
	ref := host + "/library/plain:1"
	img, err := random.Image(4096, 1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(parsed, img); err != nil {
		t.Fatal(err)
	}
	task := runTask(t, destStore(t), ref, []tasks.Item{{Name: "artifact"}})
	if task.Items[0].Status != tasks.StatusDone {
		t.Fatalf("item = %+v, want done", task.Items[0])
	}
	if len(task.Items[0].Blobs) != 0 {
		t.Errorf("blob rows = %+v, want none on the plain streaming path", task.Items[0].Blobs)
	}
}

// cuttingRegistry is a real registry whose first blob GET is truncated
// mid-body — the connection drop this whole feature answers.
type cuttingRegistry struct {
	host string

	mu     sync.Mutex
	cut    int64
	target string
	served int64
}

func newCuttingRegistry(t *testing.T, cut int64) *cuttingRegistry {
	t.Helper()
	c := &cuttingRegistry{cut: cut}
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		target := c.target
		c.mu.Unlock()
		if r.Method != http.MethodGet || target == "" || !strings.HasSuffix(r.URL.Path, target) {
			inner.ServeHTTP(w, r)
			return
		}
		// The inner registry honors Range on its own; it must not, or the
		// slice below would range a range. This front decides the
		// partial response, so the inner one always yields the whole blob.
		full := r.Clone(r.Context())
		full.Header.Del("Range")
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, full)
		body := rec.Body.Bytes()
		start := int64(0)
		if spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
			s, _, _ := strings.Cut(spec, "-")
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n <= int64(len(body)) {
				start = n
			}
		}
		chunk := body[start:]
		if start > 0 {
			w.Header().Set("Content-Range",
				"bytes "+itoa(start)+"-"+itoa(int64(len(body))-1)+"/"+itoa(int64(len(body))))
		}
		w.Header().Set("ETag", `"stable"`)
		c.mu.Lock()
		if c.cut > 0 && int64(len(chunk)) > c.cut {
			chunk, c.cut = chunk[:c.cut], 0
		}
		c.mu.Unlock()
		if start > 0 {
			w.WriteHeader(http.StatusPartialContent)
		}
		n, _ := w.Write(chunk)
		c.mu.Lock()
		c.served += int64(n)
		c.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.host = u.Host
	return c
}

// arm points the front at the blob it must cut. Seeding happens through
// the same server, so it can only be armed once the content is pushed.
func (c *cuttingRegistry) arm(dgst string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target, c.served = dgst, 0
}

func (c *cuttingRegistry) blobBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.served
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
