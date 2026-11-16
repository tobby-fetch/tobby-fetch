// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// testVEX is an OpenVEX document with one admitted statement and one
// statement carrying only an impact statement.
var testVEX = `{"@context":"https://openvex.dev/ns/v0.2.0","@id":"https://vex.example/cli","author":"Site security",
"timestamp":"2026-11-16T10:00:00Z","version":1,"statements":[
{"vulnerability":{"name":"CVE-2024-44444"},"products":[{"@id":"docker.io/library/nginx@sha256:` + strings.Repeat("a", 64) + `"}],
 "status":"not_affected","justification":"vulnerable_code_not_in_execute_path"},
{"vulnerability":{"name":"CVE-2024-55555"},"products":[{"@id":"sha256:` + strings.Repeat("a", 64) + `"}],
 "status":"not_affected","impact_statement":"we think so"}]}`

func writeVEX(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "site.openvex.json")
	if err := os.WriteFile(path, []byte(testVEX), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// scanExceptionsConfig declares one active scoped exception, one expired
// one, and the VEX document.
func scanExceptionsConfig(vexPath string) string {
	return `scan:
  mode: block
  exceptions:
    - id: CVE-2024-11111
      justification: parser disabled in this build
      expires: 2099-01-01T00:00:00Z
      scope:
        recipe: wordpress
        repository: docker.io/bitnami/**
    - id: CVE-2024-22222
      justification: legacy
      expires: 2020-01-01T00:00:00Z
  vex:
    - path: ` + vexPath + `
      expires: 2099-01-01T00:00:00Z
`
}

func runScanExceptions(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	root := New()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"scan", "exceptions"}, args...))
	err := classifyUsage(root.Execute())
	return out.String(), err
}

// TestScanExceptionsFromConfiguration reads the instance configuration and
// its VEX documents offline: every exception with its origin, scope and
// state, the documents' counts, and the statement that the Retriever's
// exceptions are known only to a running instance.
func TestScanExceptionsFromConfiguration(t *testing.T) {
	t.Setenv(EnvInstanceURL, "")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	vexPath := writeVEX(t)
	if err := os.WriteFile(cfgPath, []byte(scanExceptionsConfig(vexPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runScanExceptions(t, nil, "--config", cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CVE-2024-11111", "instance", "active", "recipe=wordpress", "repository=docker.io/bitnami/**",
		"CVE-2024-22222", "expired",
		"CVE-2024-44444", "vex " + vexPath, "digest=sha256:aaaa", "repository=docker.io/library/nginx",
		"2 statements, 1 admitted, 1 ignored, 1 exceptions", "impact-statement-only",
		"known only to a running instance",
		"hardening.disableUnitImport=false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CVE-2024-55555\t") || strings.Contains(out, "  CVE-2024-55555 ") {
		t.Errorf("the impact-statement-only statement became an exception:\n%s", out)
	}

	out, err = runScanExceptions(t, nil, "--config", cfgPath, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Source         string `json:"source"`
		RetrieverKnown bool   `json:"retriever_known"`
		Exceptions     []struct{ ID, Origin, State string }
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Source != "configuration" || doc.RetrieverKnown || len(doc.Exceptions) != 3 {
		t.Errorf("document = %+v", doc)
	}
}

// TestScanExceptionsRefusesAnInvalidVEX fails exactly as serve refuses the
// start: TBY-SCN-027 naming the file.
func TestScanExceptionsRefusesAnInvalidVEX(t *testing.T) {
	t.Setenv(EnvInstanceURL, "")
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.openvex.json")
	if err := os.WriteFile(bad, []byte(`{"@context":"https://openvex.dev/ns/v0.2.0","statements":[],"extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("scan:\n  vex:\n    - path: "+bad+"\n      expires: 2099-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runScanExceptions(t, nil, "--config", cfgPath)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeScanVEXInvalid || !strings.Contains(te.Error(), "bad.openvex.json") {
		t.Fatalf("err = %v, want TBY-SCN-027 naming the file", err)
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

// TestScanExceptionsFromInstance reads a running instance's policy, the
// Retriever's exceptions included.
func TestScanExceptionsFromInstance(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/policy" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"evaluated_at":"2026-11-16T10:00:00Z","mode":"warn","threshold":7,"blocking":true,"retriever_known":true,
"exceptions":[{"id":"CVE-2024-33333","origin":"retriever","state":"unbounded"}],"vex_documents":[],
"hardening":{"disableUnitImport":true,"disableRegistryPush":false}}`))
	}))
	t.Cleanup(srv.Close)
	out, err := runScanExceptions(t, map[string]string{EnvAPIToken: "tok-viewer"}, "--instance", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok-viewer" {
		t.Errorf("Authorization = %q", auth)
	}
	for _, want := range []string{"running instance " + srv.URL, "CVE-2024-33333", "retriever", "unbounded", "hardening.disableUnitImport=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "known only to a running instance") {
		t.Errorf("the Retriever note is shown although the instance knows them:\n%s", out)
	}

	// Without --instance, the instance flags are a usage error.
	t.Setenv(EnvInstanceURL, "")
	if _, err := runScanExceptions(t, nil, "--token-file", "/x"); exitCodeFor(err) != taxonomy.ExitUsage {
		t.Errorf("--token-file without --instance: %v", err)
	}
}
