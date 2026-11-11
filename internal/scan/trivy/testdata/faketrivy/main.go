// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// faketrivy stands in for the trivy executable in the adapter's unit
// tests. It answers with what the real binary sends — the report it
// prints is a real Trivy 0.76 report, trimmed (testdata/alpine-3.10.0.json)
// — and it refuses, the way a test can see, any invocation that breaks the
// adapter's contract: a missing offline flag, an input that is not an OCI
// layout, a database that is not where --cache-dir says, or an
// environment carrying anything beyond the minimal set.
//
// Its behaviour is read from "<executable>.conf.json", because the
// adapter gives it no environment to be configured through.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type conf struct {
	Version string `json:"version"`
	Report  string `json:"report"`
	// Mode: "" (report), "fail", "sleep", "huge", "schema1".
	Mode string `json:"mode"`
	Log  string `json:"log"`
}

var allowedEnv = map[string]bool{
	"HOME": true, "TMPDIR": true, "XDG_CACHE_HOME": true, "PATH": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "http_proxy": true, "https_proxy": true,
	"NO_PROXY": true, "no_proxy": true,
	"TMP": true, "TEMP": true, "USERPROFILE": true, "SystemRoot": true,
}

var requiredFlags = []string{
	"--offline-scan", "--skip-db-update", "--skip-java-db-update", "--skip-check-update",
	"--skip-version-check", "--skip-vex-repo-update", "--disable-telemetry", "--quiet",
	"--list-all-pkgs=false",
}

func main() {
	exe, _ := os.Executable()
	raw, err := os.ReadFile(exe + ".conf.json")
	if err != nil {
		die("no configuration next to the fake: %v", err)
	}
	var c conf
	if err := json.Unmarshal(raw, &c); err != nil {
		die("bad configuration: %v", err)
	}
	args := os.Args[1:]
	logCall(c.Log, args)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !allowedEnv[k] {
			die("unexpected environment variable %s", k)
		}
		if strings.HasSuffix(strings.ToUpper(k), "_PROXY") && k != "NO_PROXY" && k != "no_proxy" && v != "http://127.0.0.1:9" {
			die("%s points somewhere: %q", k, v)
		}
	}
	if len(args) == 0 {
		die("no subcommand")
	}
	switch args[0] {
	case "version":
		if flag(args, "--format") != "json" {
			die("version without --format json")
		}
		fmt.Printf(`{"Version":%q,"VulnerabilityDB":null}`+"\n", c.Version)
	case "image":
		image(c, args)
	default:
		die("unknown subcommand %s", args[0])
	}
}

func image(c conf, args []string) {
	for _, f := range requiredFlags {
		if !has(args, f) {
			die("missing %s", f)
		}
	}
	if flag(args, "--format") != "json" || flag(args, "--scanners") != "vuln" || flag(args, "--cache-backend") != "memory" {
		die("unexpected --format, --scanners or --cache-backend")
	}
	input := flag(args, "--input")
	var idx struct {
		Manifests []struct{ Digest string } `json:"manifests"`
	}
	raw, err := os.ReadFile(filepath.Join(input, "index.json"))
	if err != nil || json.Unmarshal(raw, &idx) != nil || len(idx.Manifests) != 1 {
		die("--input %s is not a one-image OCI layout", input)
	}
	if _, err := os.Stat(filepath.Join(input, "oci-layout")); err != nil {
		die("--input has no oci-layout marker")
	}
	man := filepath.Join(input, "blobs", "sha256", strings.TrimPrefix(idx.Manifests[0].Digest, "sha256:"))
	if _, err := os.Stat(man); err != nil {
		die("the layout lacks its manifest blob")
	}
	cache := flag(args, "--cache-dir")
	for _, f := range []string{"trivy.db", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(cache, "db", f)); err != nil {
			die("--cache-dir %s has no db/%s", cache, f)
		}
	}
	switch c.Mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "2026-11-11T00:00:00Z\tFATAL\tFatal error\trun error: image scan error: scan error: unable to initialize a scan service")
		os.Exit(1)
	case "sleep":
		time.Sleep(time.Minute)
	case "huge":
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 200; i++ {
			fmt.Print(chunk)
		}
		return
	case "schema1":
		fmt.Println(`{"SchemaVersion":1,"Results":[]}`)
		return
	}
	rep, err := os.ReadFile(c.Report)
	if err != nil {
		die("report fixture: %v", err)
	}
	_, _ = os.Stdout.Write(rep)
}

func logCall(path string, args []string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(map[string]any{"args": args, "env": os.Environ()})
	_, _ = f.Write(append(line, '\n'))
}

func has(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "faketrivy: "+format+"\n", a...)
	os.Exit(3)
}
