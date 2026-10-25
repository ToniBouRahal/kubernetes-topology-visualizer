package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

func load[T any](t *testing.T, name string) *T {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return &v
}

func assertLines(t *testing.T, got Text, want ...string) {
	t.Helper()
	if lines := got.Lines(); !slices.Equal(lines, want) {
		t.Errorf("lines differ\n got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestGraphIsTheREADMELineFormat(t *testing.T) {
	g := load[api.GraphResponse](t, "graph.json")
	assertLines(t, Graph(g, Options{}),
		"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
		"3 relationships, 5 nodes, 177 connections",
		"Deployment/backend  →  EXTERNAL           TCP:443   (4)",
		"Deployment/backend  →  StatefulSet/redis  TCP:6379  (143)",
		"Deployment/client   →  Service/backend    TCP:8080  (30, 2 failed/aborted)",
	)
}

func TestGraphRowsCarryEdgeIDsVerbatimWhenAsked(t *testing.T) {
	g := load[api.GraphResponse](t, "graph.json")
	rows := Graph(g, Options{IDs: true}).Rows
	for i, e := range g.Edges {
		if !strings.HasSuffix(rows[i], "  id="+e.ID) {
			t.Errorf("row %d %q does not end with the edge's id %q", i, rows[i], e.ID)
		}
	}
}

func TestEmptyGraphSaysNothingWasObserved(t *testing.T) {
	g := load[api.GraphResponse](t, "graph.empty.json")
	text := Graph(g, Options{})
	if len(text.Rows) != 0 {
		t.Fatalf("rows = %q, want none", text.Rows)
	}
	assertLines(t, text,
		"window 2026-08-10T08:00:00Z to 2026-08-10T09:00:00Z",
		"0 relationships, 0 nodes, 0 connections",
		"no relationships observed in this window",
	)
}

func TestGraphHeaderStatesTheServersTruncation(t *testing.T) {
	g := load[api.GraphResponse](t, "graph.json")
	reason := "edge limit 2000 reached"
	g.Summary.Truncated, g.Summary.TruncationReason = true, &reason
	if h := Graph(g, Options{}).Header; h[len(h)-1] != "truncated by the server: edge limit 2000 reached" {
		t.Errorf("header = %q, want the truncation reason last", h)
	}
}

func TestFailuresShowOnlyWhenNonZero(t *testing.T) {
	zero, two := int64(0), int64(2)
	for _, tc := range []struct {
		failed *int64
		want   string
	}{{nil, "(9)"}, {&zero, "(9)"}, {&two, "(9, 2 failed/aborted)"}} {
		if got := count(9, tc.failed); got != tc.want {
			t.Errorf("count(9, %v) = %q, want %q", tc.failed, got, tc.want)
		}
	}
}

func TestOneNameInTwoNamespacesIsQualifiedNotMerged(t *testing.T) {
	a, b := "team-a", "team-b"
	g := &api.GraphResponse{
		Nodes: []api.GraphNode{
			{ID: "n1", Kind: "Deployment", Namespace: &a, Name: "api"},
			{ID: "n2", Kind: "Deployment", Namespace: &b, Name: "api"},
			{ID: "n3", Kind: "Service", Namespace: &a, Name: "db"},
		},
		Edges: []api.GraphEdge{
			{ID: "e1", SourceID: "n1", TargetID: "n3", Protocol: "TCP", DestinationPort: 5432, ConnectionCount: 1},
			{ID: "e2", SourceID: "n2", TargetID: "n3", Protocol: "TCP", DestinationPort: 5432, ConnectionCount: 2},
		},
	}
	rows := Graph(g, Options{}).Rows
	want := []string{
		"team-a/Deployment/api  →  Service/db  TCP:5432  (1)",
		"team-b/Deployment/api  →  Service/db  TCP:5432  (2)",
	}
	if !slices.Equal(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

func TestDependenciesNamePeersFromTheGraphNodes(t *testing.T) {
	g := load[api.GraphResponse](t, "graph.query-backend.json")
	d := load[api.NodeDetail](t, "node.deployment-backend.json")
	assertLines(t, Dependencies(d, g.Nodes, Options{}),
		"Deployment/backend in namespace demo",
		"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
		"Deployment/backend  →  EXTERNAL           TCP:443   (4)",
		"Deployment/backend  →  StatefulSet/redis  TCP:6379  (143)",
	)
	// Without peer nodes the dependency's label is used, never a fragment of its ID.
	rows := Dependencies(d, nil, Options{}).Rows
	if !strings.Contains(rows[1], "→  redis ") {
		t.Errorf("row %q should name the peer by its label", rows[1])
	}
}

func TestDiffHeaderAndPrefixes(t *testing.T) {
	d := load[api.DiffResponse](t, "diff.json")
	assertLines(t, Diff(d, Options{}),
		"baseline 2026-08-10T10:00:00Z to 2026-08-10T11:00:00Z",
		"current  2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
		"1 new, 1 removed, 2 changed, 0 unchanged",
		"NEW              k8s:kind-topology:demo:Deployment:backend  →  external:EXTERNAL                             TCP:443   (4)",
		"REMOVED          k8s:kind-topology:demo:Deployment:backend  →  k8s:kind-topology:data:StatefulSet:legacy-db  TCP:5432  (12)",
		"CHANGED 100→143  k8s:kind-topology:demo:Deployment:backend  →  k8s:kind-topology:data:StatefulSet:redis      TCP:6379",
		"CHANGED 20→30    k8s:kind-topology:demo:Deployment:client   →  k8s:kind-topology:demo:Service:backend        TCP:8080",
	)
	d.Summary.Truncated = true
	if h := Diff(d, Options{}).Header; h[len(h)-1] != "truncated by the server" {
		t.Errorf("header = %q, want the truncated flag last", h)
	}
}

func TestEdgeAndNamespaces(t *testing.T) {
	assertLines(t, Edge(load[api.EdgeDetail](t, "edge.json"), Options{}),
		"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
		"Deployment/client  →  Service/backend  TCP:8080  (30, 2 failed/aborted)",
		"first seen 2026-08-10T11:29:51Z, last seen 2026-08-10T11:30:00Z",
	)
	assertLines(t, Namespaces(load[api.NamespaceList](t, "namespaces.json")),
		"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z", "data", "demo")
}
