package api

import "time"

// Response structs for the fields the CLI reads, hand-written per ADR-003 D-3.1 and checked
// against contracts/openapi.json (ADR-015 D-15.3). Nullable contract fields are pointers so that
// absent ("not measured") stays distinguishable from zero (topology-contract §6).

// TimeWindow is the half-open period [Start, End) a response actually covers.
type TimeWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// GraphNode is one node. ID is opaque: callers read Kind, Namespace and Name, never parse ID.
type GraphNode struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Namespace *string   `json:"namespace"` // nil only for the external node
	Name      string    `json:"name"`
	Label     string    `json:"label"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Canary marks a decoy named in the chart's backend.canaries (ADR-016 D-16.4).
	Canary bool `json:"canary"`
}

// GraphEdge is one aggregated relationship in a window.
type GraphEdge struct {
	ID                    string    `json:"id"`
	SourceID              string    `json:"source_id"`
	TargetID              string    `json:"target_id"`
	Protocol              string    `json:"protocol"`
	DestinationPort       int       `json:"destination_port"`
	ConnectionCount       int64     `json:"connection_count"`
	FailedConnectionCount *int64    `json:"failed_connection_count"`
	FirstSeen             time.Time `json:"first_seen"`
	LastSeen              time.Time `json:"last_seen"`
}

type GraphSummary struct {
	NodeCount        int     `json:"node_count"`
	EdgeCount        int     `json:"edge_count"`
	TotalConnections int64   `json:"total_connections"`
	Truncated        bool    `json:"truncated"`
	TruncationReason *string `json:"truncation_reason"`
}

type GraphResponse struct {
	Window  TimeWindow   `json:"window"`
	Nodes   []GraphNode  `json:"nodes"`
	Edges   []GraphEdge  `json:"edges"`
	Summary GraphSummary `json:"summary"`
}

type NamespaceList struct {
	Window     TimeWindow `json:"window"`
	Namespaces []string   `json:"namespaces"`
}

// NodeDependency is one peer of a node. It carries the peer's label, not its kind.
type NodeDependency struct {
	NodeID                string `json:"node_id"`
	Label                 string `json:"label"`
	Protocol              string `json:"protocol"`
	DestinationPort       int    `json:"destination_port"`
	ConnectionCount       int64  `json:"connection_count"`
	FailedConnectionCount *int64 `json:"failed_connection_count"`
}

type NodeDetail struct {
	Node     GraphNode        `json:"node"`
	Window   TimeWindow       `json:"window"`
	Incoming []NodeDependency `json:"incoming"`
	Outgoing []NodeDependency `json:"outgoing"`
}

type EdgeDetail struct {
	Edge   GraphEdge  `json:"edge"`
	Window TimeWindow `json:"window"`
	Source GraphNode  `json:"source"`
	Target GraphNode  `json:"target"`
}

// DiffEdge classification values.
const (
	DiffNew       = "NEW"
	DiffRemoved   = "REMOVED"
	DiffChanged   = "CHANGED"
	DiffUnchanged = "UNCHANGED"
)

type DiffEdge struct {
	ID                      string `json:"id"`
	SourceID                string `json:"source_id"`
	TargetID                string `json:"target_id"`
	Protocol                string `json:"protocol"`
	DestinationPort         int    `json:"destination_port"`
	Classification          string `json:"classification"`
	BaselineConnectionCount int64  `json:"baseline_connection_count"`
	CurrentConnectionCount  int64  `json:"current_connection_count"`
}

// DiffSummary has a truncated flag but, unlike GraphSummary, no reason.
type DiffSummary struct {
	NewCount       int  `json:"new_count"`
	RemovedCount   int  `json:"removed_count"`
	ChangedCount   int  `json:"changed_count"`
	UnchangedCount int  `json:"unchanged_count"`
	Truncated      bool `json:"truncated"`
}

type DiffResponse struct {
	Baseline TimeWindow  `json:"baseline"`
	Current  TimeWindow  `json:"current"`
	Edges    []DiffEdge  `json:"edges"`
	Summary  DiffSummary `json:"summary"`
}

// Anomaly severities (ADR-016 D-16.1).
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// Anomaly is one rule that fired for one edge; an edge appears once per rule (ADR-016 D-16.1).
type Anomaly struct {
	ID                            string   `json:"id"`
	EdgeID                        string   `json:"edge_id"`
	Type                          string   `json:"type"`
	Severity                      string   `json:"severity"`
	SourceID                      string   `json:"source_id"`
	TargetID                      string   `json:"target_id"`
	Protocol                      string   `json:"protocol"`
	DestinationPort               int      `json:"destination_port"`
	CurrentConnectionCount        int64    `json:"current_connection_count"`
	CurrentFailedConnectionCount  *int64   `json:"current_failed_connection_count"`
	BaselineConnectionCount       int64    `json:"baseline_connection_count"`
	BaselineFailedConnectionCount *int64   `json:"baseline_failed_connection_count"`
	CurrentMeanSetupUS            *float64 `json:"current_mean_setup_us"`
	BaselineMeanSetupUS           *float64 `json:"baseline_mean_setup_us"`
	Reason                        string   `json:"reason"`
}

// AnomalySummary: BaselineObserved false means the novelty rules were not applied (D-16.2).
type AnomalySummary struct {
	CriticalCount    int  `json:"critical_count"`
	WarningCount     int  `json:"warning_count"`
	BaselineObserved bool `json:"baseline_observed"`
	Truncated        bool `json:"truncated"`
}

// AnomalyResponse carries the nodes its anomalies refer to, so names never come from an ID.
type AnomalyResponse struct {
	Window    TimeWindow     `json:"window"`
	Baseline  TimeWindow     `json:"baseline"`
	Anomalies []Anomaly      `json:"anomalies"`
	Nodes     []GraphNode    `json:"nodes"`
	Summary   AnomalySummary `json:"summary"`
}

// ErrorResponse is the backend's error body (4xx/5xx other than FastAPI's own 422s).
type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}
