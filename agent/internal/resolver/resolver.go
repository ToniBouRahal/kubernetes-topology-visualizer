// Package resolver turns raw IP:port endpoints into stable Kubernetes identities.
//
// The logic here is pure: it reads through the Caches interface and returns Endpoints. The
// client-go informer wiring lives in informers.go and satisfies that interface. This split is
// deliberate — endpoint resolution carries most of the correctness risk in the agent and must be
// testable without a kernel or a cluster (ADR-002 §4).
//
// Normative rules: contracts/ids.md §6 and ADR-002 D-2.4.
package resolver

import (
	"net/netip"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/contract"
)

// Class describes what an endpoint resolved to. Unresolved and External are deliberately
// distinct: conflating them would report a CNI timing race as internet traffic.
type Class string

const (
	ClassWorkload   Class = "workload"
	ClassService    Class = "service"
	ClassExternal   Class = "external"
	ClassHost       Class = "host"
	ClassUnresolved Class = "unresolved"
)

// Endpoint is one resolved end of an observed connection.
type Endpoint struct {
	ID        string
	Kind      string
	Namespace string
	Name      string
	Class     Class

	// CandidateServices is populated only when several Services select the same pod and port.
	// The destination stays the workload and the ambiguity is preserved as metadata — never
	// resolved by picking one arbitrarily (contracts/ids.md §6, rule 3).
	CandidateServices []string
}

// IsGraphable reports whether this endpoint belongs in the default application graph.
// Host traffic is classified but excluded by default (ADR-002 D-2.4, rule 5).
func (e Endpoint) IsGraphable() bool {
	return e.Class == ClassWorkload || e.Class == ClassService || e.Class == ClassExternal
}

// ServiceRef is a Service that selects a given endpoint address on a given port.
type ServiceRef struct {
	Namespace string
	Name      string
}

// OwnerRef is one step in an ownership chain.
type OwnerRef struct {
	Kind string
	Name string
}

// Caches is the read side of the informer state. Implemented by the live informer set and by
// test fakes.
type Caches interface {
	// PodByIP finds a pod by its status.podIP, cluster-wide.
	PodByIP(ip netip.Addr) (namespace, name string, owner OwnerRef, ok bool)

	// ServiceByClusterIP finds a Service by its ClusterIP.
	ServiceByClusterIP(ip netip.Addr) (namespace, name string, ok bool)

	// ServicesForEndpoint returns every Service whose EndpointSlices contain ip and whose
	// declared target port matches port.
	ServicesForEndpoint(ip netip.Addr, port uint16) []ServiceRef

	// ResolveOwner walks one link of an ownership chain, e.g. ReplicaSet → Deployment.
	ResolveOwner(namespace string, owner OwnerRef) (OwnerRef, bool)

	// IsNodeIP reports whether ip belongs to a cluster Node.
	IsNodeIP(ip netip.Addr) bool
}

// Resolver converts endpoints to identities. Safe for concurrent use if Caches is.
type Resolver struct {
	clusterID string
	caches    Caches
}

func New(clusterID string, caches Caches) *Resolver {
	return &Resolver{clusterID: clusterID, caches: caches}
}

// External is the single summarized destination for all non-cluster traffic. The remote IP is
// deliberately absent — it is never part of identity and never persisted (ADR-001 §6).
func External() Endpoint {
	return Endpoint{
		ID:    contract.ExternalNodeID,
		Kind:  "Pod", // the wire contract has no External kind; the ID carries the meaning
		Name:  "EXTERNAL",
		Class: ClassExternal,
	}
}

func unresolved() Endpoint {
	return Endpoint{Class: ClassUnresolved, Name: "unresolved"}
}

func host() Endpoint {
	return Endpoint{Class: ClassHost, Name: "host"}
}

// ResolveSource identifies the initiating end of a connection.
//
// A source never resolves to a Service: Services are destinations, and a Service does not
// originate a connection.
//
// A source is also never External. Only active opens reach this code (the BPF program filters
// on SYN_SENT → ESTABLISHED), so the initiator is by construction a process on this node.
// Traffic arriving from outside the cluster is an accepted socket and was filtered in the kernel.
func (r *Resolver) ResolveSource(ip netip.Addr) Endpoint {
	if ns, name, owner, ok := r.caches.PodByIP(ip); ok {
		return r.workloadFor(ns, name, owner)
	}
	if r.caches.IsNodeIP(ip) {
		return host()
	}
	return unresolved()
}

