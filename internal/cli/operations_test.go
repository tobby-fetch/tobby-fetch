// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/clock"
	"github.com/tobby-fetch/tobby-fetch/internal/diag"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// runCmd executes the CLI in this process and returns its streams and error.
func runCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := New()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func readArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name], _ = io.ReadAll(tr)
	}
}

// TestDiagnosticsBundleOnTheHostRedactsAndOmitsWhatItCannotKnow: a bundle
// taken with no instance running holds the documented members it can read
// from disk, none of the planted secrets, and states what it leaves out.
func TestDiagnosticsBundleOnTheHostRedactsAndOmitsWhatItCannotKnow(t *testing.T) {
	const (
		needleProxy = "host-proxy-pa55word-0001"
		needleEnv   = "env-api-token-value-0002"
	)
	t.Setenv("TOBBY_NETWORK_PROXY_PASSWORD", needleProxy)
	t.Setenv("TOBBY_API_TOKEN", needleEnv)
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	state := filepath.Join(dir, "state")
	for _, d := range []string{storeDir, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "bundle.tar.gz")
	stdout, stderr, err := runCmd(t, "diagnostics", "bundle", "--mode", "passthrough",
		"--storage-root", storeDir, "--state-root", state, "--proxy-url", "http://proxy.example:3128",
		"-o", file, "--output", "json")
	if err != nil {
		t.Fatalf("bundle: %v\n%s", err, stderr)
	}
	var rep bundleReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the report: %v\n%s", err, stdout)
	}
	if rep.Source != "host" || rep.File != file || rep.Bytes == 0 {
		t.Errorf("report = %+v", rep)
	}
	members := readArchive(t, file)
	for name, data := range members {
		for _, secret := range []string{needleProxy, needleEnv} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("member %s holds a planted secret", name)
			}
		}
	}
	if !bytes.Contains(members[diag.MemberConfig], []byte("REDACTED")) {
		t.Errorf("config.yaml does not show the redaction:\n%s", members[diag.MemberConfig])
	}
	omitted := map[string]string{}
	for _, o := range rep.Omitted {
		omitted[o.Name] = o.Reason
	}
	for _, name := range []string{diag.MemberLogs, diag.MemberMetrics} {
		if !strings.Contains(omitted[name], "--instance") {
			t.Errorf("%s: the omission does not say how to get it: %q", name, omitted[name])
		}
	}
	if _, ok := members[diag.MemberState]; !ok {
		t.Error("the state inventory is missing")
	}
	// An existing file is never overwritten.
	if _, _, err := runCmd(t, "diagnostics", "bundle", "--mode", "passthrough", "--storage-root", storeDir,
		"--state-root", state, "-o", file); err == nil {
		t.Error("a second bundle overwrote the first")
	}
	// The audit event of the request went to stderr, naming the local actor.
	if !strings.Contains(stderr, `"action":"diagnostics.bundle"`) || !strings.Contains(stderr, `"actor":"local"`) {
		t.Errorf("no diagnostics.bundle audit event on stderr:\n%s", stderr)
	}
}

// TestDiagnosticsBundleFailsClosedOnAnUnreadableSecretFile: a configured
// secret file that exists and cannot be read is a secret the redaction
// cannot know — the bundle is not produced.
func TestDiagnosticsBundleFailsClosedOnAnUnreadableSecretFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file this user cannot read")
	}
	dir := t.TempDir()
	creds := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(creds, []byte(`{"auths":{}}`), 0o000); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "bundle.tar.gz")
	_, _, err := runCmd(t, "diagnostics", "bundle", "--mode", "passthrough",
		"--storage-root", filepath.Join(dir, "s"), "--state-root", filepath.Join(dir, "st"),
		"--config", writeConfig(t, dir, "registries:\n  credentialsFile: "+creds+"\n"), "-o", file)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeDiagnosticsFailed {
		t.Fatalf("err = %v, want TBY-DIA-001", err)
	}
	if _, serr := os.Stat(file); serr == nil {
		t.Error("a bundle was written although a secret file could not be read")
	}
}

