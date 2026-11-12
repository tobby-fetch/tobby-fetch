#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Package the Linux and Windows release binaries as one recipe-ready FileSet
# (RECIPE-SPEC §7.4, R-25): a multi-platform OCI image index whose four
# children are single-layer images holding exactly one file, the release
# binary — `tobby` for linux/amd64 and linux/arm64, `tobby.exe` for
# windows/amd64 and windows/arm64. A recipe pins the index digest and lists
# the `platforms` its zone runs; Tobby carries only those children across.
#
#   SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
#     sh tools/release-fileset.sh <version> <commit> <date-rfc3339> <dist> <repo>
#
# <dist> holds the binaries and SHA256SUMS of tools/release-build.sh;
# <repo> is a registry repository the script may write scratch tags into.
# The release workflow points it at a registry that lives only inside the
# job (`crane registry serve`) and then copies the finished index, by
# digest, to its public name: nothing reaches ghcr.io but the index and its
# four children. The digest of the index is the last line of stdout.
#
# Deterministic by construction — same binaries, same digest: the layer tar
# is written by Python's tarfile with a fixed mtime (SOURCE_DATE_EPOCH),
# uid/gid 0, no user or group names, mode 0755; crane's gzip writes no
# timestamp; the config starts from the empty OCI base and carries only the
# platform; the annotations derive from (version, commit, date), and
# crane orders the index children deterministically.
#
# Before anything is tagged, every child is read back from the registry and
# the file it holds is checked against SHA256SUMS — the same sums the SLSA
# provenance covers. A FileSet that held anything else would be refused here.
#
# Needs: crane, python3, sha256sum (or shasum).
set -eu

VERSION="$1"
COMMIT="$2"
DATE="$3"
DIST="$4"
REPO="$5"

: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH must be set to the commit timestamp}"

SOURCE="https://github.com/tobby-fetch/tobby-fetch"

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum | cut -d' ' -f1; }
else
  sha256() { shasum -a 256 | cut -d' ' -f1; }
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

set -- # the index members, accumulated as `-m <ref>` pairs
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  case "$os" in
    windows) ext=".exe" ;;
    *) ext="" ;;
  esac
  bin="${DIST}/tobby-${os}-${arch}${ext}"
  file="tobby${ext}"
  layer="${work}/${os}-${arch}.tar"

  want="$(awk -v f="tobby-${os}-${arch}${ext}" '$2 == f || $2 == "*" f {print $1}' "${DIST}/SHA256SUMS")"
  [ "${#want}" -eq 64 ] || { echo "no SHA256SUMS line for ${bin}" >&2; exit 1; }
  got="$(sha256 < "$bin")"
  [ "$got" = "$want" ] || { echo "${bin} does not match SHA256SUMS" >&2; exit 1; }

  python3 -I - "$bin" "$file" "$layer" "$SOURCE_DATE_EPOCH" <<'PY'
import sys, tarfile
src, name, out, mtime = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
with open(src, "rb") as f, tarfile.open(out, "w", format=tarfile.USTAR_FORMAT) as tar:
    info = tarfile.TarInfo(name)
    f.seek(0, 2)
    info.size = f.tell()
    f.seek(0)
    info.mode = 0o755
    info.mtime = mtime
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    tar.addfile(info, f)
PY

  # Scratch tag in the scratch repository; the platform is set by mutate,
  # which pushes the result by digest.
  crane append --oci-empty-base -f "$layer" -t "${REPO}:scratch-${os}-${arch}" >/dev/null
  ref="$(crane mutate "${REPO}:scratch-${os}-${arch}" \
    --set-platform "$target" \
    -a "org.opencontainers.image.source=${SOURCE}" \
    -a "org.opencontainers.image.version=${VERSION}" \
    -a "org.opencontainers.image.revision=${COMMIT}" \
    -a "org.opencontainers.image.created=${DATE}" \
    -a "org.opencontainers.image.title=${file}")"

  # Read the child back and prove it holds the release binary, and only it.
  crane export "$ref" "${work}/${os}-${arch}.export.tar"
  members="$(tar -tf "${work}/${os}-${arch}.export.tar")"
  [ "$members" = "$file" ] || { echo "${ref} holds [${members}], expected [${file}]" >&2; exit 1; }
  back="$(tar -xOf "${work}/${os}-${arch}.export.tar" "$file" | sha256)"
  [ "$back" = "$want" ] || { echo "${ref} does not hold the binary of SHA256SUMS" >&2; exit 1; }

  set -- "$@" -m "$ref"
done

# Annotations live on the children: crane cannot set them on an index, and
# a consumer inspecting a platform reads them from there anyway.
crane index append "$@" -t "${REPO}:scratch-index" >/dev/null
index="${REPO}:scratch-index"

# Four children, the four platforms, nothing else.
crane manifest "$index" > "${work}/index.json"
platforms="$(python3 -I - "${work}/index.json" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    m = json.load(f)
print(" ".join(sorted(d["platform"]["os"] + "/" + d["platform"]["architecture"]
                      for d in m["manifests"])))
PY
)"
[ "$platforms" = "linux/amd64 linux/arm64 windows/amd64 windows/arm64" ] \
  || { echo "index platforms are [${platforms}]" >&2; exit 1; }

crane digest "$index"
