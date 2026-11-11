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

{{/*
The desktop defaults (desktop.defaults) as JSON {data, items}: the ConfigMap data, and the volume items that
place its keys in webtop-nix's defaults directory (session.conf, xdg/<file>, home.nix). Empty data: none set.
*/}}
{{- define "kwt.desktopDefaults" -}}
{{- $d := .Values.desktop.defaults | default dict -}}
{{- if or .Values.desktop.timeZone .Values.desktop.locale -}}
{{- fail "desktop.timeZone and desktop.locale are replaced by desktop.defaults.timeZone and desktop.defaults.locale" -}}
{{- end -}}
{{- $data := dict -}}
{{- $items := list -}}
{{- $conf := list -}}
{{- with $d.timeZone }}{{ $conf = append $conf (printf "timeZone=%s" .) }}{{ end -}}
{{- with $d.locale }}{{ $conf = append $conf (printf "locale=%s" .) }}{{ end -}}
{{- with $d.language }}{{ $conf = append $conf (printf "language=%s" .) }}{{ end -}}
{{- range $k, $v := ($d.formats | default dict) }}{{ $conf = append $conf (printf "%s=%s" $k $v) }}{{ end -}}
{{- if $conf -}}
{{- $_ := set $data "session.conf" (printf "%s\n" (join "\n" $conf)) -}}
{{- $items = append $items (dict "key" "session.conf" "path" "session.conf") -}}
{{- end -}}
{{- range $name, $text := ($d.xdgConfig | default dict) -}}
{{- if eq $name "plasma-localerc" -}}{{- fail "desktop.defaults.xdgConfig.plasma-localerc is not allowed: webtop-locale owns that file; use timeZone, locale, language and formats" -}}{{- end -}}
{{- if not (regexMatch "^[A-Za-z0-9][A-Za-z0-9._-]*$" $name) -}}{{- fail (printf "desktop.defaults.xdgConfig.%s: not a plain file name" $name) -}}{{- end -}}
{{- $_ := set $data (printf "xdg-%s" $name) $text -}}
{{- $items = append $items (dict "key" (printf "xdg-%s" $name) "path" (printf "xdg/%s" $name)) -}}
{{- end -}}
{{- with $d.homeModule -}}
{{- $_ := set $data "home.nix" . -}}
{{- $items = append $items (dict "key" "home.nix" "path" "home.nix") -}}
{{- end -}}
{{- dict "data" $data "items" $items | toJson -}}
{{- end }}
