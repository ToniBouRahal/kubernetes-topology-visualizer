// Package aggregate turns a stream of resolved connection observations into batched edges.
//
// Individual TCP establishments are not graph edges. Repeated observations of the same
// (source, target, protocol, port) collapse into one edge with a connection count and a
// first/last seen window (source of truth §9, ADR-002 D-2.5).
package aggregate

import (
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/contract"
	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/resolver"
)

// Key is the aggregation key. It must stay identical to the edge_buckets primary key suffix and
// to the backend's graph grouping key — contracts/ids.md §4, asserted by T-3.4.
//
// Source port, PID, and node name are deliberately absent: the first is ephemeral, the second is
// unreliable in softirq context, and an edge is cluster-scoped rather than node-scoped.
type Key struct {
	SourceID        string
	TargetID        string
	Protocol        string
	DestinationPort uint16
}

// Observation is one resolved connection ready for aggregation.
type Observation struct {
	Source          resolver.Endpoint
	Target          resolver.Endpoint
	Protocol        string
	DestinationPort uint16
	Timestamp       time.Time
}

// Counters record why observations were discarded. They feed the agent's Prometheus metrics;
// without them a filter bug looks identical to an absence of traffic (ADR-001 §6).
type Counters struct {
	Observed               uint64
	FilteredInfraPort      uint64
	FilteredNotGraphable   uint64
	FilteredIncompleteID   uint64
	UnresolvedEndpoints    uint64
	AggregatedEdgesFlushed uint64
	BatchesFlushed         uint64
}

type entry struct {
	source    resolver.Endpoint
	target    resolver.Endpoint
	count     int64
	firstSeen time.Time
	lastSeen  time.Time
}

// Aggregator accumulates observations until Flush. Safe for concurrent use.
type Aggregator struct {
	mu    sync.Mutex
	edges map[Key]*entry

	clusterID  string
	agentID    string
	infraPorts map[uint16]bool

	counters Counters

	// now is injected so tests do not depend on wall-clock timing (ADR-004 D-4.2 applies to
	// the agent too — anything time-dependent must be testable deterministically).
	now func() time.Time

	entropy func() ulid.ULID
}

// New builds an Aggregator. infraPorts are dropped as Kubernetes control-plane noise rather
// than application topology.
func New(clusterID, agentID string, infraPorts []uint16) *Aggregator {
	ports := make(map[uint16]bool, len(infraPorts))
	for _, p := range infraPorts {
		ports[p] = true
	}

	seed := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // not cryptographic
	var seedMu sync.Mutex

	return &Aggregator{
		edges:      make(map[Key]*entry),
		clusterID:  clusterID,
		agentID:    agentID,
		infraPorts: ports,
		now:        time.Now,
		entropy: func() ulid.ULID {
			seedMu.Lock()
			defer seedMu.Unlock()
			return ulid.MustNew(ulid.Timestamp(time.Now()), seed)
		},
	}
}

// SetClock replaces the time source. Test-only.
func (a *Aggregator) SetClock(now func() time.Time) { a.now = now }

