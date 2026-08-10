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

{{- define "zke.server.imageTag" -}}
{{- default .Chart.AppVersion .Values.server.image.tag -}}
{{- end -}}

{{- define "zke.agent.imageTag" -}}
{{- default .Chart.AppVersion .Values.agent.image.tag -}}
{{- end -}}
