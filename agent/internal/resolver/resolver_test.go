package resolver

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/contract"
)

const testCluster = "kind-topology"

// fakeCaches is a hand-rolled Caches implementation. Resolution is pure logic over this
// interface precisely so these tests need no cluster, no client-go fakes, and no network.
type fakeCaches struct {
	pods      map[string]podEntry        // IP -> pod
	clusterIP map[string]ServiceRef      // ClusterIP -> Service
	endpoints map[string][]endpointEntry // pod IP -> services backing it
	owners    map[string]OwnerRef        // "ns/ReplicaSet/name" -> next owner
	nodeIPs   map[string]bool
}

type podEntry struct {
	namespace string
	name      string
	owner     OwnerRef
	node      string
}

type endpointEntry struct {
	ref  ServiceRef
	port uint16
}

func newFake() *fakeCaches {
	return &fakeCaches{
		pods:      map[string]podEntry{},
		clusterIP: map[string]ServiceRef{},
		endpoints: map[string][]endpointEntry{},
		owners:    map[string]OwnerRef{},
		nodeIPs:   map[string]bool{},
	}
}

func (f *fakeCaches) withPod(ip, ns, name string, owner OwnerRef) *fakeCaches {
	f.pods[ip] = podEntry{namespace: ns, name: name, owner: owner}
	return f
}

// withPodOnNode records which node runs the pod, for the node-scoped observation filter.
func (f *fakeCaches) withPodOnNode(ip, ns, name, node string, owner OwnerRef) *fakeCaches {
	f.pods[ip] = podEntry{namespace: ns, name: name, owner: owner, node: node}
	return f
}

func (f *fakeCaches) NodeForPodIP(ip netip.Addr) (string, bool) {
	entry, ok := f.pods[ip.String()]
	if !ok || entry.node == "" {
		return "", false
	}
	return entry.node, true
}

func (f *fakeCaches) withClusterIP(ip, ns, name string) *fakeCaches {
	f.clusterIP[ip] = ServiceRef{Namespace: ns, Name: name}
	return f
}

func (f *fakeCaches) withEndpoint(podIP, ns, svc string, port uint16) *fakeCaches {
	f.endpoints[podIP] = append(f.endpoints[podIP], endpointEntry{
		ref: ServiceRef{Namespace: ns, Name: svc}, port: port,
	})
	return f
}

func (f *fakeCaches) withReplicaSetOwner(ns, rsName string, owner OwnerRef) *fakeCaches {
	f.owners[ns+"/ReplicaSet/"+rsName] = owner
	return f
}

func (f *fakeCaches) withNodeIP(ip string) *fakeCaches {
	f.nodeIPs[ip] = true
	return f
}

func (f *fakeCaches) PodByIP(ip netip.Addr) (string, string, OwnerRef, bool) {
	p, ok := f.pods[ip.String()]
	if !ok {
		return "", "", OwnerRef{}, false
	}
	return p.namespace, p.name, p.owner, true
}

func (f *fakeCaches) ServiceByClusterIP(ip netip.Addr) (string, string, bool) {
	s, ok := f.clusterIP[ip.String()]
	if !ok {
		return "", "", false
	}
	return s.Namespace, s.Name, true
}

func (f *fakeCaches) ServicesForEndpoint(ip netip.Addr, port uint16) []ServiceRef {
	var out []ServiceRef
	for _, e := range f.endpoints[ip.String()] {
		if e.port == port {
			out = append(out, e.ref)
		}
	}
	return out
}

// EndpointsForService inverts the same table ServicesForEndpoint reads, so the two directions
// cannot disagree in a test the way two hand-maintained maps eventually would.
func (f *fakeCaches) EndpointsForService(namespace, name string) []netip.Addr {
	want := ServiceRef{Namespace: namespace, Name: name}
	var out []netip.Addr
	for podIP, entries := range f.endpoints {
		for _, e := range entries {
			if e.ref != want {
				continue
			}
			if a, err := netip.ParseAddr(podIP); err == nil {
				out = append(out, a)
			}
			break
		}
	}
	return out
}

func (f *fakeCaches) ResolveOwner(namespace string, owner OwnerRef) (OwnerRef, bool) {
	next, ok := f.owners[namespace+"/"+owner.Kind+"/"+owner.Name]
	return next, ok
}

