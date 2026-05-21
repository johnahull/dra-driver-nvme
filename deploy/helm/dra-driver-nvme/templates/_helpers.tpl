{{/*
Expand the name of the chart.
*/}}
{{- define "dra-driver-nvme.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "dra-driver-nvme.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "dra-driver-nvme.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "dra-driver-nvme.labels" -}}
helm.sh/chart: {{ include "dra-driver-nvme.chart" . }}
{{ include "dra-driver-nvme.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels (immutable after first deploy)
*/}}
{{- define "dra-driver-nvme.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dra-driver-nvme.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "dra-driver-nvme.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "dra-driver-nvme.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Namespace to deploy into
*/}}
{{- define "dra-driver-nvme.namespace" -}}
{{- .Values.namespace.name | default "dra-nvme" }}
{{- end }}