// destinationConfig writes the configuration of a destination instance
// serving the zone every fixture medium is addressed to.
func destinationConfig(t *testing.T, root string) string {
	t.Helper()
	return writeConfig(t, t.TempDir(), "mode: mirror\nzone: "+servedZone+"\nstorage:\n  root: "+strconv.Quote(root)+"\n")
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "tobby.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStateInspectListsTheLayoutAndFlagsABroadSecret: the inventory is the
// documented layout, and a secret entry readable by everyone is the refusal
// a start would give (exit class 3, TBY-STA-002).
func TestStateInspectListsTheLayoutAndFlagsABroadSecret(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "accounts.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runCmd(t, "state", "inspect", "--state-root", state, "--output", "json")
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, stderr)
	}
	var doc struct {
		Items []struct {
			Name    string `json:"name"`
			Present bool   `json:"present"`
			Class   string `json:"class"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range doc.Items {
		names = append(names, it.Name)
		if it.Name == "accounts.yaml" && (!it.Present || it.Class != "identity") {
			t.Errorf("accounts.yaml = %+v", it)
		}
	}
	if len(names) < 9 {
		t.Errorf("only %v listed", names)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(filepath.Join(state, "accounts.yaml"), 0o644); err != nil { //nolint:gosec // G302: the test loosens the file on purpose
		t.Fatal(err)
	}
	_, _, err = runCmd(t, "state", "inspect", "--state-root", state)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeStatePermissions {
		t.Fatalf("err = %v, want TBY-STA-002 for a world-readable accounts file", err)
	}
}

// resolvedMedium produces an empty medium whose manifest says it was
// resolved at the given time.
func resolvedMedium(t *testing.T, at time.Time) string {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecipeRecord(&store.RecipeRecord{Name: "seed", Version: "0", Digest: "sha256:" + strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRecipeRecord("seed", "0"); err != nil {
		t.Fatal(err)
	}
	if _, err := media.Write(context.Background(), st, media.WriteOptions{Zone: servedZone, RunID: "run_seed", ResolvedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestMediaVerifyWarnsWhenTheClockIsBehindTheMedium is the FR-096 half of
// the CLI: a medium resolved after the local clock, beyond the tolerance,
// is warned about on stderr with TBY-CLK-002 naming it, recorded as a
// clock.implausible audit event, and changes no verdict.
func TestMediaVerifyWarnsWhenTheClockIsBehindTheMedium(t *testing.T) {
	future := time.Now().Add(72 * time.Hour)
	root := resolvedMedium(t, future)
	stdout, stderr, err := runCmd(t, "media", "verify", "--config", destinationConfig(t, root), "--output", "json")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, stderr)
	}
	if strings.Contains(stdout, "CLK") {
		t.Errorf("the warning polluted the report on stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "TBY-CLK-002") || !strings.Contains(stderr, "warning:") {
		t.Errorf("no TBY-CLK-002 warning on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, `"action":"clock.implausible"`) {
		t.Errorf("no clock.implausible audit event:\n%s", stderr)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil || rep["verdict"] != "pushable" {
		t.Errorf("the clock changed the verdict or the report: %v %v", rep["verdict"], err)
	}

	// Within the tolerance (the default is an hour): nothing is raised.
	root = resolvedMedium(t, time.Now().Add(20*time.Minute))
	_, stderr, err = runCmd(t, "media", "verify", "--config", destinationConfig(t, root), "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "CLK") || strings.Contains(stderr, "clock.implausible") {
		t.Errorf("a clock within the tolerance raised something:\n%s", stderr)
	}
}

// TestABlockedMediumIsNoClockAnchor (review A5): the resolution time of a
// medium whose verification blocked is not believed — a forged date must
// neither raise the banner nor become the anchor.
func TestABlockedMediumIsNoClockAnchor(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	mon := clock.NewMonitor(time.Hour)
	mon.Now = func() time.Time { return now }
	late := now.Add(30 * 24 * time.Hour)
	observeMediumClock(mon, &media.Report{Verdict: media.VerdictBlocked, Media: &media.Info{MediaID: "forged", ResolvedAt: late}})
	if len(mon.Records()) != 0 {
		t.Errorf("a blocked medium became an anchor: %+v", mon.Records())
	}
	observeMediumClock(mon, &media.Report{Verdict: media.VerdictPartial, Media: &media.Info{MediaID: "real", ResolvedAt: late}})
	if imp := mon.Implausible(); len(imp) != 1 || imp[0].Medium != "real" {
		t.Errorf("a verified medium is not an anchor: %+v", imp)
	}
}

// TestARemoteBundleThatIsNoArchiveIsNotKept (review R6): an answer of 200
// that is not a diagnostic archive — a proxy's page, a truncated stream —
// is removed, and the error says what to check.
func TestARemoteBundleThatIsNoArchiveIsNotKept(t *testing.T) {
	for name, body := range map[string][]byte{
		"html page": []byte("<html>login</html>"),
		"truncated": func() []byte {
			b, err := diag.Build(diag.Options{Secrets: diag.NewSecrets()}, &diag.Sources{Version: func() ([]byte, error) { return []byte(`{"v":1}`), nil }})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := b.Archive()
			if err != nil {
				t.Fatal(err)
			}
			return raw[:len(raw)-20]
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/gzip")
				_, _ = w.Write(body)
			}))
			t.Cleanup(srv.Close)
			out := filepath.Join(t.TempDir(), "b.tar.gz")
			err := execute(t, "diagnostics", "bundle", "--mode", "passthrough", "--storage-root", t.TempDir(), "--instance", srv.URL, "-o", out)
			if got := taxonomyCode(t, err); got != taxonomy.CodeDiagnosticsFailed {
				t.Fatalf("err = %v, want TBY-DIA-001", err)
			}
			if !strings.Contains(taxonomy.Text("en", err.(*taxonomy.Error)), "--instance") { //nolint:errorlint // the assertion is the test's point
				t.Errorf("the error is not actionable: %v", err)
			}
			if _, serr := os.Stat(out); serr == nil {
				t.Error("the answer was kept")
			}
		})
	}
}

// TestARemoteBundleDownloadIsBounded (R6): an answer longer than any
// archive stops the copy with an error.
func TestARemoteBundleDownloadIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("z"), 4096))
	}))
	t.Cleanup(srv.Close)
	c := &instanceClient{base: srv.URL, http: srv.Client()}
	var sink bytes.Buffer
	if _, err := c.download(context.Background(), http.MethodPost, "/", &sink, 1000); err == nil {
		t.Error("an answer past the bound was accepted")
	}
	if sink.Len() > 1001 {
		t.Errorf("%d bytes copied past a bound of 1000", sink.Len())
	}
}
