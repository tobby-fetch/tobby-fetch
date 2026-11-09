#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# The reference chart must deploy the release it ships with (B-035).
#
# The chart's default image tag is "v" + appVersion. appVersion sat at
# 0.3.0 through v0.4.x, v0.5.0 and v0.5.1, because the only thing that was
# supposed to bump it was a sentence in a comment — so a default install
# of a v0.5.1 checkout deployed the v0.3.0 image. Every release now also
# publishes the chart to an OCI registry under its own version, which only
# makes the drift worse if nothing stops it. Three equalities, checked
# mechanically:
#
#   - Chart.yaml version == appVersion. The chart is published once per
#     release, and its content changes with appVersion, so it cannot keep
#     a version of its own: two releases would push two different charts
#     under one immutable-looking tag.
#   - appVersion == the newest released version in CHANGELOG.md. This is
#     the pull-request half: the release-cut commit moves the changelog,
#     and this check makes it move the chart in the same commit.
#   - appVersion == the tag being released, when one is given. This is
#     the release half, run by the release workflow before it publishes.
#
# Usage: sh tools/check-chart-version.sh [vX.Y.Z]
set -eu

chart="deploy/charts/tobby/Chart.yaml"
changelog="CHANGELOG.md"

field() {
    sed -n "s/^$1:[[:space:]]*\"\{0,1\}\([^\"[:space:]]*\)\"\{0,1\}[[:space:]]*$/\1/p" "$chart"
}

version="$(field version)"
app="$(field appVersion)"
released="$(sed -n 's/^## \[\([0-9][0-9.]*\)\].*/\1/p' "$changelog" | head -n 1)"

fail=0
if [ -z "$version" ] || [ -z "$app" ]; then
    echo "::error file=$chart::cannot read version/appVersion"
    exit 1
fi
if [ "$version" != "$app" ]; then
    echo "::error file=$chart::chart version $version differs from appVersion $app — they move together"
    fail=1
fi
if [ "$app" != "$released" ]; then
    echo "::error file=$chart::appVersion $app is not the newest released version in $changelog ($released)"
    fail=1
fi
if [ $# -ge 1 ] && [ "v$app" != "$1" ]; then
    echo "::error file=$chart::appVersion $app does not match the tag $1"
    fail=1
fi
# And the install documentation, which pins the chart version in copyable
# commands: a page that tells people to install a chart that was never
# published, or an old one, is the same defect in prose.
stale="$(grep -rnoE 'charts/tobby( --version |:)[0-9]+\.[0-9]+\.[0-9]+' \
    deploy/README.md website/src/content/docs 2>/dev/null |
    grep -vE "( --version |:)${app}\$" || true)"
if [ -n "$stale" ]; then
    printf '%s\n' "$stale" | while IFS= read -r hit; do
        echo "::error file=${hit%%:*}::install docs pin a chart version other than $app: ${hit#*:}"
    done
    fail=1
fi
[ "$fail" -eq 0 ] && echo "chart $version deploys tobby v$app — consistent, docs included"
exit "$fail"
