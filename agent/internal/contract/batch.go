// Package contract is the Go mirror of contracts/ids.md and contracts/openapi.json.
//
// These types are hand-written while the Python models are generated from, and the TypeScript
// client is generated from, the same OpenAPI document. That asymmetry is deliberate (ADR-003 §7):
// rather than add a Go code-generation toolchain, batch_test.go round-trips the shared fixture
// corpus so drift fails a test instead of failing a demo.
package contract

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the only version this release emits or accepts.
// The backend answers 400 — not 422 — for anything else, because a version mismatch is not
// something a retry can fix. See contracts/ids.md §8.
const SchemaVersion = 1

// ExternalNodeID is the single summarized destination for all non-cluster traffic.
// The remote IP is never part of identity and is never persisted (ADR-001 §6).
const ExternalNodeID = "external:EXTERNAL"

// AllowedKinds is exactly six values. ReplicaSet is deliberately absent: a pod owned by a
// ReplicaSet collapses to that ReplicaSet's own owner, the Deployment.
var AllowedKinds = map[string]bool{
	"Service":     true,
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Job":         true,
	"Pod":         true,
}

// ProtocolTCP is the only protocol in this release. UDP/SCTP/ICMP are out of scope (ADR-001 §4.2).
const ProtocolTCP = "TCP"

// ulidPattern is Crockford base32: 26 characters, excluding I, L, O and U.
var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// NodeRef identifies one endpoint of an observed relationship.
type NodeRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Namespace is a pointer so that null round-trips faithfully for the external node.
	// Using "" would make "absent" and "empty namespace" indistinguishable.
	Namespace *string `json:"namespace"`
	Name      string  `json:"name"`
}

// EdgeObservation is one aggregated client→server relationship for a flush interval.
type EdgeObservation struct {
	Source          NodeRef `json:"source"`
	Target          NodeRef `json:"target"`
	Protocol        string  `json:"protocol"`
	DestinationPort int     `json:"destination_port"`
	ConnectionCount int64   `json:"connection_count"`

	// Absent until the Phase 4 byte-accounting gate passes. omitempty on a pointer emits the
	// field only when measured — absent and zero are different (contracts/ids.md §10).
	BytesSent     *int64 `json:"bytes_sent,omitempty"`
	BytesReceived *int64 `json:"bytes_received,omitempty"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// IngestBatch is the retry-safe unit of delivery. BatchID is the idempotency key: the backend
// records it in the same transaction as the edge merge, so a retry cannot double-count.
type IngestBatch struct {
	SchemaVersion   int               `json:"schema_version"`
	ClusterID       string            `json:"cluster_id"`
	AgentID         string            `json:"agent_id"`
	BatchID         string            `json:"batch_id"`
	ObservedAt      time.Time         `json:"observed_at"`
	IntervalSeconds int               `json:"interval_seconds"`
	Edges           []EdgeObservation `json:"edges"`
}

// BuildNodeID constructs a canonical node ID. This is the one place the grammar is written in Go;
// contracts/ids.md §1 is the normative definition and backend/app/domain/models.py is its twin.
func BuildNodeID(clusterID, namespace, kind, name string) (string, error) {
	for _, seg := range []struct{ label, value string }{
		{"cluster_id", clusterID},
		{"namespace", namespace},
		{"kind", kind},
		{"name", name},
	} {
		if seg.value == "" {
			return "", fmt.Errorf("node id segment %q must not be empty", seg.label)
		}
		if strings.Contains(seg.value, ":") {
			return "", fmt.Errorf("node id segment %q must not contain ':'", seg.label)
		}
	}
	return fmt.Sprintf("k8s:%s:%s:%s:%s", clusterID, namespace, kind, name), nil
}

// EdgeKey is the aggregation key. It must stay identical to the edge_buckets primary key suffix
// and to the graph query grouping key — contracts/ids.md §4, asserted by T-3.4.
//
// Deliberately excluded: source port (ephemeral), PID (unreliable in softirq context) and node
// name (an edge is cluster-scoped, not node-scoped).
type EdgeKey struct {
	SourceID        string
	TargetID        string
	Protocol        string
	DestinationPort int
}

// Validate applies the contract rules the backend enforces, so the agent never spends a delivery
// attempt on a batch that is certain to be rejected. It mirrors backend/app/domain/models.py.
func (b *IngestBatch) Validate() error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version %d unsupported; this release emits %d",
			b.SchemaVersion, SchemaVersion)
	}
	if b.ClusterID == "" {
		return fmt.Errorf("cluster_id must not be empty")
	}
	if b.AgentID == "" {
		return fmt.Errorf("agent_id must not be empty")
	}
	if !ulidPattern.MatchString(b.BatchID) {
		return fmt.Errorf("batch_id %q is not a 26-character Crockford base32 ULID", b.BatchID)
	}
	if b.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at must be set")
	}
	if b.IntervalSeconds < 1 || b.IntervalSeconds > 3600 {
		return fmt.Errorf("interval_seconds %d out of range 1..3600", b.IntervalSeconds)
	}
	for i := range b.Edges {
		if err := b.Edges[i].validate(); err != nil {
			return fmt.Errorf("edges[%d]: %w", i, err)
		}
	}
	return nil
}

func (e *EdgeObservation) validate() error {
	if err := e.Source.validate("source"); err != nil {
		return err
	}
	if err := e.Target.validate("target"); err != nil {
		return err
	}
	if e.Protocol != ProtocolTCP {
		return fmt.Errorf("protocol %q unsupported; only %q in this release", e.Protocol, ProtocolTCP)
	}
	if e.DestinationPort < 1 || e.DestinationPort > 65535 {
		return fmt.Errorf("destination_port %d out of range 1..65535", e.DestinationPort)
	}
	if e.ConnectionCount < 1 {
		return fmt.Errorf("connection_count %d must be >= 1", e.ConnectionCount)
	}
	if e.BytesSent != nil && *e.BytesSent < 0 {
		return fmt.Errorf("bytes_sent %d must be >= 0", *e.BytesSent)
	}
	if e.BytesReceived != nil && *e.BytesReceived < 0 {
		return fmt.Errorf("bytes_received %d must be >= 0", *e.BytesReceived)
	}
	if e.FirstSeen.IsZero() || e.LastSeen.IsZero() {
		return fmt.Errorf("first_seen and last_seen must both be set")
	}
	if e.LastSeen.Before(e.FirstSeen) {
		return fmt.Errorf("last_seen must not precede first_seen")
	}
	return nil
}

func (n *NodeRef) validate(role string) error {
	if n.ID == "" {
		return fmt.Errorf("%s.id must not be empty", role)
	}
	if n.Name == "" {
		return fmt.Errorf("%s.name must not be empty", role)
	}
	if !AllowedKinds[n.Kind] {
		return fmt.Errorf("%s.kind %q is not one of the six allowed kinds", role, n.Kind)
	}
	return nil
}
