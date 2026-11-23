// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Reference-scenario benchmark (DEVELOPMENT-PLAN §2.7, performance pass).
//
// Two scenarios, each measured over several repetitions on a fresh
// destination store, the source being a real embedded Tobby store behind a
// real listener (so the source side writes and reads the filesystem, as a
// registry does):
//
//   - a large multi-arch image: four platforms of three layers each, cold
//     (everything transferred) then warm (a second import: FR-026 says it
//     moves nothing, and the warm figure is what proves it is cheap);
//   - a chart whose dependencies must be vendored (FR-025): a parent chart
//     with a Chart.lock of vendored-count dependencies fetched from a chart
//     repository.
//
// It is a test only so that it can reuse the package's fixtures; it does
// nothing unless TOBBY_BENCH is set, and never runs in `go test ./...`:
//
//	TOBBY_BENCH=1 go test -run TestReferenceScenarios -v -timeout 30m ./internal/importer/
//
// Knobs (defaults in parentheses): TOBBY_BENCH_LAYER_MIB (32),
// TOBBY_BENCH_REPS (3), TOBBY_BENCH_CHART_DEPS (12). The figures are
// wall-clock on whatever machine runs it: compare runs of the same
// machine, not machines. Peak heap is the largest Go heap in use during the
// run above its starting point, sampled every 5 ms; the source registry
// lives in the same process, so it bounds the importer's own share from
// above.

func benchEnv(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

// heapPeak samples the heap until stop is called and returns the peak
// growth over the starting point, in bytes.
func heapPeak() (stop func() uint64) {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var (
		mu   sync.Mutex
		peak uint64
		done = make(chan struct{})
		wg   sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				mu.Lock()
				if m.HeapInuse > base.HeapInuse && m.HeapInuse-base.HeapInuse > peak {
					peak = m.HeapInuse - base.HeapInuse
				}
				mu.Unlock()
			}
		}
	}()
	return func() uint64 {
		close(done)
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}

type sample struct {
	d    time.Duration
	peak uint64
}

func median(s []sample) sample {
	c := append([]sample(nil), s...)
	sort.Slice(c, func(i, j int) bool { return c[i].d < c[j].d })
	return c[len(c)/2]
}

func mibPerSec(bytes int64, d time.Duration) float64 {
	return float64(bytes) / (1 << 20) / d.Seconds()
}

