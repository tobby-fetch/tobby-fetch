#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Lock the release run's publishing decision (tools/release-meta.sh,
# tools/check-publish-target.sh): a rehearsal can never turn into a
# release, whatever its `version` input holds.
#
# Run by CI (ci.yml, "Release guards"); locally: sh tools/release-meta-test.sh
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
meta="${here}/release-meta.sh"
target="${here}/check-publish-target.sh"
failures=0

nl='
'
cr="$(printf '\r')"
tab="$(printf '\t')"

fail() {
  echo "FAIL: $*"
  failures=$((failures + 1))
}

# refused <label> <version>: a dispatch with this input must fail and print
# nothing a later step could read as an output.
refused() {
  if out="$(sh "${meta}" workflow_dispatch refs/heads/main main "$2" o o/r abc 1700000000 2>/dev/null)"; then
    fail "$1: accepted, printed: ${out}"
  elif [ -n "${out}" ]; then
    fail "$1: refused but printed: ${out}"
  fi
}

refused "newline then dry_run=false" "v1.0.0${nl}dry_run=false"
refused "newline then image=" "v1.0.0${nl}image=ghcr.io/o/r"
refused "heredoc opener" "v1.0.0${nl}dry_run<<EOF${nl}false${nl}EOF"
refused "carriage return" "v1.0.0${cr}dry_run=false"
refused "trailing newline" "v1.0.0${nl}"
refused "tab" "v1.0.0${tab}"
refused "escape" "v1.0.0$(printf '\033')"
refused "not a version" "main"
refused "empty" ""

# A dispatch, valid input: dry run, rehearsal names only.
out="$(sh "${meta}" workflow_dispatch refs/heads/main main v1.2.3 o o/r abc 1700000000 2>/dev/null)"
printf '%s\n' "${out}" | grep -qx 'dry_run=true' || fail "dispatch: not a dry run: ${out}"
printf '%s\n' "${out}" | grep -qx 'image=ghcr.io/o/dry-run/r' || fail "dispatch: image not under dry-run/: ${out}"
[ "$(printf '%s\n' "${out}" | grep -c '^dry_run=')" = "1" ] || fail "dispatch: dry_run printed more than once"

# Any event but a tag push is a dry run, even one naming a tag.
for ev in "workflow_dispatch refs/tags/v1.2.3" "push refs/heads/v1.2.3" "schedule refs/tags/v1.2.3"; do
  # shellcheck disable=SC2086 # two words on purpose
  out="$(sh "${meta}" ${ev} v1.2.3 v1.2.3 o o/r abc 1700000000 2>/dev/null)"
  printf '%s\n' "${out}" | grep -qx 'dry_run=true' || fail "${ev}: not a dry run: ${out}"
done

# The tag push is the one release.
out="$(sh "${meta}" push refs/tags/v1.2.3 v1.2.3 ignored o o/r abc 1700000000 2>/dev/null)"
printf '%s\n' "${out}" | grep -qx 'dry_run=false' || fail "tag push: not a release: ${out}"
printf '%s\n' "${out}" | grep -qx 'image=ghcr.io/o/r' || fail "tag push: wrong image: ${out}"
printf '%s\n' "${out}" | grep -qx 'version=v1.2.3' || fail "tag push: wrong version: ${out}"

# A tag pushed with a forged name is refused like an input.
if sh "${meta}" push "refs/tags/v1.2.3" "v1.2.3${nl}dry_run=true" x o o/r abc 1700000000 >/dev/null 2>&1; then
  fail "tag push with a multi-line name accepted"
fi

# The publishing lock refuses a real name outside a tag push.
if GITHUB_EVENT_NAME=workflow_dispatch GITHUB_REF=refs/heads/main sh "${target}" false ghcr.io/o/r >/dev/null 2>&1; then
  fail "check-publish-target: real name accepted under a dispatch"
fi
if GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main sh "${target}" false ghcr.io/o/r >/dev/null 2>&1; then
  fail "check-publish-target: real name accepted on a branch push"
fi
GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.2.3 sh "${target}" false ghcr.io/o/r >/dev/null 2>&1 \
  || fail "check-publish-target: real name refused on a tag push"
GITHUB_EVENT_NAME=workflow_dispatch GITHUB_REF=refs/heads/main sh "${target}" true ghcr.io/o/dry-run/r >/dev/null 2>&1 \
  || fail "check-publish-target: rehearsal name refused under a dispatch"
if sh "${target}" "true${nl}" ghcr.io/o/dry-run/r >/dev/null 2>&1; then
  fail "check-publish-target: a multi-line flag accepted"
fi

if [ "${failures}" -ne 0 ]; then
  echo "${failures} release-guard check(s) failed"
  exit 1
fi
echo "release guards: all checks pass"