func (f *fakeCaches) IsNodeIP(ip netip.Addr) bool { return f.nodeIPs[ip.String()] }

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
}

func wantID(t *testing.T, ns, kind, name string) string {
	t.Helper()
	id, err := contract.BuildNodeID(testCluster, ns, kind, name)
	if err != nil {
		t.Fatalf("build id: %v", err)
	}
	return id
}

// ── T-2.5: owner resolution and workload collapse ──────────────────────────────────────────

func TestSourceCollapsesPodThroughReplicaSetToDeployment(t *testing.T) {
	f := newFake().
		withPod("10.244.1.7", "demo", "client-7d9f8b-x2jf", OwnerRef{Kind: "ReplicaSet", Name: "client-7d9f8b"}).
		withReplicaSetOwner("demo", "client-7d9f8b", OwnerRef{Kind: "Deployment", Name: "client"})

	got := New(testCluster, f).ResolveSource(addr(t, "10.244.1.7"))

	if got.Kind != "Deployment" || got.Name != "client" {
		t.Errorf("got kind=%q name=%q, want Deployment/client", got.Kind, got.Name)
	}
	if want := wantID(t, "demo", "Deployment", "client"); got.ID != want {
		t.Errorf("ID = %q, want %q", got.ID, want)
	}
	if got.Class != ClassWorkload {
		t.Errorf("Class = %q, want %q", got.Class, ClassWorkload)
	}
}

// The guarantee that makes the graph readable during pod churn: every replica of a Deployment
// shares one identity, so N pods produce one node and one edge, not N.
func TestReplicasOfOneDeploymentShareOneIdentity(t *testing.T) {
	f := newFake().
		withReplicaSetOwner("demo", "backend-abc", OwnerRef{Kind: "Deployment", Name: "backend"})
	for _, ip := range []string{"10.244.1.10", "10.244.2.11", "10.244.3.12"} {
		f.withPod(ip, "demo", "backend-abc-"+ip, OwnerRef{Kind: "ReplicaSet", Name: "backend-abc"})
	}

	r := New(testCluster, f)
	ids := map[string]bool{}
	for _, ip := range []string{"10.244.1.10", "10.244.2.11", "10.244.3.12"} {
		ids[r.ResolveSource(addr(t, ip)).ID] = true
	}

	if len(ids) != 1 {
		t.Fatalf("three replicas produced %d distinct identities, want 1: %v", len(ids), ids)
	}
	if _, ok := ids[wantID(t, "demo", "Deployment", "backend")]; !ok {
		t.Errorf("collapsed identity is not the Deployment: %v", ids)
	}
}

func TestDirectWorkloadOwners(t *testing.T) {
	cases := []struct{ kind, name string }{
		{"StatefulSet", "postgres"},
		{"DaemonSet", "fluentd"},
		{"Job", "migrate"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			f := newFake().withPod("10.244.5.5", "data", "pod-xyz",
				OwnerRef{Kind: tc.kind, Name: tc.name})

			got := New(testCluster, f).ResolveSource(addr(t, "10.244.5.5"))

			if got.Kind != tc.kind || got.Name != tc.name {
				t.Errorf("got %s/%s, want %s/%s", got.Kind, got.Name, tc.kind, tc.name)
			}
			if want := wantID(t, "data", tc.kind, tc.name); got.ID != want {
				t.Errorf("ID = %q, want %q", got.ID, want)
			}
		})
	}
}

// A Job owned by a CronJob must stop at the Job: CronJob is not one of the six allowed kinds.
func TestJobDoesNotWalkThroughToCronJob(t *testing.T) {
	f := newFake().
		withPod("10.244.5.6", "batch", "nightly-abc", OwnerRef{Kind: "Job", Name: "nightly"}).
		withReplicaSetOwner("batch", "nightly", OwnerRef{Kind: "CronJob", Name: "nightly-cron"})

	got := New(testCluster, f).ResolveSource(addr(t, "10.244.5.6"))

	if got.Kind != "Job" {
		t.Errorf("Kind = %q, want Job — CronJob is not an allowed kind", got.Kind)
	}
	if strings.Contains(got.ID, "CronJob") {
		t.Errorf("ID %q leaked a disallowed kind", got.ID)
	}
}

