{{- define "ego.name" -}}
{{- default "ego" .Values.nameOverride -}}
{{- end }}

{{- define "ego.labels" -}}
app: {{ include "ego.name" . }}
app.kubernetes.io/name: {{ include "ego.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ (.Values.image.tag | default .Chart.AppVersion) | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- /* A chart pulled by digest is versioned "0.4.22+<digest>", and "+" cannot be in a label value. */}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{/* The --online-config document: online.config plus the findings block from online.findings. */}}
{{- define "ego.onlineConfig" -}}
{{- $cfg := deepCopy .Values.online.config -}}
{{- if .Values.online.findings -}}
{{- $_ := set $cfg "findings" .Values.online.findings -}}
{{- end -}}
{{- $cfg | toJson -}}
{{- end }}
