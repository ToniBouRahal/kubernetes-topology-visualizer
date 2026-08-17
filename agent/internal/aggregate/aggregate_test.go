package aggregate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/contract"
	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/resolver"
)

const clusterID = "kind-topology"

func at(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return parsed
}

func workload(ns, kind, name string) resolver.Endpoint {
	id, _ := contract.BuildNodeID(clusterID, ns, kind, name)
	return resolver.Endpoint{ID: id, Kind: kind, Namespace: ns, Name: name, Class: resolver.ClassWorkload}
}

func service(ns, name string) resolver.Endpoint {
	id, _ := contract.BuildNodeID(clusterID, ns, "Service", name)
	return resolver.Endpoint{ID: id, Kind: "Service", Namespace: ns, Name: name, Class: resolver.ClassService}
}

func newAgg(t *testing.T, infra ...uint16) *Aggregator {
	t.Helper()
	a := New(clusterID, "topology-agent/kind-worker", infra)
	a.SetClock(func() time.Time { return at(t, "2026-08-12T12:00:00Z") })
	return a
}

func obs(src, dst resolver.Endpoint, port uint16, ts time.Time) Observation {
	return Observation{Source: src, Target: dst, Protocol: "TCP", DestinationPort: port, Timestamp: ts}
}

// T-2.9: N identical observations become ONE edge with count N and the correct window.
func TestRepeatedObservationsCollapseToOneEdge(t *testing.T) {
	a := newAgg(t)
	src := workload("demo", "Deployment", "backend")
	dst := service("data", "redis")

	times := []string{
		"2026-08-12T11:59:55Z",
		"2026-08-12T11:59:51Z", // deliberately out of order: first_seen must be the earliest
		"2026-08-12T11:59:58Z",
		"2026-08-12T12:00:00Z",
	}
	for _, ts := range times {
		a.Add(obs(src, dst, 6379, at(t, ts)))
	}

	batch, ok := a.Flush(10)
	if !ok {
		t.Fatal("expected a batch")
	}
	if len(batch.Edges) != 1 {
		t.Fatalf("expected 1 aggregated edge, got %d", len(batch.Edges))
	}

	e := batch.Edges[0]
	if e.ConnectionCount != 4 {
		t.Errorf("ConnectionCount = %d, want 4", e.ConnectionCount)
	}
	if want := at(t, "2026-08-12T11:59:51Z"); !e.FirstSeen.Equal(want) {
		t.Errorf("FirstSeen = %v, want the earliest observation %v", e.FirstSeen, want)
	}
	if want := at(t, "2026-08-12T12:00:00Z"); !e.LastSeen.Equal(want) {
		t.Errorf("LastSeen = %v, want the latest observation %v", e.LastSeen, want)
	}
}

// Replicas of one Deployment share an identity, so their traffic is one edge, not three.
// This is the guarantee that makes the graph readable during pod churn.
func TestReplicasOfOneWorkloadProduceOneEdge(t *testing.T) {
	a := newAgg(t)
	// Three distinct pods, already collapsed by the resolver to the same workload identity.
	src := workload("demo", "Deployment", "frontend")
	dst := service("demo", "backend")

	for i := 0; i < 3; i++ {
		a.Add(obs(src, dst, 8080, at(t, "2026-08-12T11:59:5"+string(rune('0'+i))+"Z")))
	}

	batch, _ := a.Flush(10)
	if len(batch.Edges) != 1 {
		t.Fatalf("three replicas must produce one edge, got %d", len(batch.Edges))
	}
	if batch.Edges[0].ConnectionCount != 3 {
		t.Errorf("ConnectionCount = %d, want 3", batch.Edges[0].ConnectionCount)
	}
}

// Different ports are different edges: the port is part of the edge key.
func TestDistinctPortsAreDistinctEdges(t *testing.T) {
	a := newAgg(t)
	src := workload("demo", "Deployment", "backend")
	dst := workload("data", "StatefulSet", "postgres")

	a.Add(obs(src, dst, 5432, at(t, "2026-08-12T11:59:51Z")))
	a.Add(obs(src, dst, 5433, at(t, "2026-08-12T11:59:52Z")))

	batch, _ := a.Flush(10)
	if len(batch.Edges) != 2 {
		t.Fatalf("expected 2 edges for 2 ports, got %d", len(batch.Edges))
	}
}