func TestOwnerlessPodKeepsPodIdentity(t *testing.T) {
	f := newFake().withPod("10.244.9.9", "demo", "debug-shell", OwnerRef{})

	got := New(testCluster, f).ResolveSource(addr(t, "10.244.9.9"))

	if got.Kind != "Pod" || got.Name != "debug-shell" {
		t.Errorf("got %s/%s, want Pod/debug-shell", got.Kind, got.Name)
	}
}

// A bare ReplicaSet is legal Kubernetes. ReplicaSet is not an allowed kind, so its pods stay
// Pod rather than gaining an identity the contract forbids.
func TestPodUnderOwnerlessReplicaSetStaysPod(t *testing.T) {
	f := newFake().withPod("10.244.9.10", "demo", "bare-rs-pod",
		OwnerRef{Kind: "ReplicaSet", Name: "bare-rs"})
	// No ResolveOwner entry: the ReplicaSet has no controller.

	got := New(testCluster, f).ResolveSource(addr(t, "10.244.9.10"))

	if got.Kind != "Pod" {
		t.Errorf("Kind = %q, want Pod", got.Kind)
	}
	if strings.Contains(got.ID, "ReplicaSet") {
		t.Errorf("ID %q contains ReplicaSet, which is not an allowed kind", got.ID)
	}
}

// ── ResolveSource classification ───────────────────────────────────────────────────────────

// A source is never a Service, even when the pod sits behind one. Services are destinations.
func TestSourceIsNeverAServiceEvenWhenBehindOne(t *testing.T) {
	f := newFake().
		withPod("10.244.1.20", "demo", "backend-xyz", OwnerRef{Kind: "ReplicaSet", Name: "backend-rs"}).
		withReplicaSetOwner("demo", "backend-rs", OwnerRef{Kind: "Deployment", Name: "backend"}).
		withEndpoint("10.244.1.20", "demo", "backend", 8080)

	got := New(testCluster, f).ResolveSource(addr(t, "10.244.1.20"))

	if got.Class == ClassService {
		t.Fatal("a source resolved to a Service; Services do not originate connections")
	}
	if got.Kind != "Deployment" {
		t.Errorf("Kind = %q, want Deployment", got.Kind)
	}
}

func TestSourceNodeIPIsHost(t *testing.T) {
	f := newFake().withNodeIP("172.18.0.3")

	got := New(testCluster, f).ResolveSource(addr(t, "172.18.0.3"))

	if got.Class != ClassHost {
		t.Errorf("Class = %q, want %q", got.Class, ClassHost)
	}
	if got.IsGraphable() {
		t.Error("host traffic must be excluded from the default application graph")
	}
}

// A source is never External by construction: only active opens reach the resolver, so the
// initiator is always local. An unknown source is unresolved, not external.
func TestUnknownSourceIsUnresolvedNotExternal(t *testing.T) {
	got := New(testCluster, newFake()).ResolveSource(addr(t, "203.0.113.7"))

	if got.Class == ClassExternal {
		t.Fatal("a source resolved to External; only active opens reach here, so the initiator is local")
	}
	if got.Class != ClassUnresolved {
		t.Errorf("Class = %q, want %q", got.Class, ClassUnresolved)
	}
}

// ── T-2.6: the destination ladder, first match wins ────────────────────────────────────────

// T-9.1: a ClusterIP resolves PAST the Service to the workload serving it. Before ADR-009 this
// returned Service/backend, and that is exactly what made the graph impossible to connect.
func TestDestinationClusterIPResolvesToBackingWorkload(t *testing.T) {
	f := newFake().
		withClusterIP("10.96.0.10", "demo", "backend").
		withPod("10.244.1.11", "demo", "backend-abc-1", OwnerRef{Kind: "ReplicaSet", Name: "backend-abc"}).
		withReplicaSetOwner("demo", "backend-abc", OwnerRef{Kind: "Deployment", Name: "backend"}).
		withEndpoint("10.244.1.11", "demo", "backend", 8080)

	got := New(testCluster, f).ResolveDestination(addr(t, "10.96.0.10"), 8080)

	if got.Class != ClassWorkload || got.Kind != "Deployment" || got.Name != "backend" {
		t.Errorf("got class=%q %s/%s, want workload Deployment/backend",
			got.Class, got.Kind, got.Name)
	}
	if want := wantID(t, "demo", "Deployment", "backend"); got.ID != want {
		t.Errorf("ID = %q, want %q", got.ID, want)
	}
}

