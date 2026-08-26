// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The command-line manual trigger of FR-014 (FR-066): `tobby sync`
// without --dry-run.
//
// It drives a running instance through /api/v1, so every test here stands
// a fake instance up and checks what the command sends and what it makes
// of the answer. What matters is not that an HTTP call happens — it is
// that the exit code an automation branches on comes from the TASK, on
// the instance, and not from whether the request succeeded.

// taskBody renders one task document the way /api/v1 serves it.
func taskBody(status, itemError string) string {
	items := "[]"
	if itemError != "" {
		items = `[{"name":"wordpress","status":"failed","error":{"code":"` + itemError + `","params":{"host":"docker.io"}}}]`
	}
	return `{"id":"tsk_1","run_id":"run_1","type":"sync","reference":"./retriever.yaml",` +
		`"actor":"local","status":"` + status + `","created":"2026-08-26T10:00:00Z",` +
		`"started":"2026-08-26T10:00:01Z","finished":"2026-08-26T10:00:09Z","items":` + items + `}`
}

// syncInstance stands up a fake instance. statuses are served to the task
// endpoint in order, the last one repeating; bodies records what the
// trigger posted.
func syncInstance(t *testing.T, itemError string, statuses ...string) (base string, posted *[]string) {
	t.Helper()
	var sent []string
	var polls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sync", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		sent = append(sent, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, taskBody(statuses[0], ""))
	})
	mux.HandleFunc("GET /api/v1/tasks/tsk_1", func(w http.ResponseWriter, _ *http.Request) {
		i := int(polls.Add(1))
		if i >= len(statuses) {
			i = len(statuses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task":`+taskBody(statuses[i], itemError)+`}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &sent
}

// TestSyncTriggerExitsOnTheTaskOutcome is the R-08 promise of `--wait`:
// the command's exit code is the TASK's outcome, not the HTTP call's. A
// pipeline that gets 0 because the POST returned 201 while the
// synchronization was refused by the allow-list is a pipeline that
// promotes broken content.
func TestSyncTriggerExitsOnTheTaskOutcome(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		itemError string
		want      int
	}{
		{"a finished task succeeds", "done", "", taxonomy.ExitOK},
		{"a policy refusal keeps its class", "failed", string(taxonomy.CodeNotAllowlisted), taxonomy.ExitPolicy},
		{"a verification failure keeps its class", "failed", string(taxonomy.CodeSignature), taxonomy.ExitVerification},
		{"anything else is an operational failure", "failed", string(taxonomy.CodeRegistryUnreachable), taxonomy.ExitFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := syncInstance(t, tc.itemError, tc.status)
			err := execute(t, "sync", "--instance", base, "--wait")
			if got := exitCodeFor(err); got != tc.want {
				t.Errorf("exit code = %d, want %d (err %v)", got, tc.want, err)
			}
		})
	}
}

// TestSyncTriggerWithoutWaitReportsAnUnfinishedTask: without --wait the
// command returns as soon as the instance accepted the trigger, and the
// report says so — a status of "running" that nothing marked as a
// snapshot would read as a finished run.
func TestSyncTriggerWithoutWaitReportsAnUnfinishedTask(t *testing.T) {
	base, _ := syncInstance(t, "", "running")
	stdout, stderr, err := runSplit(t, "sync", "--instance", base, "--output", outputJSON)
	if err != nil {
		t.Fatalf("trigger: %v (stderr %s)", err, stderr)
	}
	var report syncTriggerReport
	if uerr := json.Unmarshal([]byte(stdout), &report); uerr != nil {
		t.Fatalf("stdout is not the report: %v\n%s", uerr, stdout)
	}
	if report.Waited {
		t.Error("the report claims the command waited, and it did not")
	}
	if report.Task == nil || report.Task.ID != "tsk_1" {
		t.Fatalf("the report names no task: %s", stdout)
	}
	if report.Instance != base {
		t.Errorf("instance = %q, want %q — a report that does not name the zone it ran on is useless in a shared log", report.Instance, base)
	}
}

// TestSyncTriggerSendsPruneOnlyWhenAsked: absent and false are different
// instructions. An absent field means "do what this instance does by
// default"; a false one forbids removal for this run. Collapsing the two
// would silently disable a mirror instance's prune, or enable it.
func TestSyncTriggerSendsPruneOnlyWhenAsked(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"nothing said", nil, "{}"},
		{"asked for", []string{"--prune"}, `{"prune":true}`},
		{"forbidden", []string{"--prune=false"}, `{"prune":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, posted := syncInstance(t, "", "done")
			if err := execute(t, append([]string{"sync", "--instance", base}, tc.args...)...); err != nil {
				t.Fatalf("trigger: %v", err)
			}
			if len(*posted) != 1 {
				t.Fatalf("the instance received %d triggers, want 1", len(*posted))
			}
			if got := (*posted)[0]; got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSyncTriggerCarriesTheInstancesRefusal: the instance decides, the
// command reports. A 403 from the API arrives as its own taxonomy code
// and its own exit class, so a token without the operator role fails the
// way a local policy refusal would.
func TestSyncTriggerCarriesTheInstancesRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taxonomy.WriteProblem(w, r.Header.Get("Accept-Language"), taxonomy.New(taxonomy.CodeRoleDenied,
			taxonomy.Params{"role": "operator"}))
	}))
	t.Cleanup(srv.Close)

	err := execute(t, "sync", "--instance", srv.URL)
	if err == nil {
		t.Fatal("a refused trigger was reported as a success")
	}
	if got := taxonomyCode(t, err); got != taxonomy.CodeRoleDenied {
		t.Errorf("code = %s, want %s", got, taxonomy.CodeRoleDenied)
	}
	if got := exitCodeFor(err); got != taxonomy.ExitPolicy {
		t.Errorf("exit code = %d, want %d (policy)", got, taxonomy.ExitPolicy)
	}
}

// TestWaitForTaskPollsUntilTerminal: --wait is a wait, not a single look.
// A task that is still pending on the first read must be read again, and
// the value returned must be the terminal one.
func TestWaitForTaskPollsUntilTerminal(t *testing.T) {
	base, _ := syncInstance(t, "", "pending", "running", "done")
	client := &instanceClient{base: base, http: http.DefaultClient, poll: 5 * time.Millisecond}
	task, err := waitForTask(context.Background(), client, "tsk_1", 0)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if task.Active() {
		t.Errorf("waitForTask returned a task still in %s", task.Status)
	}
}

// TestWaitForTaskGivesUpWithoutLying: a deadline that expires stops the
// WAIT, not the synchronization. The message has to say which, and name
// the task, because the operator's next move is to go and look at it.
func TestWaitForTaskGivesUpWithoutLying(t *testing.T) {
	base, _ := syncInstance(t, "", "running")
	client := &instanceClient{base: base, http: http.DefaultClient, poll: 5 * time.Millisecond}
	task, err := waitForTask(context.Background(), client, "tsk_1", 20*time.Millisecond)
	if err == nil {
		t.Fatal("the wait returned success after its deadline")
	}
	if task == nil || task.ID != "tsk_1" {
		t.Error("the refusal does not carry the task it gave up on")
	}
	for _, want := range []string{"tsk_1", "still", base} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message misses %q: %s", want, err)
		}
	}
}

// TestWaitTimeoutWithoutWaitIsAUsageError: a flag that cannot do anything
// says so. Accepting it silently would let a pipeline believe it had
// bounded a wait it never asked for.
func TestWaitTimeoutWithoutWaitIsAUsageError(t *testing.T) {
	err := execute(t, "sync", "--instance", "http://127.0.0.1:1", "--wait-timeout", "1m")
	if got := exitCodeFor(err); got != taxonomy.ExitUsage {
		t.Errorf("exit code = %d, want %d (usage) — err %v", got, taxonomy.ExitUsage, err)
	}
}

// TestLocalInstanceURLReadsTheListenAddress: on the instance's own host —
// a cron entry, an operator at the console — nothing needs to be given.
// A listen address is a bind spec, so the wildcard forms resolve to the
// loopback rather than to a host called "0.0.0.0".
func TestLocalInstanceURLReadsTheListenAddress(t *testing.T) {
	cases := map[string]string{
		":8080":           "http://localhost:8080",
		"0.0.0.0:8080":    "http://localhost:8080",
		"127.0.0.1:9000":  "http://127.0.0.1:9000",
		"tobby.host:8443": "http://tobby.host:8443",
	}
	for addr, want := range cases {
		cfg := config.Default()
		cfg.Server.Addr = addr
		if got := localInstanceURL(&cfg); got != want {
			t.Errorf("%s → %q, want %q", addr, got, want)
		}
	}
	// A listener presenting a certificate is reached over TLS: an
	// http:// call to an https:// listener is a confusing failure, and
	// the configuration already says which one it is.
	cfg := config.Default()
	cfg.Server.Addr = ":8443"
	cfg.Server.TLS.CertFile = "/etc/tobby/tls.crt"
	if got := localInstanceURL(&cfg); got != "https://localhost:8443" {
		t.Errorf("with a listener certificate: %q, want https://localhost:8443", got)
	}
}

// TestRemoteRefusalIsPrintedAsTheInstanceWroteIt: a problem document
// carries rendered sentences, not the parameters behind them, so this
// build cannot re-render it — and must not try. The instance may run a
// different version and name a setting this binary never heard of; its
// own three parts are what an operator has to read.
//
// Asserted through the real process entry point, because what is under
// test is what lands on the operator's terminal: a version of Execute
// that fell through to the generic taxonomy renderer would print the
// catalog's own sentence with an empty parameter, and a test reading the
// error value rather than the stream would not notice.
func TestRemoteRefusalIsPrintedAsTheInstanceWroteIt(t *testing.T) {
	const detail = "retriever.source is empty on this instance"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taxonomy.WriteProblem(w, r.Header.Get("Accept-Language"),
			taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{"detail": detail}))
	}))
	t.Cleanup(srv.Close)

	stdout, stderr, code := runProcess(t, "sync", "--instance", srv.URL)
	if code != taxonomy.ExitFailure {
		t.Errorf("exit code = %d, want %d", code, taxonomy.ExitFailure)
	}
	if !strings.Contains(stderr, detail) {
		t.Errorf("the instance's own wording never reached the operator:\n%s", stderr)
	}
	if !strings.Contains(stderr, string(taxonomy.CodeConfigInvalid)) {
		t.Errorf("the refusal carries no code:\n%s", stderr)
	}
	if strings.Contains(stderr, "<no value>") {
		t.Errorf("the refusal was re-rendered from a document that carries no parameters:\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("a refusal wrote to stdout: %q", stdout)
	}

	// And the code still reaches errors.As, so every caller that branches
	// on a taxonomy code keeps working.
	err := execute(t, "sync", "--instance", srv.URL)
	if got := taxonomyCode(t, err); got != taxonomy.CodeConfigInvalid {
		t.Errorf("code = %s, want %s", got, taxonomy.CodeConfigInvalid)
	}
	var rp *remoteProblem
	if !errors.As(err, &rp) {
		t.Fatalf("error %T is not a remote problem: %v", err, err)
	}
}
