// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"strings"
	"testing"
)

const (
	dImg  = "sha256:8a2fce4dc89afabad2523aaf1eb9654b1e4caa316d352cc3966ef30ab19d8498"
	dChrt = "sha256:424158a3bcf70348c543600c04a80745ddf87136f3402c6b99545f78b829eec6"
	dSet  = "sha256:29c99e532f3c7692eac61f9f7eb791f1b42421ee47af6683edd021d4be054150"
)

const releaseJSON = `{"assets":[{"name":"tobby-linux-amd64"},{"name":"tobby-linux-amd64.cdx.json"},{"name":"tobby-linux-amd64.cdx.json.bundle"},
 {"name":"tobby-windows-amd64.exe"},{"name":"tobby-windows-amd64.exe.cdx.json"},{"name":"tobby-windows-amd64.exe.cdx.json.bundle"},
 {"name":"tobby_1.0.0_linux_amd64.deb"},{"name":"tobby.intoto.jsonl"},{"name":"SHA256SUMS"},{"name":"tobby.json"}],
 "body":"image ghcr.io/tobby-fetch/tobby-fetch@` + dImg + ` chart ghcr.io/tobby-fetch/charts/tobby@` + dChrt + ` set ghcr.io/tobby-fetch/binaries/tobby@` + dSet + `"}`

func TestPlanRelease(t *testing.T) {
	var rel ghRelease
	if err := jsonUnmarshal(releaseJSON, &rel); err != nil {
		t.Fatal(err)
	}
	p := planRelease(rel.Assets, false)
	if len(p.binaries) != 2 || len(p.packages) != 1 || len(p.sboms) != 2 || !p.provenance || len(p.kit) != 0 {
		t.Errorf("%+v", p)
	}
	q := planRelease(rel.Assets, true)
	if len(q.binaries) != 1 || q.binaries[0] != "tobby-linux-amd64" || len(q.packages) != 0 || len(q.sboms) != 1 {
		t.Errorf("quick plan: %+v", q)
	}
	imgs := parseImages(rel.Body, "tobby-fetch/tobby-fetch")
	if imgs.image != "ghcr.io/tobby-fetch/tobby-fetch@"+dImg || imgs.chart == "" || imgs.fileset == "" {
		t.Errorf("%+v", imgs)
	}
}

func supplyFake(tools ...string) *fakeCmd {
	f := newFake(append([]string{"gh", "git"}, tools...)...)
	f.answers["git rev-parse --verify --quiet v1.0.0^{commit}"] = okOut(sha)
	f.answers["gh release view v1.0.0 -R tobby-fetch/tobby-fetch --json assets,body"] = okOut(releaseJSON)
	f.answers["gh release download v1.0.0*"] = execResult{}
	return f
}

func TestSupplyChainAllGreen(t *testing.T) {
	f := supplyFake("slsa-verifier", "cosign", "crane")
	f.answers["slsa-verifier*"] = okOut("PASSED: SLSA verification passed\n")
	f.answers["cosign*"] = okOut("Verified OK\n")
	f.answers["crane digest*"] = okOut("sha256:" + strings.Repeat("a", 64) + "\n")
	r := newGitChecker(t, f).supplyChain()
	if r.status() != statusOK {
		t.Fatalf("%s", joined(r))
	}
	// 2 binaries + 1 package provenance, 2 sbom blobs, image x2, chart, fileset x2, attestation
	if len(r.details) != 3+2+2+1+2+1 {
		t.Errorf("%d checks:\n%s", len(r.details), joined(r))
	}
	for key, env := range f.envs {
		if strings.HasPrefix(key, "cosign verify ") || strings.HasPrefix(key, "slsa-verifier verify-image") {
			if len(env) != 1 || !strings.HasPrefix(env[0], "DOCKER_CONFIG=") {
				t.Errorf("registry access must use an empty DOCKER_CONFIG: %s env=%v", key, env)
			}
		}
	}
	if !f.called("cosign verify-blob --new-bundle-format --bundle tobby-linux-amd64.cdx.json.bundle --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity https://github.com/tobby-fetch/tobby-fetch/.github/workflows/release.yml@refs/tags/v1.0.0 tobby-linux-amd64.cdx.json") {
		t.Errorf("the SBOM command is not the documented one: %v", f.calls)
	}
	if !f.called("slsa-verifier verify-artifact tobby-linux-amd64 --provenance-path tobby.intoto.jsonl --source-uri github.com/tobby-fetch/tobby-fetch --source-tag v1.0.0") {
		t.Errorf("the binary command is not the documented one: %v", f.calls)
	}
}

func TestSupplyChainMissingToolsNeverOK(t *testing.T) {
	r := newGitChecker(t, supplyFake()).supplyChain()
	if r.status() != statusUnverifiable {
		t.Fatalf("%s", joined(r))
	}
	for _, d := range r.details {
		if d.status != statusUnverifiable || !strings.Contains(d.text, "run: ") {
			t.Errorf("each line must carry the command to run: %+v", d)
		}
	}
}

func TestSupplyChainFailureIsKO(t *testing.T) {
	f := supplyFake("slsa-verifier", "cosign")
	f.answers["slsa-verifier*"] = execResult{exitCode: 1, stderr: "FAILED: SLSA verification failed: source tag mismatch\n"}
	f.answers["cosign*"] = okOut("ok")
	r := newGitChecker(t, f).supplyChain()
	if r.status() != statusKO || !strings.Contains(joined(r), "source tag mismatch") {
		t.Errorf("%s", joined(r))
	}
}

func TestSupplyChainNoRelease(t *testing.T) {
	f := newFake("gh", "git")
	f.answers["git rev-parse --verify --quiet v1.0.0^{commit}"] = okOut(sha)
	f.answers["gh release view v1.0.0 -R tobby-fetch/tobby-fetch --json assets,body"] = execResult{exitCode: 1, stderr: "release not found"}
	if r := newGitChecker(t, f).supplyChain(); r.status() != statusKO {
		t.Errorf("%s", joined(r))
	}
}