// T-9.5: the property the whole of ADR-009 exists for. The same workload reached as a source and
// as a destination must produce ONE id, or the graph cannot connect at the hop between them.
func TestSourceAndDestinationAgreeOnOneWorkloadIdentity(t *testing.T) {
	f := newFake().
		withClusterIP("10.96.0.10", "demo", "backend").
		withPod("10.244.1.11", "demo", "backend-abc-1", OwnerRef{Kind: "ReplicaSet", Name: "backend-abc"}).
		withReplicaSetOwner("demo", "backend-abc", OwnerRef{Kind: "Deployment", Name: "backend"}).
		withEndpoint("10.244.1.11", "demo", "backend", 8080)

	r := New(testCluster, f)

	// frontend -> backend, through the Service's ClusterIP.
	asDestination := r.ResolveDestination(addr(t, "10.96.0.10"), 8080)
	// backend -> redis, observed leaving a backend pod.
	asSource := r.ResolveSource(addr(t, "10.244.1.11"))

	if asDestination.ID != asSource.ID {
		t.Fatalf("backend has two identities: destination %q, source %q — the chain "+
			"frontend -> backend -> redis cannot connect", asDestination.ID, asSource.ID)
	}
}

// T-9.2: nothing is serving, so there is no workload to name. Keeping the Service is the honest
// answer — inventing one would be worse than a node that says "a Service, and that is all I know".
func TestClusterIPWithNoReadyEndpointsKeepsTheService(t *testing.T) {
	f := newFake().withClusterIP("10.96.0.10", "demo", "backend")

	got := New(testCluster, f).ResolveDestination(addr(t, "10.96.0.10"), 8080)

	if got.Class != ClassService || got.Name != "backend" {
		t.Errorf("got class=%q name=%q, want service/backend", got.Class, got.Name)
	}
	if want := wantID(t, "demo", "Service", "backend"); got.ID != want {
		t.Errorf("ID = %q, want %q", got.ID, want)
	}
}

// T-9.3: a Service fronting two DIFFERENT workloads is a real fan-out. Collapsing it onto one of
// them would invent a dependency that was never observed, so the Service stays.
func TestClusterIPSpanningSeveralWorkloadsKeepsTheService(t *testing.T) {
	f := newFake().
		withClusterIP("10.96.0.20", "demo", "api").
		withPod("10.244.1.31", "demo", "api-blue-1", OwnerRef{Kind: "StatefulSet", Name: "api-blue"}).
		withPod("10.244.1.32", "demo", "api-green-1", OwnerRef{Kind: "StatefulSet", Name: "api-green"}).
		withEndpoint("10.244.1.31", "demo", "api", 8080).
		withEndpoint("10.244.1.32", "demo", "api", 8080)

	got := New(testCluster, f).ResolveDestination(addr(t, "10.96.0.20"), 8080)

	if got.Class != ClassService {
		t.Fatalf("got %s/%s; a Service backed by two workloads must not collapse onto either",
			got.Kind, got.Name)
	}
}

// Several endpoints collapsing to ONE workload is a Deployment with replicas, not ambiguity. It
// is the single most common shape in any cluster and must resolve, not fall back.
func TestClusterIPWithManyReplicasResolvesToTheOneWorkload(t *testing.T) {
	f := newFake().
		withClusterIP("10.96.0.30", "demo", "frontend").
		withReplicaSetOwner("demo", "frontend-rs", OwnerRef{Kind: "Deployment", Name: "frontend"})
	for _, ip := range []string{"10.244.4.1", "10.244.4.2", "10.244.4.3"} {
		f.withPod(ip, "demo", "frontend-rs-"+ip, OwnerRef{Kind: "ReplicaSet", Name: "frontend-rs"}).
			withEndpoint(ip, "demo", "frontend", 80)
	}

	got := New(testCluster, f).ResolveDestination(addr(t, "10.96.0.30"), 80)

	if got.Class != ClassWorkload || got.Name != "frontend" {
		t.Errorf("got class=%q name=%q, want workload/frontend — replicas are not ambiguity",
			got.Class, got.Name)
	}
}

