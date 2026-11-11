#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Decide whether a release run is a release or a rehearsal, and print the
# names it may publish under, as `key=value` lines for $GITHUB_OUTPUT.
#
# This is the one place that turns a run's trigger into a publishing name.
# Only a `push` of a `refs/tags/v*` ref is a release; every other event —
# `workflow_dispatch` above all — is a dry run, whatever its inputs say.
#
# The version is validated BEFORE anything is printed: it must be one line
# of printable characters matching vX.Y.Z[-pre]. A control character
# (newline, carriage return, tab, ...) is refused outright, because a
# multi-line input would otherwise reach $GITHUB_OUTPUT as extra lines
# (`dry_run=false`, `image=...`, a `name<<EOF` heredoc) and override the
# decision below.
#
# Usage:
#   sh tools/release-meta.sh <event-name> <ref> <ref-name> <input-version> \
#     <owner> <repository> <commit> <source-date-epoch>
#
# The test suite is tools/release-meta-test.sh (run in CI).
set -eu

event="$1"
ref="$2"
ref_name="$3"
input_version="$4"
owner="$5"
repository="$6"
commit="$7"
sde="$8"

if [ "${event}" = "push" ] && case "${ref}" in refs/tags/v*) true ;; *) false ;; esac; then
  version="${ref_name}"
  dry_run=false
  ns="ghcr.io/${owner}"
else
  version="${input_version}"
  dry_run=true
  ns="ghcr.io/${owner}/dry-run"
fi

# Any byte that is not a printable character refuses the version — the
# regular expression below is applied per line by grep, so it cannot be the
# only check. `tr -d '[:print:]'` leaves exactly the offending bytes.
bad="$(printf '%s' "${version}" | LC_ALL=C tr -d '[:print:]' | wc -c | tr -d ' ')"
if [ "${bad}" != "0" ]; then
  echo "::error::the version contains ${bad} control or non-printable byte(s): refused" >&2
  exit 1
fi
if ! printf '%s\n' "${version}" | LC_ALL=C grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?'; then
  echo "::error::'${version}' is not a vX.Y.Z[-pre] version" >&2
  exit 1
fi
case "${owner}${repository}${commit}${sde}" in
  *[!A-Za-z0-9._/-]*)
    echo "::error::the run's owner, repository, commit or timestamp holds an unexpected character: refused" >&2
    exit 1
    ;;
esac

if [ "${dry_run}" = "true" ]; then
  echo "::notice::DRY RUN of ${version}: OCI artifacts go to ${ns}/, no release is created." >&2
fi
echo "version=${version}"
echo "commit=${commit}"
echo "source_date_epoch=${sde}"
echo "date=$(date -u -d "@${sde}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "${sde}" +%Y-%m-%dT%H:%M:%SZ)"
echo "dry_run=${dry_run}"
echo "image=${ns}/${repository#*/}"
echo "charts=${ns}/charts"
echo "binaries=${ns}/binaries/tobby"
