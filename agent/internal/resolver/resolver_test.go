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

func TestDestinationClusterIPResolvesToService(t *testing.T) {
	f := newFake().withClusterIP("10.96.0.10", "demo", "backend")

	got := New(testCluster, f).ResolveDestination(addr(t, "10.96.0.10"), 8080)

	if got.Class != ClassService || got.Name != "backend" {
		t.Errorf("got class=%q name=%q, want service/backend", got.Class, got.Name)
	}
	if want := wantID(t, "demo", "Service", "backend"); got.ID != want {
		t.Errorf("ID = %q, want %q", got.ID, want)
	}
}

func TestDestinationPodIPWithOneMatchingServiceResolvesToService(t *testing.T) {
	f := newFake().
		withPod("10.244.2.11", "data", "redis-0", OwnerRef{Kind: "StatefulSet", Name: "redis"}).
		withEndpoint("10.244.2.11", "data", "redis", 6379)

	got := New(testCluster, f).ResolveDestination(addr(t, "10.244.2.11"), 6379)

	if got.Class != ClassService || got.Name != "redis" {
		t.Errorf("got class=%q name=%q, want service/redis", got.Class, got.Name)
	}
}

// The most important case in the ladder. Several Services selecting the same pod and port is
// genuinely ambiguous; picking one would fabricate certainty the data does not support.
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

// A Service that exists but on a different port must not match: the port is part of the rule.
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
