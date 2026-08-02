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

{{/*
Per-plugin container port name. Must be unique within the pod (a bare
"http" on every plugin container collides with the hhq container's own
"http" port, and with each other across multiple plugins - k8s will only
route/probe the first match by name in that case) and must be a valid
IANA_SVC_NAME: <=15 chars, alphanumeric/'-', start and end alphanumeric.
Truncate to 10 chars before appending "-http" (5 chars) to stay under the
15-char limit, and trim any trailing '-' left behind by the truncation.
*/}}
{{- define "hhq.pluginPortName" -}}
{{- printf "%s-http" (trunc 10 . | trimSuffix "-") -}}
{{- end -}}
