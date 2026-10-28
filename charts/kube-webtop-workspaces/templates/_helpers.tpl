{{- define "kwt.name" -}}{{ .Values.nameOverride | default "kube-webtop-workspaces" | trunc 40 | trimSuffix "-" }}{{- end }}

{{- define "kwt.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 40 | trimSuffix "-" -}}
{{- else if contains (include "kwt.name" .) .Release.Name -}}
{{- .Release.Name | trunc 40 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "kwt.name" .) | trunc 40 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{- define "kwt.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "kwt.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels; call with (dict "ctx" . "component" "gateway"). */}}
{{- define "kwt.selector" -}}
app.kubernetes.io/name: {{ include "kwt.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "kwt.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{- define "kwt.desktopServiceAccount" -}}
{{- .Values.desktop.serviceAccountName | default (printf "%s-desktop" (include "kwt.fullname" .)) -}}
{{- end }}

{{/* Port of a listen address like ":8080" or "0.0.0.0:8080". */}}
{{- define "kwt.port" -}}
{{- regexFind "[0-9]+$" . | int -}}
{{- end }}

{{- define "kwt.powerApiURL" -}}
{{- .Values.powerApi.url | default (printf "http://%s-controller.%s.svc:%d" (include "kwt.fullname" .) .Release.Namespace (include "kwt.port" .Values.powerApi.addr | int)) -}}
{{- end }}

{{- define "kwt.ldapPasswordPath" -}}/etc/kube-webtop-workspaces-secrets/ldap/password{{- end }}
{{- define "kwt.ldapCAPath" -}}/etc/kube-webtop-workspaces-secrets/ldap-ca/ca.crt{{- end }}

{{/* Soft anti-affinity across nodes unless affinity is set; call with (dict "ctx" . "component" "x" "affinity" ...). */}}
{{- define "kwt.affinity" -}}
{{- if .affinity }}
{{- toYaml .affinity }}
{{- else -}}
podAntiAffinity:
  preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      podAffinityTerm:
        topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
            {{- include "kwt.selector" . | nindent 12 }}
{{- end }}
{{- end }}
