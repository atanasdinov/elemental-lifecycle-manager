{{/*
Expand the name of the chart.
*/}}
{{- define "elemental-lifecycle-manager.name" -}}
{{- default "elemental-lifecycle-manager" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "elemental-lifecycle-manager.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "elemental-lifecycle-manager.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "elemental-lifecycle-manager.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.webhookServiceName" -}}
{{- default (printf "%s-webhook-service" (include "elemental-lifecycle-manager.fullname" .)) .Values.webhook.service.name -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.metricsServiceName" -}}
{{- default (printf "%s-metrics-service" (include "elemental-lifecycle-manager.fullname" .)) .Values.metrics.service.name -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.managerRoleName" -}}
{{- printf "%s-manager-role" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.managerRoleBindingName" -}}
{{- printf "%s-manager-rolebinding" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.leaderElectionEnabled" -}}
{{- if gt (int .Values.replicaCount) 1 -}}
true
{{- end -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.leaderRoleName" -}}
{{- printf "%s-leader-role" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.leaderRoleBindingName" -}}
{{- printf "%s-leader-rolebinding" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.useDefaultCert" -}}
{{- if and .Values.webhook.enabled (not (hasKey .Values.webhook "cert")) -}}
true
{{- end -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.certificateName" -}}
{{- printf "%s-serving-cert" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.certificateSecretName" -}}
{{- printf "%s-webhook-server-cert" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.certificateIssuerName" -}}
{{- printf "%s-selfsigned-issuer" (include "elemental-lifecycle-manager.fullname" .) -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.webhookSecretName" -}}
{{- if include "elemental-lifecycle-manager.useDefaultCert" . -}}
{{- include "elemental-lifecycle-manager.certificateSecretName" . -}}
{{- else -}}
{{- required "webhook.cert.existingSecret is required when webhook.cert is defined" .Values.webhook.cert.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "elemental-lifecycle-manager.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ include "elemental-lifecycle-manager.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/part-of: elemental-lifecycle-manager
{{- end -}}

{{- define "elemental-lifecycle-manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "elemental-lifecycle-manager.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}
