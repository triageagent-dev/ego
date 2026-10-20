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
