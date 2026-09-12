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
	// The destination is the workload either way (ADR-009 D-9.1); the candidates are kept for
	// diagnostics. They are deliberately NOT delivered on the wire — ADR-009 D-9.4 records why.
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

	// EndpointsForService returns the ready endpoint addresses backing a Service. It is the
	// reverse of ServicesForEndpoint and is what lets a ClusterIP resolve past the Service to
	// the workload serving it (ADR-009 D-9.1).
	EndpointsForService(namespace, name string) []netip.Addr

	// NodeForPodIP reports which node runs the pod holding ip, if it is a known pod.
	NodeForPodIP(ip netip.Addr) (string, bool)

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

// OriginatesElsewhere reports whether a connection from ip demonstrably started on a DIFFERENT
// node than nodeName.
//
// ADR-002 states the agent "sees only active opens originating on its own node". On a real
// cluster that holds for free: each node runs its own kernel, so its tracepoint only ever fires
// for its own sockets. On kind it does NOT — the "nodes" are containers on one shared host
// kernel, so every agent observes every connection cluster-wide and each one is counted once per
// agent. With three nodes that inflates every connection_count threefold.
//
// The check is deliberately one-sided. It returns true ONLY when the source is a known pod known
// to be running elsewhere; an unresolvable IP, a host-network pod, or a node-local process all
// return false and are kept. Dropping what cannot be identified would trade a counting error for
// a data-loss error, which is worse — an inflated count is visibly wrong, a missing edge is not.
func (r *Resolver) OriginatesElsewhere(ip netip.Addr, nodeName string) bool {
	if nodeName == "" {
		return false
	}
	node, ok := r.caches.NodeForPodIP(ip)
	if !ok {
		return false
	}
	return node != nodeName
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
	// NODE IP FIRST, before the pod lookup. This ordering is load-bearing.
	//
	// Every hostNetwork pod carries the node's address as its PodIP — on a control-plane node
	// that is etcd, kube-apiserver, kube-scheduler, kube-controller-manager, kube-proxy and the
	// CNI agent, all indexed under one IP — and so does the kubelet itself. When a kubelet health
	// probe arrives from that address, PodByIP returns whichever of them the indexer happens to
	// list first, and the probe is attributed to an arbitrary control-plane component.
	//
	// That produced visibly false edges under a real CNI: "etcd -> coredns:8080",
	// "kube-apiserver -> agent:8081". etcd does not call CoreDNS's health port; the kubelet does.
	//
	// A node IP identifies the node, not any process on it, so the honest answer is `host` —
	// which contracts/ids.md rule 5 already excludes from the default graph. Naming a specific
	// workload we cannot actually identify is worse than declining to name one.
	if r.caches.IsNodeIP(ip) {
		return host()
	}
	if ns, name, owner, ok := r.caches.PodByIP(ip); ok {
		return r.workloadFor(ns, name, owner)
	}
	return unresolved()
}

// ResolveDestination identifies the receiving end, applying the ladder in contracts/ids.md §6.
// First match wins.
//
// A destination resolves to the WORKLOAD that serves it, never to the Service in front of it
// except as a fallback (ADR-009 D-9.1). Resolving to the Service was the original rule and it made
// the graph impossible to connect: ResolveSource returns `Deployment:backend` and the old
// ResolveDestination returned `Service:backend`, so `frontend -> backend -> redis` was two
// disconnected components that shared no node. A Service has no process behind it; the dependency
// is on whatever answers.
func (r *Resolver) ResolveDestination(ip netip.Addr, port uint16) Endpoint {
	// 1/2. A ClusterIP: follow the Service to the workload behind it.
	if ns, name, ok := r.caches.ServiceByClusterIP(ip); ok {
		if workload, resolved := r.workloadBehindService(ns, name); resolved {
			return workload
		}
		// 2. No ready endpoint, or endpoints spanning several workloads. Keep the Service
		// rather than guess — see workloadBehindService.
		return r.service(ns, name)
	}

	podNS, podName, owner, isPod := r.caches.PodByIP(ip)

	if isPod {
		// 3/4/5. A pod IP, with or without Services in front of it. The answer is the pod's
		// workload in every case; the Services only affect what is recorded alongside it.
		endpoint := r.workloadFor(podNS, podName, owner)
		if matches := r.caches.ServicesForEndpoint(ip, port); len(matches) > 1 {
			// Several Services select this pod and port. The workload is unambiguous, but
			// which Service carried the traffic is not; keep the candidates for diagnostics.
			endpoint.CandidateServices = make([]string, 0, len(matches))
			for _, m := range matches {
				endpoint.CandidateServices = append(endpoint.CandidateServices, m.Namespace+"/"+m.Name)
			}
		}
		return endpoint
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

// workloadBehindService resolves a Service to the single workload serving it.
//
// It reports false — and the caller keeps the Service identity — in exactly two situations, both
// of which are honest answers rather than failures:
//
//   - No ready endpoints. The Service is scaled to zero, or the informer cache has not caught up.
//     There is no workload to name.
//   - Endpoints spanning several distinct workloads. That is a real fan-out, and picking one of
//     them would invent a dependency that was never observed. This mirrors the rule the ambiguous
//     multi-Service case already followed: preserve the ambiguity (contracts/ids.md §6, rule 2).
//
// Note that several endpoints collapsing to ONE workload is the common case, not ambiguity — it is
// simply a Deployment with several replicas, which is precisely what workload identity exists to
// collapse.
func (r *Resolver) workloadBehindService(namespace, name string) (Endpoint, bool) {
	var resolved Endpoint
	found := false

	for _, addr := range r.caches.EndpointsForService(namespace, name) {
		podNS, podName, owner, ok := r.caches.PodByIP(addr)
		if !ok {
			continue
		}
		workload := r.workloadFor(podNS, podName, owner)
		if workload.ID == "" {
			continue
		}
		if !found {
			resolved, found = workload, true
			continue
		}
		if workload.ID != resolved.ID {
			return Endpoint{}, false
		}
	}

	return resolved, found
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
