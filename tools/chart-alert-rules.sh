#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# The plain Prometheus rules file kept beside the chart (R-11) is the
# chart's own alert rules at their default thresholds, rendered — never
# written by hand, so the file for a Prometheus that is not run by the
# Operator and the PrometheusRule of the chart cannot drift apart.
#
#   sh tools/chart-alert-rules.sh           rewrite the file
#   sh tools/chart-alert-rules.sh --check   fail when the file is stale
#
# Needs helm. CI runs --check where helm is installed.
set -eu

chart="deploy/charts/tobby"
out="$chart/files/tobby-alerts.rules.yaml"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

{
    echo "# SPDX-License-Identifier: GPL-3.0-only"
    echo "# Copyright © 2026 infraBuilder SASU and contributors"
    echo "#"
    echo "# Tobby alert rules at their default thresholds (R-11, FR-091). GENERATED from"
    echo "# the chart by tools/chart-alert-rules.sh; edit templates/_alerts.tpl instead."
    echo "# Every expression names only families of the metrics reference"
    echo "# (metrics-reference.json, published with each release)."
    helm template tobby "$chart" --set alerting.rulesFile.enabled=true \
        -s templates/configmap-alert-rules.yaml |
        awk '/^  tobby-alerts.rules.yaml: \|$/ {on=1; next} on && /^    / {sub(/^    /, ""); print}'
} >"$tmp"

if [ "${1:-}" = "--check" ]; then
    if ! cmp -s "$tmp" "$out"; then
        echo "::error file=$out::stale: run 'sh tools/chart-alert-rules.sh' and commit the result"
        exit 1
    fi
    echo "$out is up to date"
    exit 0
fi
mv "$tmp" "$out"
trap - EXIT
echo "wrote $out"