// ResolveDestination identifies the receiving end, applying the ladder in contracts/ids.md §6.
// First match wins.
func (r *Resolver) ResolveDestination(ip netip.Addr, port uint16) Endpoint {
	// 1. A ClusterIP resolves directly to its Service.
	if ns, name, ok := r.caches.ServiceByClusterIP(ip); ok {
		return r.service(ns, name)
	}

	podNS, podName, owner, isPod := r.caches.PodByIP(ip)

	if isPod {
		// 2/3. A pod IP backing one or more Services, matched on the observed port.
		matches := r.caches.ServicesForEndpoint(ip, port)
		switch len(matches) {
		case 1:
			return r.service(matches[0].Namespace, matches[0].Name)
		case 0:
			// 4. A pod reached directly, with no Service in front of it.
			return r.workloadFor(podNS, podName, owner)
		default:
			// 3. Ambiguous: several Services select this pod and port. Keep the workload
			// identity and attach the candidates as metadata. Choosing one arbitrarily
			// would fabricate certainty the data does not support.
			endpoint := r.workloadFor(podNS, podName, owner)
			endpoint.CandidateServices = make([]string, 0, len(matches))
			for _, m := range matches {
				endpoint.CandidateServices = append(endpoint.CandidateServices, m.Namespace+"/"+m.Name)
			}
			return endpoint
		}
	}

	// 5. Node or host traffic: classified, but excluded from the default graph.
	if r.caches.IsNodeIP(ip) {
		return host()
	}

	// 6/7. Outside the cluster. A globally routable address is genuinely external; a private
	// or otherwise non-routable one is far more likely a cluster address the informer cache
	// has not caught up with, and reporting that as internet traffic would be a lie.
	if isRoutable(ip) {
		return External()
	}
	return unresolved()
}

// workloadFor collapses a pod to its stable top-level owner.
//
// Pod → ReplicaSet → Deployment collapses two levels, which is why replicas of a Deployment all
// share one identity and pod churn does not fragment the graph. StatefulSet, DaemonSet, and Job
// own their pods directly. A Job owned by a CronJob stops at the Job: CronJob is not one of the
// six allowed kinds (contracts/ids.md §1).
func (r *Resolver) workloadFor(namespace, podName string, owner OwnerRef) Endpoint {
	kind, name := "Pod", podName

	switch owner.Kind {
	case "ReplicaSet":
		// One more hop to the Deployment. If the ReplicaSet is somehow ownerless — a bare
		// ReplicaSet is legal — the pod keeps Pod identity rather than gaining a kind that
		// is not in the allowed set.
		if next, ok := r.caches.ResolveOwner(namespace, owner); ok && next.Kind == "Deployment" {
			kind, name = "Deployment", next.Name
		}
	case "StatefulSet", "DaemonSet", "Job":
		kind, name = owner.Kind, owner.Name
	}

	return Endpoint{
		ID:        r.mustBuildID(namespace, kind, name),
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Class:     ClassWorkload,
	}
}

func (r *Resolver) service(namespace, name string) Endpoint {
	return Endpoint{
		ID:        r.mustBuildID(namespace, "Service", name),
		Kind:      "Service",
		Namespace: namespace,
		Name:      name,
		Class:     ClassService,
	}
}

// mustBuildID applies the canonical grammar. A name containing ':' would make the ID ambiguous;
// rather than emit something a consumer could mis-split, treat it as unresolvable.
func (r *Resolver) mustBuildID(namespace, kind, name string) string {
	id, err := contract.BuildNodeID(r.clusterID, namespace, kind, name)
	if err != nil {
		return ""
	}
	return id
}

// isRoutable reports whether an address is plausibly outside the cluster.
//
// Private, loopback, link-local, and unspecified addresses inside a cluster are almost always
// pod or service addresses that resolution missed, so they are reported as unresolved rather
// than external (contracts/ids.md §6, rule 7).
func isRoutable(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsUnspecified() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10, carrier-grade NAT — used by some CNIs for pod networking.
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
			return false
		}
	}
	return true
}
