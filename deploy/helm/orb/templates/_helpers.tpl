{{/*
Copyright The ORB Authors
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* The release's full name, used for most objects. */}}
{{- define "orb.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "orb.labels" -}}
app.kubernetes.io/name: orb
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* Labels that select the controller pod. */}}
{{- define "orb.selectorLabels" -}}
app.kubernetes.io/name: orb
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end -}}

{{/* The controller image. */}}
{{- define "orb.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* The agent image: the controller image unless overridden. */}}
{{- define "orb.agentImage" -}}
{{- default (include "orb.image" .) .Values.agentImage -}}
{{- end -}}

{{/* The service account name. */}}
{{- define "orb.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "orb.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
