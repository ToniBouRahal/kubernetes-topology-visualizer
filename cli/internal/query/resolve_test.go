package query

import (
	"errors"
	"testing"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

func ptr(s string) *string { return &s }

// Two nodes called "backend" in one namespace — a Deployment and its Service — as the canonical
// fixture has them, plus a third in another namespace.
var nodes = []api.GraphNode{
	{ID: "k8s:kind-topology:demo:Deployment:backend", Kind: "Deployment", Namespace: ptr("demo"), Name: "backend"},
	{ID: "k8s:kind-topology:demo:Service:backend", Kind: "Service", Namespace: ptr("demo"), Name: "backend"},
	{ID: "opaque/7f3a|x", Kind: "Deployment", Namespace: ptr("staging"), Name: "backend"},
	{ID: "k8s:kind-topology:demo:Deployment:backend-worker", Kind: "Deployment", Namespace: ptr("demo"), Name: "backend-worker"},
	{ID: "external:EXTERNAL", Kind: "External", Name: "EXTERNAL"},
}

// T-15.2: several matches list the candidates.
func TestResolveAmbiguousListsCandidates(t *testing.T) {
	_, err := Resolve(nodes, "backend", "", "")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Candidates) != 3 {
		t.Fatalf("err = %v, want 3 candidates", err)
	}
	_, err = Resolve(nodes, "backend", "demo", "")
	if !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Fatalf("-n demo: err = %v, want 2 candidates", err)
	}
}

// T-15.2: name, namespace and kind fields narrow it to one; the ID comes back verbatim.
func TestResolveByFieldsReturnsTheIDVerbatim(t *testing.T) {
	for _, tc := range []struct{ ns, kind, want string }{
		{"demo", "Deployment", "k8s:kind-topology:demo:Deployment:backend"},
		{"demo", "Service", "k8s:kind-topology:demo:Service:backend"},
		{"staging", "", "opaque/7f3a|x"}, // an ID no parser would take apart correctly
	} {
		n, err := Resolve(nodes, "backend", tc.ns, tc.kind)
		if err != nil || n.ID != tc.want {
			t.Errorf("Resolve(backend, %q, %q) = %q, %v; want %q", tc.ns, tc.kind, n.ID, err, tc.want)
		}
	}
	if n, err := Resolve(nodes, "EXTERNAL", "", ""); err != nil || n.ID != "external:EXTERNAL" {
		t.Errorf("EXTERNAL = %q, %v", n.ID, err)
	}
}

// T-15.2: the match is on the name field exactly, never on a substring or a piece of the ID.
func TestResolveAbsentIsNotObserved(t *testing.T) {
	for _, name := range []string{"back", "Backend", "demo", "k8s:kind-topology:demo:Deployment:backend"} {
		if _, err := Resolve(nodes, name, "", ""); !errors.Is(err, ErrNotObserved) {
			t.Errorf("Resolve(%q) err = %v, want ErrNotObserved", name, err)
		}
	}
	if _, err := Resolve(nodes, "backend", "data", ""); !errors.Is(err, ErrNotObserved) {
		t.Errorf("wrong namespace: err = %v", err)
	}
	if ErrNotObserved.Error() != "not observed in this window" {
		t.Errorf("wording changed: %q", ErrNotObserved)
	}
}

func TestCanonicalKind(t *testing.T) {
	if k, err := CanonicalKind("statefulset"); err != nil || k != "StatefulSet" {
		t.Errorf("CanonicalKind(statefulset) = %q, %v", k, err)
	}
	if _, err := CanonicalKind("ReplicaSet"); err == nil {
		t.Error("ReplicaSet accepted; the contract collapses it into its Deployment")
	}
}
