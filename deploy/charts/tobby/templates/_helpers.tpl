{{/*
SPDX-License-Identifier: GPL-3.0-only
Copyright © 2026 infraBuilder SASU and contributors

Naming, labelling, and the one place that assembles Tobby's configuration
file out of the values.
*/}}

{{/* Chart name, overridable. */}}
{{- define "tobby.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified resource name. */}}
{{- define "tobby.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "tobby.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "tobby.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tobby.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "tobby.labels" -}}
helm.sh/chart: {{ include "tobby.chart" . }}
{{ include "tobby.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/part-of: tobby
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "tobby.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "tobby.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image reference. A digest, when given, is authoritative: it is what the
release provenance and the cosign signature are made against.
*/}}
{{- define "tobby.image" -}}
{{- $tag := default (printf "v%s" .Chart.AppVersion) .Values.image.tag -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding config.yaml. */}}
{{- define "tobby.configSecretName" -}}
{{- default (printf "%s-config" (include "tobby.fullname" .)) .Values.existingConfigSecret -}}
{{- end -}}

{{/* Name of the kubernetes.io/dockerconfigjson Secret, empty when unused. */}}
{{- define "tobby.registryCredentialsSecretName" -}}
{{- if .Values.registryCredentials.existingSecret -}}
{{- .Values.registryCredentials.existingSecret -}}
{{- else if and .Values.registryCredentials.enabled .Values.registryCredentials.dockerconfigjson -}}
{{- printf "%s-registry-credentials" (include "tobby.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* Whether registry credentials are wired at all. */}}
{{- define "tobby.registryCredentialsEnabled" -}}
{{- if include "tobby.registryCredentialsSecretName" . -}}true{{- end -}}
{{- end -}}

{{/* Directory the dockerconfigjson Secret is mounted at. */}}
{{- define "tobby.registryCredentialsMountPath" -}}/etc/tobby-registries{{- end -}}

{{/* Directory the configuration Secret is mounted at — Tobby's default. */}}
{{- define "tobby.configMountPath" -}}/etc/tobby{{- end -}}

{{/*
The rendered /etc/tobby/config.yaml.

Four settings are owned by the chart because the pod spec depends on them
too, and a configuration that disagrees with its own volume mounts is a
failure mode worth refusing outright rather than debugging at 3am.
*/}}
{{- define "tobby.config" -}}
{{- $c := deepCopy (default (dict) .Values.config) -}}
{{- if dig "storage" "root" "" $c -}}
{{- fail "config.storage.root is set by the chart: change persistence.storage.mountPath instead" -}}
{{- end -}}
{{- if dig "state" "root" "" $c -}}
{{- fail "config.state.root is set by the chart: change persistence.state.mountPath instead" -}}
{{- end -}}
{{- if dig "server" "addr" "" $c -}}
{{- fail "config.server.addr is set by the chart: change containerPort instead" -}}
{{- end -}}
{{- if eq (dig "mode" "" $c) "" -}}
{{- fail "config.mode is required: set \"passthrough\" or \"mirror\"" -}}
{{- end -}}
{{- if eq .Values.persistence.storage.mountPath .Values.persistence.state.mountPath -}}
{{- fail "persistence.storage.mountPath and persistence.state.mountPath must differ: the instance state never travels on the transportable store" -}}
{{- end -}}
{{- $base := dict
      "storage" (dict "root" .Values.persistence.storage.mountPath)
      "state" (dict "root" .Values.persistence.state.mountPath)
      "server" (dict "addr" (printf ":%v" .Values.containerPort)) -}}
{{- if include "tobby.registryCredentialsEnabled" . -}}
{{- $_ := set $base "registries" (dict "credentialsFile" (printf "%s/.dockerconfigjson" (include "tobby.registryCredentialsMountPath" .))) -}}
{{- end -}}
{{- toYaml (mergeOverwrite $base $c) -}}
{{- end -}}