// Add records one observation, applying the filters that keep infrastructure and unresolvable
// traffic out of the application graph.
func (a *Aggregator) Add(obs Observation) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.counters.Observed++

	if a.infraPorts[obs.DestinationPort] {
		a.counters.FilteredInfraPort++
		return
	}

	// Unresolved is counted separately from other exclusions: it is the signal that resolution
	// is failing, and it must never be silently folded into "external" (contracts/ids.md §6).
	if obs.Source.Class == resolver.ClassUnresolved || obs.Target.Class == resolver.ClassUnresolved {
		a.counters.UnresolvedEndpoints++
		return
	}

	if !obs.Source.IsGraphable() || !obs.Target.IsGraphable() {
		a.counters.FilteredNotGraphable++
		return
	}

	// A blank ID means the canonical grammar rejected a segment (for example a name containing
	// ':'). Emitting it would produce an edge a consumer could mis-split.
	if obs.Source.ID == "" || obs.Target.ID == "" {
		a.counters.FilteredIncompleteID++
		return
	}

	key := Key{
		SourceID:        obs.Source.ID,
		TargetID:        obs.Target.ID,
		Protocol:        obs.Protocol,
		DestinationPort: obs.DestinationPort,
	}

	existing, ok := a.edges[key]
	if !ok {
		a.edges[key] = &entry{
			source:    obs.Source,
			target:    obs.Target,
			count:     1,
			firstSeen: obs.Timestamp,
			lastSeen:  obs.Timestamp,
		}
		return
	}

	existing.count++
	if obs.Timestamp.Before(existing.firstSeen) {
		existing.firstSeen = obs.Timestamp
	}
	if obs.Timestamp.After(existing.lastSeen) {
		existing.lastSeen = obs.Timestamp
	}
}

// Flush returns the accumulated edges as a batch and resets the accumulator.
//
// The returned batch is fully detached from internal state, so the caller owns it. This is what
// makes safe retry possible: the reference prototype cleared its map and only then attempted
// delivery, losing a whole interval on every failed POST (ADR-002 C7). Here the batch survives
// independently of this map, and the delivery layer decides when to discard it.
//
// Returns ok=false when there is nothing to send, so callers can skip an empty POST.
func (a *Aggregator) Flush(intervalSeconds int) (contract.IngestBatch, bool) {
	a.mu.Lock()
	edges := a.edges
	a.edges = make(map[Key]*entry, len(edges))
	a.counters.BatchesFlushed++
	a.counters.AggregatedEdgesFlushed += uint64(len(edges))
	now := a.now()
	entropy := a.entropy
	a.mu.Unlock()

	if len(edges) == 0 {
		return contract.IngestBatch{}, false
	}

	observations := make([]contract.EdgeObservation, 0, len(edges))
	for key, e := range edges {
		observations = append(observations, contract.EdgeObservation{
			Source:          nodeRef(e.source),
			Target:          nodeRef(e.target),
			Protocol:        key.Protocol,
			DestinationPort: int(key.DestinationPort),
			ConnectionCount: e.count,
			FirstSeen:       e.firstSeen.UTC(),
			LastSeen:        e.lastSeen.UTC(),
			// Byte fields stay absent until the Phase 4 feasibility gate. Absent is not zero
			// (contracts/ids.md §10).
		})
	}

	// Deterministic order by the edge key. Map iteration order is random in Go, and an
	// unordered batch would make golden-file tests and diffing meaningless.
	sort.Slice(observations, func(i, j int) bool {
		a, b := observations[i], observations[j]
		if a.Source.ID != b.Source.ID {
			return a.Source.ID < b.Source.ID
		}
		if a.Target.ID != b.Target.ID {
			return a.Target.ID < b.Target.ID
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		return a.DestinationPort < b.DestinationPort
	})

	return contract.IngestBatch{
		SchemaVersion:   contract.SchemaVersion,
		ClusterID:       a.clusterID,
		AgentID:         a.agentID,
		BatchID:         entropy().String(),
		ObservedAt:      now.UTC(),
		IntervalSeconds: intervalSeconds,
		Edges:           observations,
	}, true
}

// Counters returns a snapshot.
func (a *Aggregator) Counters() Counters {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counters
}

// PendingEdges reports how many distinct edges are currently accumulated.
func (a *Aggregator) PendingEdges() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.edges)
}

func nodeRef(e resolver.Endpoint) contract.NodeRef {
	ref := contract.NodeRef{
		ID:   e.ID,
		Kind: e.Kind,
		Name: e.Name,
	}
	// The external node has no namespace. A pointer keeps null distinct from the empty string
	// on the wire (contracts/ids.md §7).
	if e.Namespace != "" {
		ns := e.Namespace
		ref.Namespace = &ns
	}
	return ref
}
