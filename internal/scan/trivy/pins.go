// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

// PinnedVersion is the Trivy release this build of Tobby is qualified
// against (ADR-0008 decision 4: a pinned external binary). Bumped by
// hand, with the digests below, and tracked by renovate through the
// matching entries of melange.yaml and .github/workflows/ci.yml — the
// three must move together.
//
// The digests are those of the `trivy` executable inside the upstream
// release archives (trivy_<version>_<os>-<arch>.tar.gz / .zip), each
// archive itself checked against the release's published checksums file
// before extraction. They pin:
//
//   - the binary the container image bundles at ImagePath (melange
//     installs it from the same archive, with the same expected
//     checksum), verified before every execution;
//   - an explicit scan.trivy.path configured without scan.trivy.digest,
//     which is accepted only when it IS this release.
//
// A binary delivered by the tobby-system channel is pinned by its recipe
// instead (the ingredient digest, signed by the site's own chain); it may
// therefore be a newer Trivy than this table, which is the point of the
// channel — Tobby's release cadence is not tied to Trivy's.
const PinnedVersion = "0.76.2"

// pinnedDigests maps "os/arch" to the sha256 of the pinned executable.
// windows/arm64 is absent because upstream publishes no such build: an
// instance there runs in the visible degraded mode unless the site's
// channel delivers one.
var pinnedDigests = map[string]string{
	"linux/amd64":   "sha256:6a972c69606c7b3ea4c6bdcc6fc9332ea9ad38381624310c1e85cc77345909f3",
	"linux/arm64":   "sha256:7844c3851b28470b6d7f0975fa0b3262c5b2272c9a54a91f522801bb8926f51f",
	"windows/amd64": "sha256:dd07adf881a72b54d1a922191d62fa939938df2155be85a727da93b67b24e617",
	"darwin/arm64":  "sha256:7a15c426f963473a485a4f315e21f8e2c881269d500bf0de35da13a4421fa108",
}

// PinnedDigest returns the pinned executable digest for a platform.
func PinnedDigest(goos, goarch string) (string, bool) {
	d, ok := pinnedDigests[goos+"/"+goarch]
	return d, ok
}

// ImagePath is where the container image installs the pinned binary
// (melange.yaml). Outside the image the path does not exist.
const ImagePath = "/usr/libexec/tobby/trivy"

// Default nominal references of the tobby-system ingredients this adapter
// consumes (ADR-0008 decision 3). They are the upstream repositories, so
// a site's tobby-system recipe can reference them as they are published;
// scan.trivy.ref and scan.dbRepository point elsewhere when the site
// re-publishes them.
const (
	// DefaultBinaryRef is a multi-platform image that is also a valid
	// FileSet (RECIPE-SPEC §7.4): the executable sits at
	// usr/local/bin/trivy in its linux children.
	DefaultBinaryRef = "ghcr.io/aquasecurity/trivy"
	// DefaultDBRef is the vulnerability database artifact, schema 2.
	DefaultDBRef = "ghcr.io/aquasecurity/trivy-db"
	// DBTag is the database schema tag Trivy reads.
	DBTag = "2"
)

// Media types of the database artifact, as upstream publishes it.
const (
	mediaTypeDBLayer = "application/vnd.aquasec.trivy.db.layer.v1.tar+gzip"
)