// T-2.8: infrastructure ports are dropped and counted.
func TestInfrastructurePortsAreFilteredAndCounted(t *testing.T) {
	infra := []uint16{6443, 10250, 2379}
	a := newAgg(t, infra...)
	src := workload("demo", "Deployment", "backend")
	dst := workload("kube-system", "DaemonSet", "kube-proxy")

	for _, port := range infra {
		a.Add(obs(src, dst, port, at(t, "2026-08-12T11:59:51Z")))
	}
	a.Add(obs(src, service("demo", "api"), 8080, at(t, "2026-08-12T11:59:52Z")))

	if got := a.PendingEdges(); got != 1 {
		t.Errorf("expected only the application edge to survive, got %d edges", got)
	}
	if got := a.Counters().FilteredInfraPort; got != uint64(len(infra)) {
		t.Errorf("FilteredInfraPort = %d, want %d", got, len(infra))
	}
}

// Unresolved endpoints are counted separately from other exclusions. Folding them into
// "external" would report a CNI race as internet traffic.
func TestUnresolvedIsCountedAndExcluded(t *testing.T) {
	a := newAgg(t)
	src := workload("demo", "Deployment", "backend")
	unres := resolver.Endpoint{Class: resolver.ClassUnresolved, Name: "unresolved"}

	a.Add(obs(src, unres, 9999, at(t, "2026-08-12T11:59:51Z")))

	c := a.Counters()
	if c.UnresolvedEndpoints != 1 {
		t.Errorf("UnresolvedEndpoints = %d, want 1", c.UnresolvedEndpoints)
	}
	if c.FilteredNotGraphable != 0 {
		t.Error("unresolved must not also be counted as not-graphable; the two signals mean different things")
	}
	if a.PendingEdges() != 0 {
		t.Error("unresolved endpoints must not enter the graph")
	}
}

// Host traffic is classified but excluded from the default application graph.
func TestHostTrafficIsExcluded(t *testing.T) {
	a := newAgg(t)
	src := workload("demo", "Deployment", "backend")
	hostEP := resolver.Endpoint{Class: resolver.ClassHost, Name: "host"}

	a.Add(obs(src, hostEP, 22, at(t, "2026-08-12T11:59:51Z")))

	if a.PendingEdges() != 0 {
		t.Error("host traffic must not appear in the default graph")
	}
	if a.Counters().FilteredNotGraphable != 1 {
		t.Errorf("FilteredNotGraphable = %d, want 1", a.Counters().FilteredNotGraphable)
	}
}

// External destinations DO belong in the graph, as the single summarized node.
func TestExternalDestinationIsKept(t *testing.T) {
	a := newAgg(t)
	src := workload("demo", "Deployment", "backend")

	a.Add(obs(src, resolver.External(), 443, at(t, "2026-08-12T11:59:51Z")))

	batch, ok := a.Flush(10)
	if !ok || len(batch.Edges) != 1 {
		t.Fatal("external traffic must produce an edge")
	}
	if got := batch.Edges[0].Target.ID; got != contract.ExternalNodeID {
		t.Errorf("target ID = %q, want %q", got, contract.ExternalNodeID)
	}
	if batch.Edges[0].Target.Namespace != nil {
		t.Error("the external node must have a null namespace, not an empty string")
	}
}

// Flush must produce a batch the backend will accept. This is the agent-side half of the
// cross-language contract check.
func TestFlushProducesAValidBatch(t *testing.T) {
	a := newAgg(t)
	a.Add(obs(workload("demo", "Deployment", "frontend"), service("demo", "backend"), 8080, at(t, "2026-08-12T11:59:51Z")))
	a.Add(obs(workload("demo", "Deployment", "backend"), service("data", "redis"), 6379, at(t, "2026-08-12T11:59:52Z")))
	a.Add(obs(workload("demo", "Deployment", "backend"), resolver.External(), 443, at(t, "2026-08-12T11:59:53Z")))

	batch, ok := a.Flush(10)
	if !ok {
		t.Fatal("expected a batch")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("flushed batch fails the contract the backend enforces: %v", err)
	}
	if batch.SchemaVersion != contract.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", batch.SchemaVersion, contract.SchemaVersion)
	}
	if batch.IntervalSeconds != 10 {
		t.Errorf("IntervalSeconds = %d, want 10", batch.IntervalSeconds)
	}

	// The batch must round-trip as JSON — it is about to be POSTed.
	encoded, err := json.Marshal(&batch)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "bytes_sent") {
		t.Error("byte fields must be absent until the Phase 4 gate; absent is not zero")
	}
}