// T-9.4: reached at its pod IP instead of through the ClusterIP, the answer is the same workload.
// The two paths must not disagree, or the same dependency splits into two nodes again.
func TestDestinationPodIPBehindOneServiceResolvesToItsWorkload(t *testing.T) {
	f := newFake().
		withPod("10.244.2.11", "data", "redis-0", OwnerRef{Kind: "StatefulSet", Name: "redis"}).
		withEndpoint("10.244.2.11", "data", "redis", 6379)

	got := New(testCluster, f).ResolveDestination(addr(t, "10.244.2.11"), 6379)

	if got.Class != ClassWorkload || got.Kind != "StatefulSet" || got.Name != "redis" {
		t.Errorf("got class=%q %s/%s, want workload StatefulSet/redis",
			got.Class, got.Kind, got.Name)
	}
}

// Several Services selecting the same pod and port. The WORKLOAD was already the answer here
// before ADR-009 and still is; what is ambiguous is only which Service carried the traffic, which
// is kept as diagnostic metadata and never delivered on the wire (ADR-009 D-9.4).
func TestAmbiguousServiceKeepsWorkloadAndRecordsCandidates(t *testing.T) {
	f := newFake().
		withPod("10.244.2.20", "demo", "api-abc-1", OwnerRef{Kind: "ReplicaSet", Name: "api-abc"}).
		withReplicaSetOwner("demo", "api-abc", OwnerRef{Kind: "Deployment", Name: "api"}).
		withEndpoint("10.244.2.20", "demo", "api-public", 8080).
		withEndpoint("10.244.2.20", "demo", "api-internal", 8080)

	got := New(testCluster, f).ResolveDestination(addr(t, "10.244.2.20"), 8080)

	if got.Class == ClassService {
		t.Fatal("an ambiguous destination resolved to a single Service; it must never guess")
	}
	if got.Kind != "Deployment" || got.Name != "api" {
		t.Errorf("got %s/%s, want the workload Deployment/api", got.Kind, got.Name)
	}
	if len(got.CandidateServices) != 2 {
		t.Fatalf("CandidateServices = %v, want both candidates preserved", got.CandidateServices)
	}
	for _, want := range []string{"demo/api-public", "demo/api-internal"} {
		found := false
		for _, c := range got.CandidateServices {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("candidate %q missing from %v", want, got.CandidateServices)
		}
	}
}

func TestDestinationPodWithNoServiceResolvesToWorkload(t *testing.T) {
	f := newFake().
		withPod("10.244.3.30", "demo", "worker-0", OwnerRef{Kind: "StatefulSet", Name: "worker"})

	got := New(testCluster, f).ResolveDestination(addr(t, "10.244.3.30"), 9000)

	if got.Class != ClassWorkload || got.Kind != "StatefulSet" {
		t.Errorf("got class=%q kind=%q, want workload/StatefulSet", got.Class, got.Kind)
	}
}

// The port is still part of the Service match, but since ADR-009 the pod path answers with the
// workload either way. What this pins is that a non-matching port cannot promote a destination
// back to a Service identity.
func TestServiceOnDifferentPortDoesNotMatch(t *testing.T) {
	f := newFake().
		withPod("10.244.2.40", "demo", "api-1", OwnerRef{Kind: "StatefulSet", Name: "api"}).
		withEndpoint("10.244.2.40", "demo", "api", 8080)

	got := New(testCluster, f).ResolveDestination(addr(t, "10.244.2.40"), 9999)

	if got.Class == ClassService {
		t.Error("a Service matched on the wrong port")
	}
	if got.Kind != "StatefulSet" {
		t.Errorf("Kind = %q, want the workload StatefulSet", got.Kind)
	}
}

func TestDestinationNodeIPIsHostAndNotGraphable(t *testing.T) {
	f := newFake().withNodeIP("172.18.0.4")

	got := New(testCluster, f).ResolveDestination(addr(t, "172.18.0.4"), 10250)

	if got.Class != ClassHost {
		t.Errorf("Class = %q, want %q", got.Class, ClassHost)
	}
	if got.IsGraphable() {
		t.Error("host traffic must not enter the default graph")
	}
}

