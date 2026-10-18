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
{{ else if eq .component "edge" -}}
controlURL: {{ printf "http://%s-control:8080" $name | quote }}
internalTokenFile: "/etc/agents/secrets/internal.token"
{{ end -}}
{{- end -}}
