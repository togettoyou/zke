{{- define "zke.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "zke.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains (include "zke.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "zke.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "zke.server.fullname" -}}
{{- printf "%s-server" (include "zke.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "zke.server.configName" -}}
{{- printf "%s-config" (include "zke.server.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "zke.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/part-of: zke
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "zke.server.selectorLabels" -}}
app.kubernetes.io/name: zke-server
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "zke.postgresql.selectorLabels" -}}
app.kubernetes.io/name: zke-postgresql
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "zke.postgresql.fullname" -}}
{{- printf "%s-postgresql" (include "zke.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "zke.metrics.selectorLabels" -}}
app.kubernetes.io/name: zke-victoriametrics
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "zke.metrics.fullname" -}}
{{- printf "%s-victoriametrics" (include "zke.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- /*
Whether this release runs the metrics storage itself: whenever metrics are on
and no external endpoint is named. Naming one turns the bundled storage off.
*/ -}}
{{- define "zke.metrics.bundled" -}}
{{- if and .Values.server.metrics.enabled (empty .Values.server.metrics.storageWriteURL) -}}
true
{{- end -}}
{{- end -}}

{{- define "zke.metrics.serviceHost" -}}
{{- printf "%s.%s.svc.%s" (include "zke.metrics.fullname" .) .Release.Namespace (trimSuffix "." (default "cluster.local" .Values.clusterDomain)) -}}
{{- end -}}

{{- define "zke.metrics.writeURL" -}}
{{- if .Values.server.metrics.storageWriteURL -}}
{{- .Values.server.metrics.storageWriteURL -}}
{{- else -}}
{{- printf "http://%s:8428/api/v1/write" (include "zke.metrics.serviceHost" .) -}}
{{- end -}}
{{- end -}}

{{- define "zke.metrics.queryURL" -}}
{{- if .Values.server.metrics.storageQueryURL -}}
{{- .Values.server.metrics.storageQueryURL -}}
{{- else -}}
{{- printf "http://%s:8428/prometheus" (include "zke.metrics.serviceHost" .) -}}
{{- end -}}
{{- end -}}

{{- define "zke.server.imageTag" -}}
{{- default .Chart.AppVersion .Values.server.image.tag -}}
{{- end -}}