// Routable public addresses collapse into the single EXTERNAL node — and the address itself
// must never survive into identity (ADR-001 §6).
func TestRoutableAddressesBecomeTheSingleExternalNode(t *testing.T) {
	for _, ip := range []string{"140.82.121.4", "8.8.8.8", "1.1.1.1", "203.0.113.7"} {
		t.Run(ip, func(t *testing.T) {
			got := New(testCluster, newFake()).ResolveDestination(addr(t, ip), 443)

			if got.Class != ClassExternal {
				t.Fatalf("Class = %q, want %q", got.Class, ClassExternal)
			}
			if got.ID != contract.ExternalNodeID {
				t.Errorf("ID = %q, want the single %q", got.ID, contract.ExternalNodeID)
			}
			for field, value := range map[string]string{
				"ID": got.ID, "Name": got.Name, "Namespace": got.Namespace,
			} {
				if strings.Contains(value, ip) {
					t.Errorf("%s = %q leaks the remote address; external IPs must never become identity",
						field, value)
				}
			}
		})
	}
}

// Unknown private or cluster-range addresses are unresolved, NOT external. This is what stops
// a CNI timing race being reported as internet traffic.
func TestUnknownPrivateAddressesAreUnresolvedNotExternal(t *testing.T) {
	cases := []struct{ ip, why string }{
		{"10.244.7.7", "pod CIDR the informer cache has not caught up with"},
		{"10.96.5.5", "service CIDR"},
		{"172.16.0.9", "RFC1918"},
		{"172.31.255.1", "RFC1918 upper bound"},
		{"192.168.1.50", "RFC1918"},
		{"127.0.0.1", "loopback"},
		{"169.254.1.1", "link-local"},
		{"100.64.0.1", "CGNAT, used by some CNIs for pod networking"},
		{"100.127.255.254", "CGNAT upper bound"},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			got := New(testCluster, newFake()).ResolveDestination(addr(t, tc.ip), 8080)

			if got.Class == ClassExternal {
				t.Fatalf("%s (%s) was reported as external; unresolved and external must stay distinct",
					tc.ip, tc.why)
			}
			if got.Class != ClassUnresolved {
				t.Errorf("Class = %q, want %q", got.Class, ClassUnresolved)
			}
		})
	}
}

// Ladder precedence: a ClusterIP wins even if the same address somehow also indexes a pod.
func TestClusterIPTakesPrecedenceOverPodLookup(t *testing.T) {
	f := newFake().
		withClusterIP("10.96.0.20", "demo", "frontend-svc").
		withPod("10.96.0.20", "demo", "some-pod", OwnerRef{Kind: "StatefulSet", Name: "some-set"})

	got := New(testCluster, f).ResolveDestination(addr(t, "10.96.0.20"), 80)

	if got.Class != ClassService || got.Name != "frontend-svc" {
		t.Errorf("got class=%q name=%q, want the Service to win", got.Class, got.Name)
	}
}

// ── Identity grammar ───────────────────────────────────────────────────────────────────────

// A name containing the ID separator would produce an ambiguous ID. Emitting a blank ID lets
// the aggregator drop and count it rather than shipping something a consumer could mis-split.
func TestNameContainingSeparatorYieldsNoID(t *testing.T) {
	f := newFake().withPod("10.244.4.4", "demo", "weird:name", OwnerRef{})

	got := New(testCluster, f).ResolveSource(addr(t, "10.244.4.4"))

	if got.ID != "" {
		t.Errorf("ID = %q, want empty so the aggregator rejects it", got.ID)
	}
}

func TestExternalEndpointShape(t *testing.T) {
	e := External()
	if e.ID != contract.ExternalNodeID {
		t.Errorf("ID = %q, want %q", e.ID, contract.ExternalNodeID)
	}
	if e.Namespace != "" {
		t.Errorf("Namespace = %q, want empty so it serialises as null", e.Namespace)
	}
	if !contract.AllowedKinds[e.Kind] {
		t.Errorf("Kind %q is not one of the six allowed kinds", e.Kind)
	}
	if !e.IsGraphable() {
		t.Error("external destinations belong in the graph")
	}
}

func TestGraphableClasses(t *testing.T) {
	cases := map[Class]bool{
		ClassWorkload:   true,
		ClassService:    true,
		ClassExternal:   true,
		ClassHost:       false,
		ClassUnresolved: false,
	}
	for class, want := range cases {
		if got := (Endpoint{Class: class}).IsGraphable(); got != want {
			t.Errorf("%s: IsGraphable() = %v, want %v", class, got, want)
		}
	}
}

