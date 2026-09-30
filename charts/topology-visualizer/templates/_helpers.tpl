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
{{- printf "https://%s.%s.svc.cluster.local:%d/api/v1/ingest/batches" (include "topology.backendService" .) .Release.Namespace (int .Values.backend.service.ingestPort) -}}
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

{{/*
The internal PKI (ADR-014 D-14.2), computed ONCE per render and shared by every template.

Three CAs, one per role:
  server         signs the backend, PostgreSQL and bundled-Dex server certificates
  ingest-client  signs the agent's client certificate   — trusted only by the ingest listener
  api-client     signs the frontend's client certificate — trusted only by the API listener

The CA private keys are never stored. Every certificate is generated together and only the
leaves are kept; an upgrade reuses them (lookup) as long as all five Secrets exist, and if any is
missing the whole set is regenerated so it always hangs together. Anyone who can read Secrets in
the namespace can therefore use a certificate, but cannot mint one.

Memoised in .Values because genCA is random: two templates each generating their own set would
hand the backend a CA that never signed the agent's certificate. `helm template` has no cluster to
look up, so it always generates.
*/}}
{{- define "topology.pki" -}}
{{- if not (hasKey .Values "__pki") -}}
{{- $full := include "topology.fullname" . -}}
{{- $ns := .Release.Namespace -}}
{{- $pki := dict -}}
{{- $existing := dict -}}
{{- $complete := true -}}
{{- range $name := list "server" "postgresql" "agent" "frontend" "client-cas" "dex" -}}
{{- /* Dex's is a fixed name: its subchart mounts it from values, which cannot be templated. */ -}}
{{- $secretName := ternary "topology-dex-tls" (printf "%s-tls-%s" $full $name) (eq $name "dex") -}}
{{- $secret := lookup "v1" "Secret" $ns $secretName -}}
{{- if $secret -}}{{- $_ := set $existing $name $secret.data -}}{{- else -}}{{- $complete = false -}}{{- end -}}
{{- end -}}
{{- if $complete -}}
{{- range $name := list "server" "postgresql" "agent" "frontend" "dex" -}}
{{- $d := index $existing $name -}}
{{- $_ := set $pki $name (dict "crt" (index $d "tls.crt" | b64dec) "key" (index $d "tls.key" | b64dec) "ca" (index $d "ca.crt" | b64dec)) -}}
{{- end -}}
{{- $cas := index $existing "client-cas" -}}
{{- $_ := set $pki "clientCAs" (dict "ingest" (index $cas "ingest-ca.crt" | b64dec) "api" (index $cas "api-ca.crt" | b64dec)) -}}
{{- else -}}
{{- $caDays := int .Values.tls.caValidityDays -}}
{{- $days := int .Values.tls.certificateValidityDays -}}
{{- $serverCA := genCA (printf "%s server CA" $full) $caDays -}}
{{- $ingestCA := genCA (printf "%s ingest-client CA" $full) $caDays -}}
{{- $apiCA := genCA (printf "%s api-client CA" $full) $caDays -}}
{{- $backend := include "topology.backendService" . -}}
{{- $pg := printf "%s-postgresql" $full -}}
{{- $backendNames := list $backend (printf "%s.%s" $backend $ns) (printf "%s.%s.svc" $backend $ns) (printf "%s.%s.svc.cluster.local" $backend $ns) -}}
{{- $pgNames := list $pg (printf "%s.%s" $pg $ns) (printf "%s.%s.svc" $pg $ns) (printf "%s.%s.svc.cluster.local" $pg $ns) -}}
{{- $dexNames := list "topology-dex" (printf "topology-dex.%s" $ns) (printf "topology-dex.%s.svc" $ns) (printf "topology-dex.%s.svc.cluster.local" $ns) -}}
{{- $server := genSignedCert $backend nil $backendNames $days $serverCA -}}
{{- $postgres := genSignedCert $pg nil $pgNames $days $serverCA -}}
{{- $dex := genSignedCert "topology-dex" nil $dexNames $days $serverCA -}}
{{- $agent := genSignedCert "topology-agent" nil nil $days $ingestCA -}}
{{- $frontend := genSignedCert "topology-frontend" nil nil $days $apiCA -}}
{{- $_ := set $pki "server" (dict "crt" $server.Cert "key" $server.Key "ca" $serverCA.Cert) -}}
{{- $_ := set $pki "postgresql" (dict "crt" $postgres.Cert "key" $postgres.Key "ca" $serverCA.Cert) -}}
{{- $_ := set $pki "dex" (dict "crt" $dex.Cert "key" $dex.Key "ca" $serverCA.Cert) -}}
{{- /* A client's ca.crt is the CA it verifies the SERVER with, not the one that signed it. */ -}}
{{- $_ := set $pki "agent" (dict "crt" $agent.Cert "key" $agent.Key "ca" $serverCA.Cert) -}}
{{- $_ := set $pki "frontend" (dict "crt" $frontend.Cert "key" $frontend.Key "ca" $serverCA.Cert) -}}
{{- $_ := set $pki "clientCAs" (dict "ingest" $ingestCA.Cert "api" $apiCA.Cert) -}}
{{- end -}}
{{- $_ := set .Values "__pki" $pki -}}
{{- end -}}
{{- end -}}

{{/* A checksum that changes when a role's certificate does, so its pods roll onto the new one. */}}
{{- define "topology.pkiChecksum" -}}
{{- include "topology.pki" .root -}}
{{- $leaf := index .root.Values.__pki .role -}}
{{- printf "%s%s" $leaf.crt $leaf.ca | sha256sum -}}
{{- end -}}

{{/*
PostgreSQL host-based authentication (ADR-014 D-14.8). The first matching line wins, so plain TCP
reaches the `reject` lines only after TLS has failed to match.
*/}}
{{- define "topology.postgresql.hba" -}}
# The pod's own Unix socket: the image's initialisation and the pg_isready probes. Not reachable
# from the network.
local   all  all                 trust
# TCP: TLS and a SCRAM password, or nothing.
hostssl all  all  0.0.0.0/0      scram-sha-256
hostssl all  all  ::/0           scram-sha-256
host    all  all  0.0.0.0/0      reject
host    all  all  ::/0           reject
{{- end -}}
