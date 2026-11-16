{{/*
SPDX-License-Identifier: GPL-3.0-only
Copyright © 2026 infraBuilder SASU and contributors

The alert rules of the supervision kit (R-11, FR-091 amendment), written
once and rendered twice: as a PrometheusRule and as a plain rules file.

Every expression names only families of the published metrics reference
(metrics-reference.json); internal/metrics holds this file to it. The
annotations use Prometheus' own templating, hence the raw-string actions.
*/}}
{{- define "tobby.alertGroups" -}}
{{- $t := .Values.alerting.thresholds -}}
{{- $extra := .Values.alerting.extraLabels -}}
- name: tobby.synchronization
  rules:
    - alert: TobbySyncFailing
      expr: increase(tobby_sync_runs_total{outcome="failure"}[{{ $t.syncFailuresWindow }}]) >= {{ $t.syncFailures }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: Tobby synchronizations keep failing
        description: {{ printf "%s or more synchronization runs failed within %s on {{ $labels.instance }}. Open the failed task for its taxonomy code." (toString $t.syncFailures) $t.syncFailuresWindow | quote }}
    {{- if ne (int $t.syncMaxAgeSeconds) 0 }}
    - alert: TobbySyncStale
      expr: time() - tobby_sync_last_success_timestamp_seconds > {{ $t.syncMaxAgeSeconds }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: No successful Tobby synchronization for too long
        description: {{ printf "The last successful synchronization on {{ $labels.instance }} ended more than %d seconds ago." (int $t.syncMaxAgeSeconds) | quote }}
    - alert: TobbySyncNeverSucceeded
      expr: tobby_sync_runs_total{outcome="failure"} > 0 unless on(instance) tobby_sync_last_success_timestamp_seconds
      for: {{ $t.syncFailuresWindow }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: Tobby has failed synchronizing and never succeeded since it started
        description: "{{`{{ $labels.instance }}`}} has not completed a single successful synchronization since it started."
    {{- end }}
    - alert: TobbySyncSaturated
      expr: tobby_sync_transfers_inflight >= tobby_sync_parallelism
      for: {{ $t.saturationFor }}
      labels:
        severity: info
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: Tobby transfers are pinned at sync.parallelism
        description: "{{`{{ $labels.instance }}`}} has run at its transfer bound for a sustained period: raise sync.parallelism if the link and the registries allow it."
- name: tobby.store
  rules:
    - alert: TobbyStoreOccupancyHigh
      expr: tobby_store_occupancy_exceeded == 1
      for: {{ $t.storeOccupancyFor }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: The Tobby store is beyond its occupancy threshold
        description: "The store of {{`{{ $labels.instance }}`}} holds more than storage.occupancyThreshold. Remove content, raise the threshold or grow the volume."
- name: tobby.vulnerability-scanning
  rules:
    - alert: TobbyVulnerabilityDatabaseStale
      expr: tobby_scan_db_stale == 1
      for: {{ $t.scannerFor }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: The Tobby vulnerability database is beyond scan.maxDbAge
        description: "{{`{{ $labels.instance }}`}} scans against a database older than it accepts."
    - alert: TobbyVulnerabilityDatabaseAbsent
      expr: tobby_scan_db_present == 0
      for: {{ $t.scannerFor }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: Tobby has no vulnerability database
        description: "{{`{{ $labels.instance }}`}} is in the absent-database degraded mode: content is reported as not scanned."
    - alert: TobbyScannerDegraded
      expr: tobby_scan_scanner_available == 0
      for: {{ $t.scannerFor }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: The Tobby vulnerability scanner is unavailable
        description: "{{`{{ $labels.instance }}`}} has no usable verified scanner binary: content is reported as not scanned."
- name: tobby.identity
  rules:
    - alert: TobbyServerCertificateExpiring
      expr: tobby_server_certificate_expiry_timestamp_seconds - time() < {{ $t.certificateExpirySeconds }}
      for: {{ $t.certificateExpiryFor }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: The Tobby listener certificate is about to expire
        description: {{ printf "The certificate presented by {{ $labels.instance }} expires within %d seconds (or already has): replace it from /admin/network." (int $t.certificateExpirySeconds) | quote }}
    - alert: TobbyClockImplausible
      expr: tobby_clock_implausible == 1
      for: {{ $t.clockFor }}
      labels:
        severity: warning
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        summary: The clock of a Tobby host is earlier than a date it proves has passed
        description: "{{`{{ $labels.instance }}`}} reads a time earlier than {{`{{ $labels.anchor }}`}} (TBY-CLK-001/002): expiries and freshness checks may be wrong."
{{- end -}}
