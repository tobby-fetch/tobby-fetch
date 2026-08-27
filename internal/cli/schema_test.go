// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/tobby-fetch/tobby-fetch/internal/clischema"
)

// The FR-066 acceptance, verbatim: "every reporting command accepts
// --output json and emits a document validating against its published
// schema".
//
// The schema is the one a running instance serves at
// GET /api/v1/cli-output.schema.json, beside the OpenAPI document. It is
// the contract, not a description of one — so it is checked against real
// command runs, on this side of the wire, rather than reviewed by eye.

// runJSON executes one command with --output json, keeping the streams
// apart, and returns stdout and stderr. The separation is the point:
// under --output json the document owns stdout alone (B-010), and a
// helper that merged the two would be unable to see the bug it exists to
// catch.
func runJSON(t *testing.T, stdin string, args ...string) (stdout, stderr string) {
	t.Helper()
	root := New()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	if stdin != "" {
		root.SetIn(strings.NewReader(stdin))
	}
	root.SetArgs(append(args, "--output", outputJSON))
	// The error is deliberately not asserted here: several of these
	// commands report AND fail — a plan with changes, an import of an
	// absent layout — and the report is exactly what has to be valid when
	// they do.
	_ = root.Execute()
	return out.String(), errOut.String()
}

// compileSchema builds the validator for one $defs entry of the published
// document.
func compileSchema(t *testing.T, def string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(clischema.Document()))
	if err != nil {
		t.Fatalf("the published schema is not JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	const url = "cli-output.schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(url + "#/$defs/" + strings.ReplaceAll(strings.ReplaceAll(def, "~", "~0"), "/", "~1"))
	if err != nil {
		t.Fatalf("compiling $defs/%s: %v\n"+
			"every reporting command needs an entry in internal/clischema/cli-output.schema.json", def, err)
	}
	return sch
}

// assertDocument checks that stdout carries exactly one JSON document, that
// it validates, and that nothing human leaked into it.
func assertDocument(t *testing.T, def, stdout, stderr string) {
	t.Helper()
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("%s: --output json wrote nothing to stdout (stderr: %s)", def, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%s: stdout is not a JSON document: %v\n%s", def, err, stdout)
	}
	if dec.More() {
		t.Errorf("%s: stdout carries more than one document — a log record or a second report joined it (B-010):\n%s", def, stdout)
	}
	if err := compileSchema(t, def).Validate(doc); err != nil {
		t.Errorf("%s: the report does not validate against its published schema:\n%v\n\ndocument:\n%s", def, err, stdout)
	}
}

// TestEveryReportedDocumentMatchesItsSchema runs each reporting command
// for real and validates what it wrote.
func TestEveryReportedDocumentMatchesItsSchema(t *testing.T) {
	t.Run("tobby version", func(t *testing.T) {
		stdout, stderr := runJSON(t, "", "version")
		assertDocument(t, "tobby version", stdout, stderr)
	})

	t.Run("tobby config dump", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(cfgPath, []byte("mode: mirror\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr := runJSON(t, "", "config", "dump", "--config", cfgPath)
		assertDocument(t, "tobby config dump", stdout, stderr)
	})

	t.Run("tobby user", func(t *testing.T) {
		state := t.TempDir()
		stdout, stderr := runJSON(t, "s3cret-first\n", "user", "add", "alice",
			"--state-root", state, "--password-stdin")
		assertDocument(t, "tobby user add", stdout, stderr)

		stdout, stderr = runJSON(t, "s3cret-second\n", "user", "passwd", "alice",
			"--state-root", state, "--password-stdin")
		assertDocument(t, "tobby user passwd", stdout, stderr)

		stdout, stderr = runJSON(t, "", "user", "list", "--state-root", state)
		assertDocument(t, "tobby user list", stdout, stderr)
	})

	t.Run("tobby fileset pack", func(t *testing.T) {
		stdout, stderr := runJSON(t, "", "fileset", "pack", packTree(t), "debs:1.0.0",
			"--storage-root", t.TempDir())
		assertDocument(t, "tobby fileset pack", stdout, stderr)
	})

	t.Run("tobby export", func(t *testing.T) {
		source, _ := seedStoreRoot(t)
		plan := filepath.Join(t.TempDir(), "planned.tar")
		stdout, stderr := runJSON(t, "", "export", plan, "--storage-root", source, "--dry-run")
		assertDocument(t, "tobby export --dry-run", stdout, stderr)

		out := filepath.Join(t.TempDir(), "payload.tar")
		stdout, stderr = runJSON(t, "", "export", out, "--storage-root", source)
		assertDocument(t, "tobby export", stdout, stderr)

		stdout, stderr = runJSON(t, "", "import", out, "--storage-root", t.TempDir())
		assertDocument(t, "tobby import", stdout, stderr)
	})

	t.Run("tobby media", func(t *testing.T) {
		root := emptyMedium(t)
		stdout, stderr := runJSON(t, "", "media", "verify", "--storage-root", root, "--zone", servedZone)
		assertDocument(t, "tobby media verify", stdout, stderr)

		registry := httptest.NewServer(nil)
		t.Cleanup(registry.Close)
		cfgPath := mediumConfig(t, root, servedZone, registry.Listener.Addr().String())
		stdout, stderr = runJSON(t, "", "media", "import", "--config", cfgPath)
		assertDocument(t, "tobby media import", stdout, stderr)
	})

	t.Run("tobby recipe push", func(t *testing.T) {
		addr, cfgPath := testCookbook(t)
		file := writeRecipe(t, validPushRecipe)
		stdout, stderr := runJSON(t, "", "recipe", "push", file,
			addr+"/cookbook/schema-check:1.0.0", "--config", cfgPath)
		assertDocument(t, "tobby recipe push", stdout, stderr)
	})

	t.Run("tobby sync --dry-run", func(t *testing.T) {
		stdout, stderr := runJSON(t, "", "sync", "--dry-run",
			"--retriever", unreachableRetriever(t),
			"--config", planConfig(t))
		assertDocument(t, "tobby sync --dry-run", stdout, stderr)
	})

	t.Run("tobby sync", func(t *testing.T) {
		instance := fakeInstance(t, "done")
		stdout, stderr := runJSON(t, "", "sync", "--instance", instance, "--wait")
		assertDocument(t, "tobby sync", stdout, stderr)
	})
}

// unreachableRetriever writes a candidate Retriever pointing at a cookbook
// nothing serves. The plan then reports what it could establish and files
// the rest as problems, which is exactly the shape a gate has to be able
// to read — and it needs no registry fixture to produce.
func unreachableRetriever(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retriever.yaml")
	body := `apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: schema-check
spec:
  cookbook: 127.0.0.1:1/cookbook
  recipes:
    - name: wordpress
      version: "1.0.0"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// planConfig is the configuration a local plan needs: a store to compare
// against, and the loopback cookbook marked insecure so the attempt is a
// connection refusal rather than a TLS complaint.
func planConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	// The store root is emitted as a quoted scalar: a Windows path is full
	// of backslashes and a colon after the drive letter, none of which a
	// plain YAML scalar is obliged to carry unchanged (NFR-018).
	body := "mode: mirror\nstorage:\n  root: " + strconv.Quote(t.TempDir()) +
		"\nregistries:\n  insecure: [\"127.0.0.1:1\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeInstance stands in for a running instance's /api/v1: it accepts the
// trigger and serves the created task, already in the requested terminal
// state. The command under test is the client, so the server only has to
// answer the two calls the contract makes.
func fakeInstance(t *testing.T, status string) string {
	t.Helper()
	const id = "tsk_schema"
	body := `{"id":"` + id + `","run_id":"run_schema","type":"sync","reference":"./retriever.yaml",` +
		`"actor":"local","status":"` + status + `","created":"2026-08-26T10:00:00Z",` +
		`"started":"2026-08-26T10:00:01Z","finished":"2026-08-26T10:00:09Z","items":[]}`
	srv := httptest.NewServer(newFakeInstanceHandler(t, id, body))
	t.Cleanup(srv.Close)
	return srv.URL
}

// validPushRecipe is a minimal cooked recipe: fully pinned, which is what
// a cookbook accepts.
const validPushRecipe = `apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: schema-check
  version: 1.0.0
spec:
  ingredients:
    - name: app
      kind: ContainerImage
      ref: docker.io/library/alpine
      version: "3.22.1"
      digest: sha256:0000000000000000000000000000000000000000000000000000000000000000
`

// newFakeInstanceHandler answers the two calls the trigger makes, BOTH
// wrapped the way the API wraps a single task. The POST used to answer a
// bare one — the second stub in this package to make that false claim
// about the server, and the reason B-031 shipped with a green suite.
func newFakeInstanceHandler(t *testing.T, id, task string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sync", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"task":`+task+`}`)
	})
	mux.HandleFunc("GET /api/v1/tasks/"+id, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task":`+task+`}`)
	})
	return mux
}
