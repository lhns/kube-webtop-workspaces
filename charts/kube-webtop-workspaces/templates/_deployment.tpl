{{/* Deployment for one mode; call with (dict "ctx" . "component" "controller|gateway" "v" .Values.<component>). */}}
{{- define "kwt.deployment" -}}
{{- $ctx := .ctx }}
{{- $v := .v }}
{{- $component := .component }}
{{- $isController := eq $component "controller" }}
{{- $apiPort := ternary (include "kwt.port" $ctx.Values.powerApi.addr) (include "kwt.port" $ctx.Values.gateway.addr) $isController }}
{{- $metricsPort := include "kwt.port" $ctx.Values.metricsAddr }}
{{- $healthPort := include "kwt.port" $ctx.Values.healthAddr }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "kwt.fullname" $ctx }}-{{ $component }}
  labels:
    {{- include "kwt.labels" $ctx | nindent 4 }}
    app.kubernetes.io/component: {{ $component }}
spec:
  replicas: {{ $v.replicas }}
  selector:
    matchLabels:
      {{- include "kwt.selector" (dict "ctx" $ctx "component" $component) | nindent 6 }}
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  template:
    metadata:
      annotations:
        checksum/config: {{ include (print $ctx.Template.BasePath "/configmap.yaml") $ctx | sha256sum }}
        {{- if $ctx.Values.metrics.prometheusAnnotations }}
        prometheus.io/scrape: "true"
        prometheus.io/port: {{ $metricsPort | quote }}
        prometheus.io/path: /metrics
        {{- end }}
        {{- with $v.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      labels:
        {{- include "kwt.labels" $ctx | nindent 8 }}
        app.kubernetes.io/component: {{ $component }}
        {{- with $v.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "kwt.fullname" $ctx }}-{{ $component }}
      {{- with $ctx.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: {{ $component }}
          image: {{ include "kwt.image" $ctx }}
          imagePullPolicy: {{ $ctx.Values.image.pullPolicy }}
          args:
            - --mode={{ $component }}
            - --config=/etc/kube-webtop-workspaces/config.yaml
            - --log-level={{ $ctx.Values.logLevel }}
            - --log-format={{ $ctx.Values.logFormat }}
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            {{- with $v.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          ports:
            - name: {{ ternary "power-api" "http" $isController }}
              containerPort: {{ $apiPort }}
            - name: metrics
              containerPort: {{ $metricsPort }}
            - name: health
              containerPort: {{ $healthPort }}
          livenessProbe:
            httpGet: {path: /healthz, port: health}
          readinessProbe:
            httpGet: {path: /readyz, port: health}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          {{- with $v.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: config
              mountPath: /etc/kube-webtop-workspaces
              readOnly: true
            {{- if and $isController $ctx.Values.ldap.bindSecret.name }}
            - name: ldap-password
              mountPath: {{ include "kwt.ldapPasswordPath" $ctx | dir }}
              readOnly: true
            {{- end }}
            {{- if and $isController $ctx.Values.ldap.caSecret.name }}
            - name: ldap-ca
              mountPath: {{ include "kwt.ldapCAPath" $ctx | dir }}
              readOnly: true
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "kwt.fullname" $ctx }}
        {{- if and $isController $ctx.Values.ldap.bindSecret.name }}
        - name: ldap-password
          secret:
            secretName: {{ $ctx.Values.ldap.bindSecret.name }}
            items:
              - key: {{ $ctx.Values.ldap.bindSecret.key }}
                path: password
        {{- end }}
        {{- if and $isController $ctx.Values.ldap.caSecret.name }}
        - name: ldap-ca
          secret:
            secretName: {{ $ctx.Values.ldap.caSecret.name }}
            items:
              - key: {{ $ctx.Values.ldap.caSecret.key }}
                path: ca.crt
        {{- end }}
      affinity:
        {{- include "kwt.affinity" (dict "ctx" $ctx "component" $component "affinity" $v.affinity) | nindent 8 }}
      {{- with $v.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.priorityClassName }}
      priorityClassName: {{ . }}
      {{- end }}
{{- end }}
