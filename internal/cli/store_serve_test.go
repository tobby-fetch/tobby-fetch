// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check/checktest"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// bootInstance starts a real instance over cfg and returns its base URL;
// the instance stops with the test.
func bootInstance(t *testing.T, cfg *config.Config) string {
	t.Helper()
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)
	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	t.Cleanup(func() { serveHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg) }()
	t.Cleanup(func() {
		http.DefaultClient.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runServe returned %v, want nil", err)
		}
	})

	var srv *server.Server
	select {
	case srv = <-srvCh:
	case err := <-done:
		t.Fatalf("runServe exited before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runServe never built its server")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if addr := srv.Addr(); addr != "" {
			resp, err := http.Get("http://" + addr + "/readyz")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return "http://" + addr
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("instance never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func apiCall(t *testing.T, method, target string) (status int, payload []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "test-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func defectKeys(l []integrity.Defect) []string {
	out := make([]string, 0, len(l))
	for i := range l {
		out = append(out, string(l[i].Code)+" "+l[i].Path)
	}
	sort.Strings(out)
	return out
}

// TestInstanceVerifiesItsStoreThroughTheAPIAndTheScreen is the FR-057
// acceptance played on a running instance: the acceptance store (a
// truncated blob, a manifest missing a referenced blob, a signature whose
// key is no longer a trust root, an orphan) is verified from the API, the
// report names each defect, the screen shows the same, the command line
// run on the same directory reports the same defects, the metrics count
// the verification and the audit trail records it — and a recipe admitted
// unsigned by a declared scope carries its marker in the recipe listing and
// the reduced-assurance metric.
func TestInstanceVerifiesItsStoreThroughTheAPIAndTheScreen(t *testing.T) {
	f := checktest.New(t)
	acc := f.BuildAcceptance()
	bare, _ := f.AddRecipe("bare", nil)
	f.AdmitUnder(&bare, "lab")
	rootPEM, err := f.KP.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}

	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	cfg.Storage.Root = f.Root
	cfg.Trust = config.Trust{
		Roots: []config.TrustRoot{{Name: "a-root", Key: string(rootPEM)}},
		Scopes: []config.TrustScope{{
			Name: "lab", Repositories: []string{"registry.example.com/cookbook/bare"}, AllowUnsigned: true,
		}},
	}
	// A token for the command line to drive the instance with.
	accounts, err := auth.Open(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := accounts.CreateToken("cli", auth.RoleOperator, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAPIToken, secret)
	base := bootInstance(t, &cfg)

	// Nothing verified yet: an explicit null, not an error.
	code, raw := apiCall(t, http.MethodGet, base+"/api/v1/store/verification")
	if code != http.StatusOK || !strings.Contains(string(raw), `"verification":null`) {
		t.Fatalf("before any verification: %d %s", code, raw)
	}

	code, raw = apiCall(t, http.MethodPost, base+"/api/v1/store/verify")
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/store/verify = %d %s, want 201", code, raw)
	}
	var created struct {
		Task struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Task.Type != "store-verify" {
		t.Errorf("task type = %q", created.Task.Type)
	}

	// Wait for the verdict through the API, like a script would.
	var status struct {
		TaskID       string            `json:"task_id"`
		Status       string            `json:"status"`
		Verification *integrity.Report `json:"verification"`
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, raw = apiCall(t, http.MethodGet, base+"/api/v1/store/verification")
		status = struct {
			TaskID       string            `json:"task_id"`
			Status       string            `json:"status"`
			Verification *integrity.Report `json:"verification"`
		}{}
		if err := json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
		if status.Verification != nil && !status.Verification.Running() && status.Status != "running" && status.Status != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the verification never finished: %s", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
	rep := status.Verification
	if status.TaskID != created.Task.ID || status.Status != "failed" || rep.Verdict != integrity.VerdictDefects {
		t.Fatalf("status = %s %s, verdict %q, want the failed task %s with defects", status.TaskID, status.Status, rep.Verdict, created.Task.ID)
	}
	want := []string{
		string(taxonomy.CodeIntegrityBlobDigest) + " " + checktest.BlobPath(acc.Truncated),
		string(taxonomy.CodeIntegrityIncomplete) + " " + acc.MissingRecord.Ingredients[0].Repo + "@" + acc.MissingRecord.Ingredients[0].Digest,
		string(taxonomy.CodeSignature) + " removedkey@1.0.0",
	}
	sort.Strings(want)
	if got := defectKeys(rep.Defects); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("defects through the API:\n  got  %v\n  want %v", got, want)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Code != taxonomy.CodeIntegrityOrphan || rep.Findings[0].Path != checktest.BlobPath(acc.Orphan) {
		t.Errorf("findings = %+v, want the orphan blob named", rep.Findings)
	}
	var bareRow *integrity.Recipe
	for i := range rep.Recipes {
		if rep.Recipes[i].Name == "bare" {
			bareRow = &rep.Recipes[i]
		}
	}
	if bareRow == nil || !bareRow.Assurance.Reduced() || bareRow.Assurance.Scope != "lab" {
		t.Errorf("bare row = %+v, want the reduced-assurance marker re-reported", bareRow)
	}

	// The task page and the task API carry the same report.
	code, raw = apiCall(t, http.MethodGet, base+"/api/v1/tasks/"+created.Task.ID)
	if code != http.StatusOK || !strings.Contains(string(raw), `"verification"`) || !strings.Contains(string(raw), `"origin":"127.0.0.1`) {
		t.Errorf("the task document lacks the report or the origin: %d %s", code, raw)
	}

	// The screen: signed in like an operator, reading the same report.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.PostForm(base+"/login", url.Values{"username": {"admin"}, "password": {"test-password"}, "next": {"/"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	page := func(path string) string {
		r, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close() //nolint:errcheck // read side
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}
	screen := page("/integrity")
	for _, w := range []string{"3 defects found", "TBY-INT-001", checktest.BlobPath(acc.Truncated), "TBY-INT-002", "TBY-SIG-001", "removedkey@1.0.0", checktest.BlobPath(acc.Orphan)} {
		if !strings.Contains(screen, w) {
			t.Errorf("the screen does not carry %q", w)
		}
	}
	if !strings.Contains(screen, `data-assurance="reduced"`) {
		t.Error("the screen does not re-report the reduced-assurance marker")
	}
	if recipes := page("/recipes"); !strings.Contains(recipes, `data-assurance="reduced"`) {
		t.Error("the recipe listing does not carry the reduced-assurance marker")
	}

	// The metrics: the verification is counted and dated, and the recipe
	// under reduced assurance is counted per scope.
	_, raw = apiCall(t, http.MethodGet, base+"/metrics")
	metrics := string(raw)
	for _, w := range []string{
		`tobby_integrity_verifications_total{verdict="defects"} 1`,
		`tobby_integrity_verifications_total{verdict="clean"} 0`,
		"tobby_integrity_last_verification_timestamp_seconds ",
		`tobby_assurance_reduced_recipes{scope="lab"} 1`,
	} {
		if !strings.Contains(metrics, w) {
			t.Errorf("/metrics lacks %q", w)
		}
	}

	// The command line, run on the same directory, reports the same
	// defects: one verification, three surfaces.
	cliCfg := storeVerifyConfig(t, f.Root, "  scopes:\n    - name: lab\n      repositories: [\"registry.example.com/cookbook/bare\"]\n      allowUnsigned: true\n", f.KP)
	stdout, _, cerr := runSplit(t, "store", "verify", "--config", cliCfg, "--output", "json")
	if cerr == nil {
		t.Error("the command line verified a damaged store clean")
	}
	var cliRep integrity.Report
	if err := json.Unmarshal([]byte(stdout), &cliRep); err != nil {
		t.Fatal(err)
	}
	if got := defectKeys(cliRep.Defects); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("defects through the command line:\n  got  %v\n  want %v", got, want)
	}

	// `--instance --wait`: the command drives the RUNNING instance, follows
	// the task and exits on its outcome — against the real API, so what the
	// command decodes is what the instance really sends (B-031).
	instCfg := storeVerifyConfig(t, f.Root, "", f.KP)
	stdout, _, ierr := runSplit(t, "store", "verify", "--config", instCfg, "--instance", base, "--wait", "--output", "json")
	if ierr == nil || exitCodeFor(ierr) != taxonomy.ExitVerification {
		t.Errorf("--instance --wait on a damaged store = %v, want the verification exit class", ierr)
	}
	var remote integrity.Report
	if err := json.Unmarshal([]byte(stdout), &remote); err != nil {
		t.Fatalf("--instance --wait did not print the report: %v\n%s", err, stdout)
	}
	if got := defectKeys(remote.Defects); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("defects through --instance:\n  got  %v\n  want %v", got, want)
	}
	assertDocument(t, "tobby store verify", stdout, "")
	// Without --wait: the task, as the schema says.
	stdout, _, ierr = runSplit(t, "store", "verify", "--config", instCfg, "--instance", base, "--output", "json")
	if ierr != nil {
		t.Errorf("--instance without --wait = %v, want the task created and exit 0", ierr)
	}
	assertDocument(t, "tobby store verify", stdout, "")
	if !strings.Contains(stdout, `"waited": false`) || !strings.Contains(stdout, `"type": "store-verify"`) {
		t.Errorf("trigger document = %s", stdout)
	}

	// A store an instance is serving is refused by the in-process form,
	// with the way out in the message — not merely discouraged in a page.
	servedCfg := storeVerifyConfig(t, f.Root, "", f.KP)
	body, rerr := os.ReadFile(servedCfg) //nolint:gosec // G304: a temp file this test wrote
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := os.WriteFile(servedCfg, append(body, []byte("state:\n  root: "+strconv.Quote(cfg.State.Root)+"\nserver:\n  addr: "+strconv.Quote(strings.TrimPrefix(base, "http://"))+"\n")...), 0o600); err != nil { //nolint:gosec // G703: a temp file this test wrote
		t.Fatal(err)
	}
	_, _, serr := runSplit(t, "store", "verify", "--config", servedCfg)
	if !isTaxonomy(serr, taxonomy.CodeConfigInvalid) || !strings.Contains(serr.Error(), "--instance") {
		t.Errorf("store verify on a served store = %v, want a refusal naming --instance", serr)
	}

	// Let the verification the trigger above left behind finish.
	for range 600 {
		_, raw = apiCall(t, http.MethodGet, base+"/api/v1/store/verification")
		if strings.Contains(string(raw), `"status":"failed"`) || strings.Contains(string(raw), `"status":"done"`) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// One at a time is enforced on the instance: the second verification
	// is accepted only because the first has finished.
	if code, raw = apiCall(t, http.MethodPost, base+"/api/v1/store/verify"); code != http.StatusCreated {
		t.Errorf("a verification after the first finished = %d %s, want 201", code, raw)
	}
}