// Node-scoped observation — ADR-002 §8.
//
// The ADR states the agent "sees only active opens originating on its own node". Nothing enforced
// it. On a real cluster that is free, because each node runs its own kernel; on kind the nodes are
// containers sharing one kernel, so every agent observed every connection and each was counted
// once per node — a threefold inflation of connection_count on a three-node cluster.
func TestOriginatesElsewhere(t *testing.T) {
	caches := newFake().
		withPodOnNode("10.1.0.5", "demo", "local-pod", "node-a", OwnerRef{}).
		withPodOnNode("10.1.0.6", "demo", "remote-pod", "node-b", OwnerRef{}).
		withPod("10.1.0.7", "demo", "node-unknown", OwnerRef{})
	r := New("c1", caches)

	cases := []struct {
		name   string
		ip     string
		node   string
		expect bool
		why    string
	}{
		{"a pod on this node is kept", "10.1.0.5", "node-a", false,
			"the connection originated here"},
		{"a pod on another node is dropped", "10.1.0.6", "node-a", true,
			"this is the kind shared-kernel case the filter exists for"},
		{"a pod whose node is unknown is kept", "10.1.0.7", "node-a", false,
			"dropping the unidentifiable would trade a counting error for data loss"},
		{"an unknown IP is kept", "203.0.113.9", "node-a", false,
			"external and host traffic must still be recorded"},
		{"no node name configured keeps everything", "10.1.0.6", "", false,
			"without NODE_NAME the filter cannot be applied safely"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.OriginatesElsewhere(netip.MustParseAddr(tc.ip), tc.node)
			if got != tc.expect {
				t.Errorf("OriginatesElsewhere(%s, %q) = %v, want %v — %s",
					tc.ip, tc.node, got, tc.expect, tc.why)
			}
		})
	}
}

// The property that makes the filter safe to apply: it never drops something it cannot identify.
func TestOriginatesElsewhereNeverDropsUnidentifiedTraffic(t *testing.T) {
	r := New("c1", newFake())

	for _, ip := range []string{"10.1.0.99", "192.168.1.1", "203.0.113.1", "127.0.0.1"} {
		if r.OriginatesElsewhere(netip.MustParseAddr(ip), "node-a") {
			t.Errorf("%s was dropped despite being unresolvable; an inflated count is visibly "+
				"wrong, a missing edge is not", ip)
		}
	}
}

// A node IP as SOURCE must resolve to host, not to a hostNetwork pod that happens to share it.
//
// Found on a Calico cluster, where the graph showed "etcd -> coredns:8080" and
// "kube-apiserver -> agent:8081". Neither is real: those are kubelet health probes, which
// originate from the node address. Every hostNetwork pod carries that same address as its PodIP,
// so the pod lookup returned an arbitrary one of them and named it as the source.
func TestSourceNodeIPResolvesToHostNotAHostNetworkPod(t *testing.T) {
	nodeIP := "10.0.0.10"

	// The situation on a real control-plane node: several hostNetwork pods indexed under the
	// node's own address, exactly as the informer would hold them.
	caches := newFake().
		withNodeIP(nodeIP).
		withPod(nodeIP, "kube-system", "etcd-node-a", OwnerRef{})

	r := New("c1", caches)
	got := r.ResolveSource(netip.MustParseAddr(nodeIP))

	if got.Class != ClassHost {
		t.Errorf("ResolveSource(node IP) = %v/%q, want ClassHost — a node address identifies the "+
			"node, not whichever hostNetwork pod the indexer listed first", got.Class, got.Name)
	}
	if got.IsGraphable() {
		t.Error("host traffic must be excluded from the default graph (contracts/ids.md rule 5)")
	}
}

// The ordering must not break the ordinary case it sits in front of.
func TestSourceOrdinaryPodStillResolvesToItsWorkload(t *testing.T) {
	caches := newFake().
		withNodeIP("10.0.0.10").
		withPod("10.1.2.3", "demo", "backend-abc", OwnerRef{Kind: "ReplicaSet", Name: "backend-7d9"}).
		withReplicaSetOwner("demo", "backend-7d9", OwnerRef{Kind: "Deployment", Name: "backend"})

	r := New("c1", caches)
	got := r.ResolveSource(netip.MustParseAddr("10.1.2.3"))

	if got.Class != ClassWorkload || got.Name != "backend" || got.Kind != "Deployment" {
		t.Errorf("ResolveSource(pod IP) = %s/%s (%v), want Deployment/backend (workload)",
			got.Kind, got.Name, got.Class)
	}
}
