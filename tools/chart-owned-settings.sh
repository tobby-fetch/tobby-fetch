#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# The settings the chart owns are refused, not overridden (B-072): a
# configuration that disagrees with the volumes the pod mounts must fail at
# `helm template` time. Each case below renders the chart with one value set
# and checks that it either renders or fails with the message that names
# what to use instead.
#
#   sh tools/chart-owned-settings.sh
#
# Needs helm. CI runs it where helm is installed. Stdout only: nothing is
# installed anywhere.
set -eu

chart="deploy/charts/tobby"
fail=0

# refused <expected fragment> <helm args...>: must fail, message names the fragment.
refused() {
    want="$1"
    shift
    if out="$(helm template tobby "$chart" "$@" 2>&1)"; then
        echo "FAIL: rendered, expected a refusal naming '$want': $*"
        fail=1
    elif ! printf '%s' "$out" | grep -qF -- "$want"; then
        echo "FAIL: refused without '$want': $*"
        printf '%s\n' "$out"
        fail=1
    else
        echo "ok: refused ($want): $*"
    fi
}

# renders <helm args...>: must render.
renders() {
    if out="$(helm template tobby "$chart" "$@" 2>&1)"; then
        echo "ok: renders: $*"
    else
        echo "FAIL: refused, expected to render: $*"
        printf '%s\n' "$out"
        fail=1
    fi
}

renders
refused "config.storage.root is set by the chart" --set config.storage.root=/x
refused "config.state.root is set by the chart" --set config.state.root=/x
refused "config.server.addr is set by the chart" --set config.server.addr=:1

# registries.credentialsFile is owned while the chart mounts the Secret...
renders --set registryCredentials.existingSecret=creds
refused "registryCredentials.existingSecret" \
    --set registryCredentials.existingSecret=creds --set config.registries.credentialsFile=/x/y
refused "registryCredentials.dockerconfigjson" \
    --set registryCredentials.enabled=true \
    --set-json 'registryCredentials.dockerconfigjson="{\"auths\":{}}"' \
    --set config.registries.credentialsFile=/x/y
# ... and the operator's own while it does not (a file from extraVolumes).
renders --set config.registries.credentialsFile=/x/y
renders --set registryCredentials.enabled=true --set config.registries.credentialsFile=/x/y

exit "$fail"
