// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// auditRecords returns the audit records of one action in a JSON log.
func auditRecords(t *testing.T, logs *bytes.Buffer, action string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if rec[audit.KeyLogType] == audit.LogType && rec["action"] == action {
			out = append(out, rec)
		}
	}
	return out
}

// TestScanScopedInstanceException is the R-15 acceptance through the
// engine: an instance exception scoped to the vulnerable image's digest,
// repository or recipe admits it; scoped to anything else it does not,
// and the report says the exception is out of scope.
func TestScanScopedInstanceException(t *testing.T) {
	for name, c := range map[string]struct {
		scope  func(env *scanEnv) scan.ExceptionScope
		covers bool
	}{
		"digest":               {func(e *scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Digest: e.vulnDig} }, true},
		"other digest":         {func(e *scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Digest: e.cleanDig} }, false},
		"repository":           {func(e *scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Repository: e.hostRepo + "/images/*"} }, true},
		"other repository":     {func(e *scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Repository: e.hostRepo + "/other/*"} }, false},
		"recipe":               {func(*scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Recipe: "vuln"} }, true},
		"recipe version":       {func(*scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Recipe: "vuln@1.0.0"} }, true},
		"other recipe":         {func(*scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Recipe: "clean"} }, false},
		"all criteria, one no": {func(e *scanEnv) scan.ExceptionScope { return scan.ExceptionScope{Digest: e.vulnDig, Recipe: "clean"} }, false},
	} {
		t.Run(name, func(t *testing.T) {
			env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Mode: scan.ModeBlock}})
			sc := c.scope(env)
			env.eng.scan.Exceptions = []scan.Exception{
				{ID: "CVE-2024-0001", Justification: "accepted", Expires: scanNow.Add(time.Hour), Level: scan.LevelInstance, Scope: &sc},
			}
			task, err := runSync(t, env.eng)
			if err != nil {
				t.Fatal(err)
			}
			it := itemByName(t, task, "vuln@1.0.0/app")
			if got := it.Scan.Policy.Verdict == scan.VerdictPass; got != c.covers {
				t.Fatalf("covered = %v, want %v: %+v", got, c.covers, it.Scan.Policy)
			}
			if !c.covers && !slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanExceptionOutOfScope) {
				t.Errorf("the out-of-scope exception is not reported: %v", reasonCodes(it.Scan))
			}
		})
	}
}

// TestScanExpiryReblocksVisibly: the same image, admitted while its
// exception is in force, is refused at the next evaluation once the
// expiry has passed — with the expired exception named in the report, a
// scan.exception_expired audit record naming the item and the exception,
// and the re-block observed for the metric.
func TestScanExpiryReblocksVisibly(t *testing.T) {
	clock := scanNow
	reblocked := 0
	expires := scanNow.Add(time.Hour)
	env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{
		Mode: scan.ModeBlock,
		Exceptions: []scan.Exception{{ID: "CVE-2024-0001", Justification: "accepted", Expires: expires, Level: scan.LevelInstance,
			VEX: "/etc/tobby/site.openvex.json", Scope: &scan.ExceptionScope{Recipe: "vuln"}}},
		Observe: func(r *scan.Report) {
			if len(r.Reblocked()) > 0 {
				reblocked++
			}
		},
	}})
	env.eng.scan.Now = func() time.Time { return clock }

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	task := &tasks.Task{ID: "tsk_1", RunID: "run_1", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, env.eng, task, logger); err != nil {
		t.Fatal(err)
	}
	if it := itemByName(t, task, "vuln@1.0.0/app"); it.Scan.Policy.Verdict != scan.VerdictPass {
		t.Fatalf("before the expiry: %+v", it.Scan.Policy)
	}
	if n := len(auditRecords(t, &logs, audit.ActionScanExceptionExpired)); n != 0 || reblocked != 0 {
		t.Fatalf("re-blocked before the expiry: %d records, %d observed", n, reblocked)
	}

	clock = expires.Add(time.Minute)
	logs.Reset()
	task = &tasks.Task{ID: "tsk_2", RunID: "run_2", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, env.eng, task, logger); err != nil {
		t.Fatal(err)
	}
	it := itemByName(t, task, "vuln@1.0.0/app")
	if it.Scan.Policy.Verdict != scan.VerdictBlock || len(it.Scan.Policy.ExceptionsExpired) != 1 ||
		!slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanExceptionExpired) {
		t.Fatalf("after the expiry: %+v", it.Scan.Policy)
	}
	recs := auditRecords(t, &logs, audit.ActionScanExceptionExpired)
	if len(recs) != 1 {
		t.Fatalf("%d scan.exception_expired records, want 1:\n%s", len(recs), logs.String())
	}
	r := recs[0]
	if r["target"] != env.src.addr+"/images/vuln@"+env.vulnDig || r["outcome"] != audit.OutcomeDenied ||
		r["exception"] != "CVE-2024-0001" || r["exception_source"] != "vex:/etc/tobby/site.openvex.json" ||
		r["exception_expires"] != expires.UTC().Format(time.RFC3339) {
		t.Errorf("record = %v", r)
	}
	if n := len(auditRecords(t, &logs, audit.ActionScanBlock)); n != 1 {
		t.Errorf("%d scan.block records, want 1", n)
	}
	if reblocked != 1 {
		t.Errorf("re-blocks observed = %d, want 1", reblocked)
	}
}

// TestScanUnboundedRetrieverException: a v1alpha1 Retriever exception
// without expiry applies, the report says it is unbounded (TBY-SCN-028),
// and the engine exposes it for the policy view.
func TestScanUnboundedRetrieverException(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{
		settings: ScanSettings{Mode: scan.ModeBlock},
		policy:   &spec.ScanPolicy{Exceptions: []spec.ScanException{{ID: "CVE-2024-0001", Justification: "not reachable"}}},
	})
	if _, known := env.eng.RetrieverExceptions(); known {
		t.Error("Retriever exceptions are known before any run")
	}
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatal(err)
	}
	it := itemByName(t, task, "vuln@1.0.0/app")
	if it.Scan.Policy.Verdict != scan.VerdictPass || !slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanExceptionUnbounded) {
		t.Errorf("evaluation = %+v", it.Scan.Policy)
	}
	exs, known := env.eng.RetrieverExceptions()
	if !known || len(exs) != 1 || !exs[0].Unbounded() || exs[0].Source() != scan.SourceRetriever {
		t.Errorf("RetrieverExceptions = %+v, %v", exs, known)
	}
}
