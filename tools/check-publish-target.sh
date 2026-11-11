#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Refuse to publish under a real name during a release rehearsal.
#
# The release workflow computes every OCI name once, in its build job, and a
# `workflow_dispatch` run moves them all under ghcr.io/<owner>/dry-run/. This
# is the second lock on that door: each job that pushes to a registry runs it
# against the exact name it is about to push to, so a future edit that
# hard-codes a name, or reads it from the wrong place, fails before the push
# rather than overwriting a release.
#
# A real name is also refused unless the run is the push of a v* tag
# ($GITHUB_EVENT_NAME, $GITHUB_REF — set by the runner, not by any input):
# the dry-run flag is an output of another job, and this lock does not take
# its word for it.
#
# Usage: sh tools/check-publish-target.sh <dry-run: true|false> <oci-name>
set -eu

dry_run="$1"
target="$2"

case "${dry_run}" in
  true)
    case "${target}" in
      ghcr.io/*/dry-run/*) echo "dry run: publishing to ${target}" ;;
      *)
        echo "::error::dry run would publish to ${target}, outside ghcr.io/<owner>/dry-run/ — refused"
        exit 1
        ;;
    esac
    ;;
  false)
    if [ "${GITHUB_EVENT_NAME:-}" != "push" ] || case "${GITHUB_REF:-}" in refs/tags/v*) false ;; *) true ;; esac; then
      echo "::error::only the push of a v* tag publishes under a real name (event '${GITHUB_EVENT_NAME:-}', ref '${GITHUB_REF:-}') — refused"
      exit 1
    fi
    case "${target}" in
      */dry-run/*)
        echo "::error::a release would publish to the rehearsal name ${target} — refused"
        exit 1
        ;;
      *) echo "release: publishing to ${target}" ;;
    esac
    ;;
  *)
    echo "::error::dry-run flag is '${dry_run}', expected true or false"
    exit 1
    ;;
esac
