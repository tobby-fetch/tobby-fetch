// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const oidcIssuer = "https://token.actions.githubusercontent.com"

type ghAsset struct {
	Name string `json:"name"`
}

type ghRelease struct {
	Assets []ghAsset `json:"assets"`
	Body   string    `json:"body"`
}

var (
	binaryAssetRe  = regexp.MustCompile(`^tobby-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)
	packageAssetRe = regexp.MustCompile(`^tobby_.+\.(deb|rpm|apk)$`)
	sbomAssetRe    = regexp.MustCompile(`^tobby-.+\.cdx\.json$`)
)

const (
	provenanceAsset = "tobby.intoto.jsonl"
	quickBinary     = "tobby-linux-amd64"
)

// kit files are signed like the SBOMs, from the release that ships them.
var kitAssets = []string{"openapi.yaml", "metrics-reference.json"}

// releasePlan is what a release publishes and this check verifies, derived
// from the asset list.
type releasePlan struct {
	binaries   []string
	packages   []string
	sboms      []string // each with a sibling .bundle
	kit        []string // each with a sibling .bundle
	provenance bool
}

func planRelease(assets []ghAsset, quick bool) releasePlan {
	have := map[string]bool{}
	for _, a := range assets {
		have[a.Name] = true
	}
	var p releasePlan
	p.provenance = have[provenanceAsset]
	for _, a := range assets {
		switch {
		case binaryAssetRe.MatchString(a.Name):
			p.binaries = append(p.binaries, a.Name)
		case packageAssetRe.MatchString(a.Name):
			p.packages = append(p.packages, a.Name)
		case sbomAssetRe.MatchString(a.Name) && have[a.Name+".bundle"]:
			p.sboms = append(p.sboms, a.Name)
		}
	}
	for _, k := range kitAssets {
		if have[k] && have[k+".bundle"] {
			p.kit = append(p.kit, k)
		}
	}
	if quick {
		p.binaries = filterOnly(p.binaries, quickBinary)
		p.packages = nil
		p.sboms = filterOnly(p.sboms, quickBinary+".cdx.json")
		p.kit = nil
	}
	return p
}

func filterOnly(in []string, keep string) []string {
	for _, s := range in {
		if s == keep {
			return []string{s}
		}
	}
	return nil
}

func (p *releasePlan) downloads() []string {
	var out []string
	if p.provenance {
		out = append(out, provenanceAsset)
	}
	out = append(out, p.binaries...)
	out = append(out, p.packages...)
	for _, s := range p.sboms {
		out = append(out, s, s+".bundle")
	}
	for _, k := range p.kit {
		out = append(out, k, k+".bundle")
	}
	return out
}

// images are the immutable digests the release notes state.
type images struct {
	image, chart, fileset string
}

func parseImages(body, repo string) images {
	owner, name, _ := strings.Cut(repo, "/")
	find := func(path string) string {
		re := regexp.MustCompile(`ghcr\.io/` + regexp.QuoteMeta(owner+"/"+path) + `@sha256:[0-9a-f]{64}`)
		return re.FindString(body)
	}
	return images{
		image:   find(name),
		chart:   find("charts/tobby"),
		fileset: find("binaries/tobby"),
	}
}

func cmdline(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// supplyChain is criterion 4. Every command is the one of
// docs/release-verification.md, run on the artifacts of the published
// release; registry access is anonymous (an empty DOCKER_CONFIG).
func (c *checker) supplyChain() *result {
	r := &result{id: 4, title: "SLSA Build L3 provenance verifiable (slsa-verifier); signatures verifiable (cosign)"}
	if !c.cmd.has("gh") {
		r.unverifiable("gh is absent, the release assets cannot be fetched; follow docs/release-verification.md sections 1, 3, 4 and 5 for %s", c.version)
		return r
	}
	vr := c.gh("release", "view", c.version, "-R", c.repo, "--json", "assets,body")
	if !vr.success() {
		if strings.Contains(vr.output(), "release not found") || strings.Contains(vr.output(), "HTTP 404") {
			r.ko("no published release for %s in %s", c.version, c.repo)
		} else {
			r.unverifiable("gh release view failed: %s", lastLine(vr.output(), 160))
		}
		return r
	}
	var rel ghRelease
	if err := json.Unmarshal([]byte(vr.stdout), &rel); err != nil {
		r.unverifiable("unreadable gh release view output: %v", err)
		return r
	}
	plan := planRelease(rel.Assets, c.quick)
	imgs := parseImages(rel.Body, c.repo)
	if !plan.provenance {
		r.ko("release asset %s is missing", provenanceAsset)
	}
	if len(plan.binaries) == 0 {
		r.ko("the release carries no tobby-<os>-<arch> binary")
	}

	work, err := os.MkdirTemp("", "release-criteria-*")
	if err != nil {
		r.unverifiable("cannot create a work directory: %v", err)
		return r
	}
	defer func() { _ = os.RemoveAll(work) }()
	// An empty Docker config: no stored credential can make a private
	// package look public.
	anon := filepath.Join(work, "docker-config")
	if err := os.MkdirAll(anon, 0o750); err != nil {
		r.unverifiable("cannot create an empty DOCKER_CONFIG: %v", err)
		return r
	}
	assets := filepath.Join(work, "assets")
	if err := os.MkdirAll(assets, 0o750); err != nil {
		r.unverifiable("cannot create a download directory: %v", err)
		return r
	}
	env := []string{"DOCKER_CONFIG=" + anon}

	args := []string{"release", "download", c.version, "-R", c.repo, "-D", assets, "--clobber"}
	for _, n := range plan.downloads() {
		args = append(args, "-p", n)
	}
	ctx, cancel := c.withTimeout(10 * time.Minute)
	dl := c.cmd.run(ctx, c.dir, nil, "gh", args...)
	cancel()
	if !dl.success() {
		r.unverifiable("download of the release assets failed: %s", lastLine(dl.output(), 160))
		return r
	}

	slug := "github.com/" + c.repo
	identity := "https://github.com/" + c.repo + "/.github/workflows/release.yml@refs/tags/" + c.version
	regexpID := "^https://github.com/" + c.repo + "/"

	verifyArtifact := func(f string) {
		c.tool(r, assets, env, f+" provenance", "slsa-verifier", "verify-artifact", f,
			"--provenance-path", provenanceAsset, "--source-uri", slug, "--source-tag", c.version)
	}
	for _, f := range plan.binaries {
		verifyArtifact(f)
	}
	for _, f := range plan.packages {
		verifyArtifact(f)
	}
	for _, f := range append(append([]string{}, plan.sboms...), plan.kit...) {
		c.tool(r, assets, env, f+" cosign bundle", "cosign", "verify-blob", "--new-bundle-format",
			"--bundle", f+".bundle", "--certificate-oidc-issuer", oidcIssuer,
			"--certificate-identity", identity, f)
	}

	if imgs.image == "" {
		r.ko("the release notes state no container image digest (ghcr.io/%s@sha256:…)", c.repo)
	} else {
		c.tool(r, work, env, "image signature", "cosign", "verify", imgs.image,
			"--certificate-oidc-issuer", oidcIssuer, "--certificate-identity-regexp", regexpID)
		c.tool(r, work, env, "image provenance", "slsa-verifier", "verify-image", imgs.image,
			"--source-uri", slug, "--source-tag", c.version)
	}
	if imgs.chart != "" {
		c.tool(r, work, env, "chart signature", "cosign", "verify", imgs.chart,
			"--certificate-oidc-issuer", oidcIssuer, "--certificate-identity-regexp", regexpID)
	}
	if imgs.fileset != "" {
		c.tool(r, work, env, "FileSet signature", "cosign", "verify", imgs.fileset,
			"--certificate-oidc-issuer", oidcIssuer, "--certificate-identity", identity)
		c.tool(r, work, env, "FileSet provenance", "slsa-verifier", "verify-image", imgs.fileset,
			"--source-uri", slug, "--source-tag", c.version)
		c.filesetAttestation(r, work, env, imgs.fileset, identity)
	}
	return r
}

// filesetAttestation checks the SBOM attestation on one child of the
// binaries FileSet (release-verification.md section 5); it needs crane to
// resolve the child digest.
func (c *checker) filesetAttestation(r *result, work string, env []string, fileset, identity string) {
	const label = "FileSet SBOM attestation"
	if !c.cmd.has("crane") {
		r.unverifiable("%s: crane is absent — run: crane digest --platform windows/amd64 %s, then cosign verify-attestation --type cyclonedx <that child> --certificate-oidc-issuer %s --certificate-identity %s",
			label, fileset, oidcIssuer, identity)
		return
	}
	ctx, cancel := c.withTimeout(2 * time.Minute)
	defer cancel()
	res := c.cmd.run(ctx, work, env, "crane", "digest", "--platform", "windows/amd64", fileset)
	child := strings.TrimSpace(res.stdout)
	if !res.success() || child == "" {
		r.unverifiable("%s: crane could not resolve the child digest (%s) — run: crane digest --platform windows/amd64 %s", label, lastLine(res.output(), 100), fileset)
		return
	}
	ref := strings.SplitN(fileset, "@", 2)[0] + "@" + child
	c.tool(r, work, env, label, "cosign", "verify-attestation", "--type", "cyclonedx", ref,
		"--certificate-oidc-issuer", oidcIssuer, "--certificate-identity", identity)
}

// tool runs one verification command, or says what to run when the tool is
// not installed: an absent verifier is never a pass.
func (c *checker) tool(r *result, dir string, env []string, label, name string, args ...string) {
	if !c.cmd.has(name) {
		r.unverifiable("%s: %s is absent — run: %s", label, name, cmdline(name, args...))
		return
	}
	ctx, cancel := c.withTimeout(5 * time.Minute)
	defer cancel()
	res := c.cmd.run(ctx, dir, env, name, args...)
	if !res.success() {
		if res.err != nil {
			r.unverifiable("%s: %s could not run: %v", label, name, res.err)
			return
		}
		r.ko("%s: %s exited %d: %s", label, name, res.exitCode, lastLine(res.output(), 200))
		return
	}
	r.ok("%s: %s verified", label, name)
}
