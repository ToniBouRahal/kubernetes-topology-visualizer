import type { GraphNode, GraphResponse } from "../../api/types";

/**
 * The graph response as a Mermaid flowchart (ADR-016 D-16.7).
 *
 * Built from the API response, not from the canvas: the export states what was observed in the
 * window under the filters, independent of how the canvas happened to group or cap it.
 *
 * The CLI builds the same text in Go, and both are held to one golden file
 * (`cli/testdata/graph.export.mmd`, D-16.8) — change the format here and there together.
 */

const ESCAPES: Record<string, string> = {
  "#": "#35;",
  '"': "#quot;",
  "<": "#lt;",
  ">": "#gt;",
  "|": "#124;",
  "&": "#amp;",
  "`": "#96;",
  "\r": " ",
  "\n": " ",
};

/**
 * Make a label inert inside a quoted Mermaid string.
 *
 * One pass, character by character, so an entity this function writes is never escaped a second
 * time. Labels come from workload names, which whoever can create a workload chooses; without
 * this a name could close the quote and add Mermaid syntax of its own.
 */
export function escapeMermaid(text: string): string {
  let out = "";
  for (const ch of text) out += ESCAPES[ch] ?? ch;
  return out;
}

/** UTC to the second: both implementations must print the same instant the same way. */
function instant(value: string): string {
  return new Date(value).toISOString().replace(/\.\d{3}Z$/, "Z");
}

export function toMermaid(graph: GraphResponse): string {
  // Synthetic ids in response order (sorted by node id), so no user-controlled string is ever
  // used as Mermaid syntax.
  const ids = new Map<string, string>(graph.nodes.map((n, i) => [n.id, `n${i + 1}`]));

  const lines = [
    `%% Runtime topology observed from ${instant(graph.window.start)} to ${instant(graph.window.end)}`,
    "%% Edge labels count TCP connections, not requests.",
  ];
  if (graph.summary.truncated) {
    lines.push("%% Truncated by the server; this is not the whole graph.");
  }
  lines.push("flowchart LR");

  const byNamespace = new Map<string, GraphNode[]>();
  const outside: GraphNode[] = [];
  for (const node of graph.nodes) {
    if (node.namespace == null) {
      outside.push(node);
      continue;
    }
    const members = byNamespace.get(node.namespace) ?? [];
    members.push(node);
    byNamespace.set(node.namespace, members);
  }

  // Plain code-unit order, which is what Go's sort.Strings gives — localeCompare would not.
  const namespaces = [...byNamespace.keys()].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  namespaces.forEach((ns, index) => {
    lines.push(`  subgraph ns${index + 1}["${escapeMermaid(ns)}"]`);
    for (const node of byNamespace.get(ns)!) {
      lines.push(`    ${ids.get(node.id)}["${escapeMermaid(node.label)}<br/>${node.kind}"]`);
    }
    lines.push("  end");
  });
  for (const node of outside) {
    const label = escapeMermaid(node.label);
    lines.push(
      node.kind === "External" ? `  ${ids.get(node.id)}(["${label}"])` : `  ${ids.get(node.id)}["${label}"]`,
    );
  }

  for (const edge of graph.edges) {
    const source = ids.get(edge.source_id);
    const target = ids.get(edge.target_id);
    if (!source || !target) continue;
    const failed = edge.failed_connection_count ?? 0;
    let label = `${edge.protocol}:${edge.destination_port} · ${edge.connection_count}`;
    if (failed > 0) label += ` · ${failed} failed`;
    // Dashed when nothing succeeded, as on the canvas.
    const arrow = edge.connection_count === 0 ? "-.->" : "-->";
    lines.push(`  ${source} ${arrow}|"${label}"| ${target}`);
  }

  const canaries = graph.nodes.filter((n) => n.canary).map((n) => ids.get(n.id));
  if (canaries.length > 0) {
    lines.push("  classDef canary fill:#fde2e2,stroke:#c62828,stroke-width:3px");
    lines.push(`  class ${canaries.join(",")} canary`);
  }

  return `${lines.join("\n")}\n`;
}

/** File name for a download: the window's start, colon-free so every filesystem accepts it. */
export function exportFileName(graph: GraphResponse): string {
  return `topology-${instant(graph.window.start).replace(/:/g, "")}.mmd`;
}

/**
 * Hand the text to the browser as a file. A Blob URL and a temporary anchor: no server round
 * trip, and nothing the CSP's `script-src 'self'` would need loosening for.
 */
export function downloadMermaid(graph: GraphResponse): void {
  const url = URL.createObjectURL(new Blob([toMermaid(graph)], { type: "text/plain" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = exportFileName(graph);
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}
