{{- define "ivp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "ivp.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "ivp.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "ivp.labels" -}}
app.kubernetes.io/name: {{ include "ivp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "ivp.image" -}}
{{- $digest := required "Set image.digest to the built image sha256 digest" .Values.image.digest -}}
{{- if not (regexMatch "^sha256:[a-f0-9]{64}$" $digest) -}}
{{- fail "image.digest must be sha256 followed by 64 lowercase hex characters" -}}
{{- end -}}
{{- printf "%s@%s" .Values.image.repository $digest -}}
{{- end -}}