// Determinism: map iteration order is random in Go, so the sort must impose a stable order.
func TestFlushOrderingIsDeterministic(t *testing.T) {
	build := func() contract.IngestBatch {
		a := newAgg(t)
		// Insert in an order unrelated to the desired output order.
		a.Add(obs(workload("demo", "Deployment", "zulu"), service("demo", "alpha"), 9000, at(t, "2026-08-12T11:59:51Z")))
		a.Add(obs(workload("demo", "Deployment", "alpha"), service("demo", "zulu"), 8080, at(t, "2026-08-12T11:59:51Z")))
		a.Add(obs(workload("demo", "Deployment", "alpha"), service("demo", "zulu"), 8081, at(t, "2026-08-12T11:59:51Z")))
		a.Add(obs(workload("demo", "Deployment", "mike"), service("demo", "beta"), 7000, at(t, "2026-08-12T11:59:51Z")))
		batch, _ := a.Flush(10)
		return batch
	}

	first := build()
	for i := 0; i < 20; i++ {
		next := build()
		for j := range first.Edges {
			if first.Edges[j].Source.ID != next.Edges[j].Source.ID ||
				first.Edges[j].DestinationPort != next.Edges[j].DestinationPort {
				t.Fatalf("edge order is not deterministic at index %d across runs", j)
			}
		}
	}

	// Explicitly: sorted by source ID, then target ID, then protocol, then port.
	for i := 1; i < len(first.Edges); i++ {
		prev, cur := first.Edges[i-1], first.Edges[i]
		if prev.Source.ID > cur.Source.ID {
			t.Errorf("edges are not sorted by source ID: %q before %q", prev.Source.ID, cur.Source.ID)
		}
		if prev.Source.ID == cur.Source.ID && prev.Target.ID == cur.Target.ID &&
			prev.DestinationPort > cur.DestinationPort {
			t.Error("edges sharing source and target are not sorted by port")
		}
	}
}

// Flush must hand the caller an independent batch and reset the accumulator. The prototype
// cleared its map before delivery was confirmed and lost an interval on every failed POST.
func TestFlushResetsAccumulatorAndDetachesBatch(t *testing.T) {
	a := newAgg(t)
	src := workload("demo", "Deployment", "backend")
	a.Add(obs(src, service("data", "redis"), 6379, at(t, "2026-08-12T11:59:51Z")))

	batch, _ := a.Flush(10)
	originalCount := batch.Edges[0].ConnectionCount

	if a.PendingEdges() != 0 {
		t.Error("Flush must reset the accumulator")
	}

	// Further observations must not mutate the already-flushed batch.
	a.Add(obs(src, service("data", "redis"), 6379, at(t, "2026-08-12T12:00:05Z")))
	if batch.Edges[0].ConnectionCount != originalCount {
		t.Error("the flushed batch must be detached from the aggregator's state")
	}
	if a.PendingEdges() != 1 {
		t.Error("post-flush observations should start a fresh interval")
	}
}

func TestFlushOnEmptyAccumulatorReportsNothingToSend(t *testing.T) {
	a := newAgg(t)
	if _, ok := a.Flush(10); ok {
		t.Error("an empty interval must not produce a batch to POST")
	}
}

// Each flush gets a fresh batch ID: it is the backend's idempotency key, so a repeated ID
// would make a genuine second batch look like a retry and be silently discarded.
func TestEachFlushGetsAUniqueBatchID(t *testing.T) {
	a := newAgg(t)
	seen := make(map[string]bool)

	for i := 0; i < 25; i++ {
		a.Add(obs(workload("demo", "Deployment", "backend"), service("data", "redis"), 6379,
			at(t, "2026-08-12T11:59:51Z")))
		batch, ok := a.Flush(10)
		if !ok {
			t.Fatal("expected a batch")
		}
		if seen[batch.BatchID] {
			t.Fatalf("duplicate batch ID %q on iteration %d", batch.BatchID, i)
		}
		seen[batch.BatchID] = true

		if err := batch.Validate(); err != nil {
			t.Fatalf("generated batch ID is not contract-valid: %v", err)
		}
	}
}
