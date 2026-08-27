// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import "testing"

// The path validators, exercised directly (NFR-011). The end-to-end test
// proves a hostile manifest blocks the medium; this one proves each shape
// is refused for its own reason, including the ones no fixture would
// produce by accident.

func TestCheckInventoryPathRefusesHostileShapes(t *testing.T) {
	for _, p := range []string{
		"",
		"..",
		"../etc/passwd",
		"meta/../../etc/passwd",
		"/meta/recipes.json",
		"meta//recipes.json",
		"meta/./recipes.json",
		"meta/recipes.json/",
		`meta\recipes.json`,
		"C:/meta/recipes.json",
		"meta/recipes.json\x00",
		"meta/rec\x01ipes.json",
		"_tobby/tasks/t.json",
		"logs/operation.log",
		"docker/registry/v3/blobs",
		ManifestPath,
	} {
		if err := checkInventoryPath(p); err == nil {
			t.Errorf("checkInventoryPath(%q) accepted a path it must refuse", p)
		}
	}
}

func TestCheckInventoryPathAcceptsCoveredPaths(t *testing.T) {
	for _, p := range []string{
		"meta/recipes.json",
		"meta/format.json",
		"docker/registry/v2/blobs/sha256/ab/abcd/data",
		"docker/registry/v2/repositories/docker.io/library/nginx/_manifests/tags/1.0/current/link",
	} {
		if err := checkInventoryPath(p); err != nil {
			t.Errorf("checkInventoryPath(%q) refused a legitimate path: %v", p, err)
		}
	}
}

func TestCheckRepoNameRefusesTraversal(t *testing.T) {
	for _, repo := range []string{
		"", "..", "../../etc", "docker.io/../../etc", "docker.io//library",
		"docker.io/library/", "/docker.io/library", "docker.io/lib rary",
		"docker.io/library/nginx:latest", `docker.io\library`,
	} {
		if err := checkRepoName(repo); err == nil {
			t.Errorf("checkRepoName(%q) accepted a name it must refuse", repo)
		}
	}
	for _, repo := range []string{
		"docker.io/library/nginx",
		"registry.example.com_5000/team/app",
		"base/docker.io/bitnami/wordpress",
	} {
		if err := checkRepoName(repo); err != nil {
			t.Errorf("checkRepoName(%q) refused a legitimate name: %v", repo, err)
		}
	}
}

func TestCheckTag(t *testing.T) {
	for _, tag := range []string{"", ".hidden", "-dash", "with/slash", "with space", "tag\x00"} {
		if err := checkTag(tag); err == nil {
			t.Errorf("checkTag(%q) accepted a tag it must refuse", tag)
		}
	}
	for _, tag := range []string{"1.0.0", "latest", "sha256-abcd.sig", "v1_2-3"} {
		if err := checkTag(tag); err != nil {
			t.Errorf("checkTag(%q) refused a legitimate tag: %v", tag, err)
		}
	}
}

func TestDigestHex(t *testing.T) {
	good := "sha256:" + hex64('a')
	h, err := digestHex(good)
	if err != nil || h != hex64('a') {
		t.Fatalf("digestHex(%q) = %q, %v", good, h, err)
	}
	for _, d := range []string{
		"", "abcd", "sha512:" + hex64('a'), "sha256:" + hex64('A'),
		"sha256:tooshort", "sha256:" + hex64('a') + "0",
		"sha256:../../etc/passwd",
	} {
		if _, err := digestHex(d); err == nil {
			t.Errorf("digestHex(%q) accepted a digest it must refuse", d)
		}
	}
}

func TestHexOfBlobPath(t *testing.T) {
	h := hex64('a')
	if got, ok := hexOfBlobPath(blobPath(h)); !ok || got != h {
		t.Errorf("hexOfBlobPath(blobPath(h)) = %q, %v", got, ok)
	}
	for _, p := range []string{
		"meta/recipes.json",
		"docker/registry/v2/blobs/sha256/ab/" + h + "/data", // prefix disagrees with the digest
		blobsPrefix + "/" + h[:2] + "/" + h + "/link",
	} {
		if _, ok := hexOfBlobPath(p); ok {
			t.Errorf("hexOfBlobPath(%q) claimed a content address", p)
		}
	}
}

func hex64(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