// sourceStore is a real embedded registry used as the upstream.
func sourceStore(t *testing.T) string {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(st.APIHandler())
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestReferenceScenarios(t *testing.T) {
	if os.Getenv("TOBBY_BENCH") == "" {
		t.Skip("set TOBBY_BENCH=1 to run the reference scenarios (performance pass)")
	}
	reps := benchEnv("TOBBY_BENCH_REPS", 3)
	t.Logf("go %s %s/%s, %d CPU", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	t.Run("multi-arch image", func(t *testing.T) {
		layerBytes := int64(benchEnv("TOBBY_BENCH_LAYER_MIB", 32)) << 20
		platforms := []v1.Platform{
			{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"},
			{OS: "linux", Architecture: "arm", Variant: "v7"}, {OS: "linux", Architecture: "ppc64le"},
		}
		const layers = 3
		host := sourceStore(t)
		var idx v1.ImageIndex = empty.Index
		for i := range platforms {
			img, err := random.Image(layerBytes, layers)
			if err != nil {
				t.Fatal(err)
			}
			idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &platforms[i]}})
		}
		ref := host + "/bench/large:1.0.0"
		r, err := name.ParseReference(ref, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.WriteIndex(r, idx); err != nil {
			t.Fatal(err)
		}
		total := layerBytes * layers * int64(len(platforms))

		var cold, warm []sample
		for range reps {
			dst := destStore(t)
			ctx := context.Background()
			stop := heapPeak()
			start := time.Now()
			rep, err := Inspect(ctx, ref, dst, mustSourcePolicy(t, config.Registries{Insecure: []string{host}}))
			if err != nil {
				t.Fatal(err)
			}
			task := runTask(t, dst, ref, itemsFromReport(rep, nil), mustSourcePolicy(t, config.Registries{Insecure: []string{host}}))
			if agg := task.Aggregate(); agg.Done != len(platforms) {
				t.Fatalf("cold import aggregates = %+v", agg)
			}
			cold = append(cold, sample{time.Since(start), stop()})

			stop = heapPeak()
			start = time.Now()
			rep, err = Inspect(ctx, ref, dst, mustSourcePolicy(t, config.Registries{Insecure: []string{host}}))
			if err != nil {
				t.Fatal(err)
			}
			task = runTask(t, dst, ref, itemsFromReport(rep, nil), mustSourcePolicy(t, config.Registries{Insecure: []string{host}}))
			if agg := task.Aggregate(); agg.Done != len(platforms) {
				t.Fatalf("warm import aggregates = %+v", agg)
			}
			warm = append(warm, sample{time.Since(start), stop()})
		}
		c, w := median(cold), median(warm)
		t.Logf("RESULT multi-arch %d platforms x %d layers x %d MiB = %d MiB", len(platforms), layers, layerBytes>>20, total>>20)
		t.Logf("RESULT   cold: %7.2fs  %7.1f MiB/s  peak heap +%d MiB   (median of %d)", c.d.Seconds(), mibPerSec(total, c.d), c.peak>>20, reps)
		t.Logf("RESULT   warm: %7.2fs  (nothing to transfer)       peak heap +%d MiB", w.d.Seconds(), w.peak>>20)
	})

	t.Run("chart with vendored dependencies", func(t *testing.T) {
		deps := benchEnv("TOBBY_BENCH_CHART_DEPS", 12)
		files := map[string][]byte{}
		var entries = map[string][]helmIndexEntry{}
		var chartYAML, lock strings.Builder
		fmt.Fprintf(&chartYAML, "name: platform\nversion: 1.0.0\ndependencies:\n")
		lock.WriteString("dependencies:\n")
		// 256 KiB of incompressible values per dependency: a chart archive
		// is gzip, and a repetitive pad would shrink to nothing.
		pad := make([]byte, 256<<10)
		if _, err := rand.Read(pad); err != nil {
			t.Fatal(err)
		}
		var depBytes int64
		for i := range deps {
			n := fmt.Sprintf("dep%02d", i)
			tgz := buildTgz(t, []tfile{
				{n + "/Chart.yaml", []byte(fmt.Sprintf("name: %s\nversion: 1.%d.0\n", n, i))},
				{n + "/values.yaml", pad},
			})
			files[fmt.Sprintf("/deps/%s-1.%d.0.tgz", n, i)] = tgz
			entries[n] = []helmIndexEntry{{Version: fmt.Sprintf("1.%d.0", i), URLs: []string{fmt.Sprintf("%s-1.%d.0.tgz", n, i)}, Digest: "sha256:" + sha256Hex(tgz)}}
			depBytes += int64(len(tgz))
			fmt.Fprintf(&chartYAML, "  - name: %s\n    version: 1.x\n    repository: REPO\n", n)
			fmt.Fprintf(&lock, "  - name: %s\n    version: 1.%d.0\n    repository: REPO\n", n, i)
		}
		files["/deps/index.yaml"] = chartIndexYAML(entries)
		du := serveChartRepo(t, files)
		depRepo := du.String() + "/deps"
		parent := buildTgz(t, []tfile{
			{"platform/Chart.yaml", []byte(strings.ReplaceAll(chartYAML.String(), "REPO", depRepo))},
			{"platform/Chart.lock", []byte(strings.ReplaceAll(lock.String(), "REPO", depRepo))},
			{"platform/values.yaml", []byte("replicaCount: 1\n")},
		})
		host := upstream(t)
		ref := host + "/charts/platform:1.0.0"
		pushChart(t, ref, parent)

		var runs []sample
		for range reps {
			dst := destStore(t)
			opts := mustSourcePolicy(t, config.Registries{Insecure: []string{du.Host, host}})
			stop := heapPeak()
			start := time.Now()
			rep, err := Inspect(context.Background(), ref, dst, opts)
			if err != nil {
				t.Fatal(err)
			}
			task := runTaskVendor(t, dst, ref, itemsFromReport(rep, nil), true, opts)
			if agg := task.Aggregate(); agg.Done != 1 || len(task.ChartDependencies) != deps {
				t.Fatalf("vendoring aggregates = %+v, %d dependencies reported (error %+v)", agg, len(task.ChartDependencies), task.Error)
			}
			runs = append(runs, sample{time.Since(start), stop()})
		}
		m := median(runs)
		t.Logf("RESULT chart, %d vendored dependencies (%d KiB of archives)", deps, depBytes>>10)
		t.Logf("RESULT   inspect + vendor + store: %7.2fs   peak heap +%d MiB   (median of %d)", m.d.Seconds(), m.peak>>20, reps)
	})
}
