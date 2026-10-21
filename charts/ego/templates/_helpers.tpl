{{- define "ego.name" -}}
{{- default "ego" .Values.nameOverride -}}
{{- end }}

{{- define "ego.labels" -}}
app: {{ include "ego.name" . }}
app.kubernetes.io/name: {{ include "ego.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ (.Values.image.tag | default .Chart.AppVersion) | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{/* The --online-config document: online.config plus the findings block from online.findings. */}}
{{- define "ego.onlineConfig" -}}
{{- $cfg := deepCopy .Values.online.config -}}
{{- if .Values.online.findings -}}
{{- $_ := set $cfg "findings" .Values.online.findings -}}
{{- end -}}
{{- $cfg | toJson -}}
{{- end }}
