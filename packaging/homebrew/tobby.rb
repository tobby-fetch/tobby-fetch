# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Homebrew formula for the macOS convenience tier (NFR-001 amendment
# 2026-08-12). Lives in this repository as the template; the published copy
# is seeded into the `tobby-fetch/homebrew-tap` repository as
# `Formula/tobby.rb`, and the release workflow bumps `version`, `url`, and
# the two `sha256` values on every tag (checksums come straight from the
# release's signed SHA256SUMS).
#
# Install:  brew install tobby-fetch/tap/tobby
#
# The formula pours the release binaries — the same reproducible,
# SBOM-and-provenance-carrying artifacts as every other target. Homebrew
# verifies the pinned sha256 on download; the binaries carry Go's
# deterministic ad-hoc code signature, which macOS accepts for
# formula-installed CLI tools (no quarantine attribute on brew downloads).
class Tobby < Formula
  desc "Carries OCI assets across network zones, down to air-gapped ones"
  homepage "https://tobby-fetch.github.io/tobby-fetch/"
  license "GPL-3.0-only"
  version "0.0.0" # bumped by the release workflow

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/tobby-fetch/tobby-fetch/releases/download/v#{version}/tobby-darwin-arm64"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000" # bumped by the release workflow
    else
      url "https://github.com/tobby-fetch/tobby-fetch/releases/download/v#{version}/tobby-darwin-amd64"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000" # bumped by the release workflow
    end
  end

  def install
    binary = Dir["tobby-darwin-*"].first || "tobby-darwin-#{Hardware::CPU.arm? ? "arm64" : "amd64"}"
    bin.install binary => "tobby"
  end

  def caveats
    <<~EOS
      macOS is a convenience tier: full test suite in CI, but the validated
      operating scope is Linux (server) and Windows (mirror workstation).
      See the platform matrix in the documentation.
    EOS
  end

  test do
    assert_match "tobby", shell_output("#{bin}/tobby version")
  end
end
