{{- define "agents.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 48 | trimSuffix "-" -}}
{{- end -}}

{{- define "agents.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 48 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "agents.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 48 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 48 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "agents.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agents.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name | quote }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "agents.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" | quote }}
{{ include "agents.selectorLabels" . }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service | quote }}
{{- end -}}

{{- define "agents.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "agents.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "agents.configuration" -}}
{{- $root := .root -}}
{{- $name := include "agents.fullname" $root -}}
listenAddress: "0.0.0.0:8080"
serverURL: {{ trimSuffix "/" $root.Values.public.serverURL | quote }}
edgeURL: {{ $root.Values.public.edgeURL | quote }}
tlsOffload: true
{{ if eq .component "control" -}}
stateDir: "/var/lib/agents/state"
adminTokenFile: "/etc/agents/secrets/admin.token"
internalTokenFile: "/etc/agents/secrets/internal.token"
edgeInternalURL: {{ printf "http://%s-edge:8080" $name | quote }}
consoleURL: {{ printf "http://%s-console:8080" $name | quote }}
database:
  type: {{ $root.Values.database.type }}
{{ if eq $root.Values.database.type "sqlite" }}  dsn: "/var/lib/agents/state/agents.db"
{{ else }}  dsnFile: "/etc/agents/secrets/database-dsn"
{{ end -}}
{{ if $root.Values.oidc.enabled }}oidc:
  enabled: true
  issuer: {{ $root.Values.oidc.issuer | quote }}
  clientId: {{ $root.Values.oidc.clientId | quote }}
  redirectURL: {{ printf "%s/auth/oidc/callback" (trimSuffix "/" $root.Values.public.serverURL) | quote }}
{{ if $root.Values.oidc.clientSecretKey }}  clientSecretFile: "/etc/agents/secrets/client-secret"
{{ end }}  scopes: {{ $root.Values.oidc.scopes | toJson }}
  bootstrapAdminSubjects: {{ $root.Values.oidc.bootstrapAdminSubjects | toJson }}
  adminEmails: {{ $root.Values.oidc.adminEmails | toJson }}
{{ end -}}
{{ if $root.Values.hydra.enabled }}hydra:
  enabled: true
  publicURL: {{ trimSuffix "/" $root.Values.hydra.publicURL | quote }}
  adminURL: {{ printf "http://%s-hydra-admin:4445" $name | quote }}
  clientId: {{ $root.Values.hydra.clientId | quote }}
{{ with $root.Values.hydra.audience }}  audience: {{ . | quote }}
{{ end }}{{ end -}}
{{ else if eq .component "edge" -}}
controlURL: {{ printf "http://%s-control:8080" $name | quote }}
internalTokenFile: "/etc/agents/secrets/internal.token"
{{ end -}}
{{- end -}}

{{- define "agents.hydraImage" -}}
{{- $image := .Values.hydra.image -}}
{{- if $image.digest -}}
{{- printf "%s:%s@%s" $image.repository $image.tag $image.digest -}}
{{- else -}}
{{- printf "%s:%s" $image.repository $image.tag -}}
{{- end -}}
{{- end -}}

{{/* Hydra's configuration carries no secrets; DSN and system secrets are environment variables from the Secret. */}}
{{- define "agents.hydraConfiguration" -}}
{{- $server := trimSuffix "/" .Values.public.serverURL -}}
serve:
  public: {port: 4444}
  admin: {port: 4445}
urls:
  self:
    issuer: {{ trimSuffix "/" .Values.hydra.publicURL | quote }}
  login: {{ printf "%s/oauth/login" $server | quote }}
  consent: {{ printf "%s/oauth/consent" $server | quote }}
  error: {{ printf "%s/oauth/error" $server | quote }}
  device:
    verification: {{ printf "%s/oauth/device" $server | quote }}
    success: {{ printf "%s/oauth/device/done" $server | quote }}
strategies:
  access_token: opaque
ttl:
  device_user_code: 10m
log:
  level: info
  format: json
{{- end -}}

{{/* Hydra's DSN and system secrets, for the server and its migration Job. */}}
{{- define "agents.hydraEnvironment" -}}
- name: DSN
{{- if eq .Values.hydra.database.type "memory" }}
  value: memory
{{- else }}
  valueFrom:
    secretKeyRef:
      name: {{ default .Values.existingSecret .Values.hydra.existingSecret | quote }}
      key: {{ .Values.hydra.secretKeys.dsn | quote }}
{{- end }}
- name: SECRETS_SYSTEM
  valueFrom:
    secretKeyRef:
      name: {{ default .Values.existingSecret .Values.hydra.existingSecret | quote }}
      key: {{ .Values.hydra.secretKeys.systemSecrets | quote }}
{{- end -}}
