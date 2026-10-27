// Package render produces the README's line format — one relationship per line, aligned — from
// API responses (ADR-015 D-15.4). The CLI prints it and the MCP tools return it, so both say the
// same thing in the same words and the format is tested once.
//
// Every function returns lines, not a string, so a caller can cap the relationship rows without
// re-parsing text. Nodes are named from the response's kind, namespace and name fields; an ID is
// never parsed, only printed verbatim where no node fields exist (diff) or when asked for.
package render

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

const arrow = "→"

// Text is one rendered response. Header says what was queried (the window actually used, and
// the server's truncation, if any); Rows are relationship lines, one each; Notes follow them.
type Text struct {
	Header []string
	Rows   []string
	Notes  []string
}

// Lines is the whole text, header first.
func (t Text) Lines() []string {
	out := make([]string, 0, len(t.Header)+len(t.Rows)+len(t.Notes))
	out = append(out, t.Header...)
	out = append(out, t.Rows...)
	return append(out, t.Notes...)
}

// String is Lines joined, with a final newline.
func (t Text) String() string {
	lines := t.Lines()
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// Options adjusts the output for a consumer that needs more than a person does.
type Options struct {
	// IDs appends each relationship's opaque ID ("id=…", or "node=…" for a dependency, whose
	// response carries no edge ID) so a follow-up call can pass it back verbatim.
	IDs bool
}

// Namespaces renders GET /api/v1/namespaces.
func Namespaces(r *api.NamespaceList) Text {
	t := Text{Header: []string{windowLine(r.Window)}, Rows: append([]string(nil), r.Namespaces...)}
	if len(r.Namespaces) == 0 {
		t.Notes = []string{"no namespaces observed in this window"}
	}
	return t
}

// Graph renders GET /api/v1/graph.
func Graph(r *api.GraphResponse, o Options) Text {
	t := Text{Header: []string{
		windowLine(r.Window),
		fmt.Sprintf("%d relationships, %d nodes, %d connections",
			r.Summary.EdgeCount, r.Summary.NodeCount, r.Summary.TotalConnections),
	}}
	if r.Summary.Truncated || r.Summary.TruncationReason != nil {
		t.Header = append(t.Header, truncatedLine(r.Summary.TruncationReason))
	}
	names := newNamer(r.Nodes)
	rows := make([][]string, 0, len(r.Edges))
	for _, e := range r.Edges {
		row := []string{names.of(e.SourceID, e.SourceID), arrow, names.of(e.TargetID, e.TargetID),
			port(e.Protocol, e.DestinationPort), count(e.ConnectionCount, e.FailedConnectionCount)}
		if o.IDs {
			row = append(row, "id="+e.ID)
		}
		rows = append(rows, row)
	}
	t.Rows = align(rows)
	if len(t.Rows) == 0 {
		t.Notes = []string{"no relationships observed in this window"}
	}
	return t
}

// Dependencies renders GET /api/v1/nodes/{id}: what the node talks to, then what talks to it.
// peers are nodes known from another response (the graph used to resolve the name); a
// dependency's peer is named from them when present, from the dependency's label otherwise.
func Dependencies(d *api.NodeDetail, peers []api.GraphNode, o Options) Text {
	all := append([]api.GraphNode{d.Node}, peers...)
	names := newNamer(all)
	self := names.of(d.Node.ID, d.Node.Name)
	t := Text{Header: []string{nodeLine(d.Node, self), windowLine(d.Window)}}

	rows := make([][]string, 0, len(d.Outgoing)+len(d.Incoming))
	add := func(src, dst string, dep api.NodeDependency) {
		row := []string{src, arrow, dst, port(dep.Protocol, dep.DestinationPort),
			count(dep.ConnectionCount, dep.FailedConnectionCount)}
		if o.IDs {
			row = append(row, "node="+dep.NodeID)
		}
		rows = append(rows, row)
	}
	for _, dep := range d.Outgoing {
		add(self, names.of(dep.NodeID, dep.Label), dep)
	}
	for _, dep := range d.Incoming {
		add(names.of(dep.NodeID, dep.Label), self, dep)
	}
	t.Rows = align(rows)
	if len(t.Rows) == 0 {
		t.Notes = []string{"no relationships observed in this window"}
	}
	return t
}

// Diff renders GET /api/v1/diff. The response carries node IDs only, so they are printed
// verbatim rather than taken apart into names.
func Diff(r *api.DiffResponse, o Options) Text {
	s := r.Summary
	t := Text{Header: []string{
		"baseline " + period(r.Baseline),
		"current  " + period(r.Current),
		fmt.Sprintf("%d new, %d removed, %d changed, %d unchanged",
			s.NewCount, s.RemovedCount, s.ChangedCount, s.UnchangedCount),
	}}
	if s.Truncated {
		t.Header = append(t.Header, "truncated by the server")
	}
	rows := make([][]string, 0, len(r.Edges))
	for _, e := range r.Edges {
		var label, n string
		switch e.Classification {
		case api.DiffChanged:
			label = fmt.Sprintf("%s %d%s%d", e.Classification, e.BaselineConnectionCount, arrow, e.CurrentConnectionCount)
		case api.DiffRemoved:
			label, n = e.Classification, fmt.Sprintf("(%d)", e.BaselineConnectionCount)
		default:
			label, n = e.Classification, fmt.Sprintf("(%d)", e.CurrentConnectionCount)
		}
		row := []string{label, e.SourceID, arrow, e.TargetID, port(e.Protocol, e.DestinationPort), n}
		if o.IDs {
			row = append(row, "id="+e.ID)
		}
		rows = append(rows, row)
	}
	t.Rows = align(rows)
	if len(t.Rows) == 0 {
		t.Notes = []string{"no changes between the two periods"}
	}
	return t
}

// Edge renders GET /api/v1/edges/{id}.
func Edge(r *api.EdgeDetail, o Options) Text {
	names := newNamer([]api.GraphNode{r.Source, r.Target})
	e := r.Edge
	row := []string{names.of(r.Source.ID, r.Source.Name), arrow, names.of(r.Target.ID, r.Target.Name),
		port(e.Protocol, e.DestinationPort), count(e.ConnectionCount, e.FailedConnectionCount)}
	if o.IDs {
		row = append(row, "id="+e.ID)
	}
	return Text{
		Header: []string{windowLine(r.Window)},
		Rows:   align([][]string{row}),
		Notes:  []string{"first seen " + stamp(e.FirstSeen) + ", last seen " + stamp(e.LastSeen)},
	}
}

// Anomalies renders GET /api/v1/anomalies (ADR-016 D-16.3): one aligned row per anomaly,
// critical first in the server's order within each severity, each followed by its reason. The
// reason is what makes a finding checkable, so it is printed rather than left to -o json.
func Anomalies(r *api.AnomalyResponse, o Options) Text {
	names := newNamer(r.Nodes)
	s := r.Summary
	t := Text{Header: []string{
		windowLine(r.Window),
		"baseline " + period(r.Baseline),
		fmt.Sprintf("%d critical, %d warning", s.CriticalCount, s.WarningCount),
	}}
	if !s.BaselineObserved {
		t.Header = append(t.Header, "no baseline yet: new-relationship rules are off")
	}
	if s.Truncated {
		t.Header = append(t.Header, "truncated by the server")
	}

	// Severity first, so the rows that matter are at the top however long the list is; the
	// server's own order (edge key, then rule) is kept within each severity.
	ordered := slices.Clone(r.Anomalies)
	slices.SortStableFunc(ordered, func(a, b api.Anomaly) int {
		return severityRank(a.Severity) - severityRank(b.Severity)
	})

	rows := make([][]string, 0, len(ordered))
	for _, a := range ordered {
		row := []string{a.Severity, a.Type, names.of(a.SourceID, a.SourceID), arrow,
			names.of(a.TargetID, a.TargetID), port(a.Protocol, a.DestinationPort)}
		if o.IDs {
			row = append(row, "id="+a.EdgeID)
		}
		rows = append(rows, row)
	}
	for i, aligned := range align(rows) {
		t.Rows = append(t.Rows, aligned, "    "+ordered[i].Reason)
	}
	if len(t.Rows) == 0 {
		t.Notes = []string{"no anomalies in this window"}
	}
	return t
}

func severityRank(s string) int {
	if s == api.SeverityCritical {
		return 0
	}
	return 1
}

func windowLine(w api.TimeWindow) string { return "window " + period(w) }

func period(w api.TimeWindow) string { return stamp(w.Start) + " to " + stamp(w.End) }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func truncatedLine(reason *string) string {
	if reason == nil || *reason == "" {
		return "truncated by the server"
	}
	return "truncated by the server: " + *reason
}

func nodeLine(n api.GraphNode, name string) string {
	if n.Namespace == nil {
		return name
	}
	return name + " in namespace " + *n.Namespace
}

func port(protocol string, p int) string { return fmt.Sprintf("%s:%d", protocol, p) }

// count is "(344)", or "(344, 2 failed/aborted)" when setups failed. An absent failure count
// (not measured) and zero both print nothing extra.
func count(n int64, failed *int64) string {
	if failed != nil && *failed > 0 {
		return fmt.Sprintf("(%d, %d failed/aborted)", n, *failed)
	}
	return fmt.Sprintf("(%d)", n)
}

// namer names nodes "Kind/name" as the README does, and the external node by its name. Where two
// nodes in the same output would share "Kind/name" (one name in two namespaces) both gain their
// namespace, "ns/Kind/name", rather than reading as one workload.
type namer struct {
	byID  map[string]api.GraphNode
	short map[string]int
}

func newNamer(nodes []api.GraphNode) namer {
	n := namer{byID: map[string]api.GraphNode{}, short: map[string]int{}}
	for _, node := range nodes {
		if _, seen := n.byID[node.ID]; seen {
			continue
		}
		n.byID[node.ID] = node
		n.short[shortName(node)]++
	}
	return n
}

// of names the node with this ID, or returns fallback (a label, or the ID verbatim) when the
// node is not in the response.
func (n namer) of(id, fallback string) string {
	node, ok := n.byID[id]
	if !ok {
		return fallback
	}
	s := shortName(node)
	if n.short[s] > 1 && node.Namespace != nil {
		return *node.Namespace + "/" + s
	}
	return s
}

func shortName(n api.GraphNode) string {
	if n.Kind == "External" {
		return n.Name
	}
	return n.Kind + "/" + n.Name
}

// align pads every column but the last to its widest cell, two spaces apart, as in the README.
// Widths count runes so the arrow is one column.
func align(rows [][]string) []string {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)))
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}
