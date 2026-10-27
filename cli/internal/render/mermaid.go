package render

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

// Mermaid renders a graph response as a Mermaid flowchart (ADR-016 D-16.7). The browser's export
// builds the same text in TypeScript; both must equal cli/testdata/graph.export.mmd (D-16.8), so
// any change here is a change to that golden file and to frontend/src/features/export/.
//
// Node identifiers are synthetic (n1, n2, … in response order), so no name or label ever becomes
// Mermaid syntax; labels appear only inside quotes, escaped.
func Mermaid(r *api.GraphResponse) string {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}

	line("%%%% Runtime topology observed from %s to %s", mermaidStamp(r.Window.Start), mermaidStamp(r.Window.End))
	line("%%%% Edge labels count TCP connections, not requests.")
	if r.Summary.Truncated {
		line("%%%% Truncated by the server; this is not the whole graph.")
	}
	line("flowchart LR")

	ids := make(map[string]string, len(r.Nodes))
	byNamespace := map[string][]int{}
	var outside, canaries []int
	for i, n := range r.Nodes {
		ids[n.ID] = fmt.Sprintf("n%d", i+1)
		if n.Namespace == nil {
			outside = append(outside, i)
		} else {
			byNamespace[*n.Namespace] = append(byNamespace[*n.Namespace], i)
		}
		if n.Canary {
			canaries = append(canaries, i)
		}
	}

	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
	}
	slices.Sort(namespaces)
	for k, ns := range namespaces {
		line(`  subgraph ns%d["%s"]`, k+1, mermaidEscape(ns))
		for _, i := range byNamespace[ns] {
			n := r.Nodes[i]
			line(`    %s["%s<br/>%s"]`, ids[n.ID], mermaidEscape(n.Label), mermaidEscape(n.Kind))
		}
		line("  end")
	}
	for _, i := range outside {
		n := r.Nodes[i]
		if n.Kind == "External" {
			// A stadium: the cluster boundary, not a workload.
			line(`  %s(["%s"])`, ids[n.ID], mermaidEscape(n.Label))
		} else {
			line(`  %s["%s"]`, ids[n.ID], mermaidEscape(n.Label))
		}
	}

	for _, e := range r.Edges {
		src, okS := ids[e.SourceID]
		dst, okT := ids[e.TargetID]
		if !okS || !okT {
			continue
		}
		label := fmt.Sprintf("%s:%d · %d", e.Protocol, e.DestinationPort, e.ConnectionCount)
		if e.FailedConnectionCount != nil && *e.FailedConnectionCount > 0 {
			label += fmt.Sprintf(" · %d failed", *e.FailedConnectionCount)
		}
		// Dashed when nothing succeeded, as the canvas draws a failed-only edge.
		arrowStyle := "-->"
		if e.ConnectionCount == 0 {
			arrowStyle = "-.->"
		}
		line(`  %s %s|"%s"| %s`, src, arrowStyle, mermaidEscape(label), dst)
	}

	if len(canaries) > 0 {
		line("  classDef canary fill:#fde2e2,stroke:#c62828,stroke-width:3px")
		names := make([]string, len(canaries))
		for k, i := range canaries {
			names[k] = ids[r.Nodes[i].ID]
		}
		line("  class %s canary", strings.Join(names, ","))
	}
	return b.String()
}

// mermaidStamp drops sub-second precision, as the TypeScript export does, so both agree.
func mermaidStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// mermaidEscape replaces every character that could end a quoted label or start Mermaid syntax
// with its entity code, in one pass so an inserted "#" is never escaped again.
func mermaidEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '#':
			b.WriteString("#35;")
		case '"':
			b.WriteString("#quot;")
		case '<':
			b.WriteString("#lt;")
		case '>':
			b.WriteString("#gt;")
		case '|':
			b.WriteString("#124;")
		case '&':
			b.WriteString("#amp;")
		case '`':
			b.WriteString("#96;")
		case '\r', '\n':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
