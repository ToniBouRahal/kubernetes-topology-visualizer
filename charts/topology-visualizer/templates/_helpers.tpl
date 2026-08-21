{{- define "topology.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "topology.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "topology.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else if hasPrefix .Release.Name $name -}}
{{- /* Release "topology" + chart "topology-visualizer" would otherwise render
       "topology-topology-visualizer-agent", which is what every workload is NAMED and therefore
       what the graph displays. The doubled prefix is pure noise in the UI. */ -}}
{{- $name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "topology.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "topology.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: topology-visualizer
{{- end -}}

{{- define "topology.agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "topology.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: agent
{{- end -}}

{{- define "topology.backend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "topology.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: backend
{{- end -}}

{{- define "topology.frontend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "topology.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: frontend
{{- end -}}

{{- define "topology.serviceAccountName" -}}
{{- printf "%s-agent" (include "topology.fullname" .) -}}
{{- end -}}

{{- define "topology.backendService" -}}
{{- printf "%s-backend" (include "topology.fullname" .) -}}
{{- end -}}

{{/*
The ingest URL the agent posts to. Derived from one place so agent and backend cannot disagree.
*/}}
{{- define "topology.backendIngestUrl" -}}
{{- if .Values.agent.backendIngestUrl -}}
{{- .Values.agent.backendIngestUrl -}}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:%d/api/v1/ingest/batches" (include "topology.backendService" .) .Release.Namespace (int .Values.backend.service.port) -}}
{{- end -}}
{{- end -}}

{{- define "topology.postgresql.selectorLabels" -}}
app.kubernetes.io/name: {{ include "topology.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: database
{{- end -}}

{{/*
The DSN the backend connects with. Derived in ONE place: an internal database points at the
StatefulSet's headless Service, an external one comes from a Secret the operator provides.
*/}}
{{- define "topology.databaseSecret" -}}
{{- if eq .Values.postgresql.mode "external" -}}
{{- required "externalDatabaseUrlSecret is required when postgresql.mode=external" .Values.externalDatabaseUrlSecret -}}
{{- else -}}
{{- .Values.postgresql.auth.existingSecret | default (printf "%s-db" (include "topology.fullname" .)) -}}
{{- end -}}
{{- end -}}
