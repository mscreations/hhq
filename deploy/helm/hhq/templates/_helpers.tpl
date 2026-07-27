{{/*
Base name for resources in this chart.
*/}}
{{- define "hhq.fullname" -}}
{{- .Release.Name -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "hhq.labels" -}}
app.kubernetes.io/name: hhq
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/*
Selector labels - used by both the Deployment's selector and pod template
labels, and must never change across releases of the same chart version.
*/}}
{{- define "hhq.selectorLabels" -}}
app.kubernetes.io/name: hhq
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Per-plugin CONFIG_DIR - kept distinct per plugin name so multiple plugins'
bootstrap files never collide with each other or with hhq's own /config.
*/}}
{{- define "hhq.pluginConfigDir" -}}
/config/plugins/{{ . }}
{{- end -}}
