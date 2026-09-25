import { render, screen, cleanup, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { DetailsPanel } from "../src/features/details/DetailsPanel";
import type { GraphNode, NodeDetail } from "../src/api/types";
afterEach(cleanup);

const node = {id: "a", name: "a", label: "a", namespace: "web", kind: "Deployment", first_seen: "2026-01-01", last_seen: "2026-01-02", attributes: {}} as GraphNode;
const detail: NodeDetail = {node, window: {start: node.first_seen, end: node.last_seen}, incoming: [], outgoing: [
  {first_seen: node.first_seen, last_seen: node.last_seen, node_id: "b", label: "b", protocol: "TCP", destination_port: 80, connection_count: 3, failed_connection_count: 0, connect_latency_count: 3, connect_latency_sum_us: 12000},
  {first_seen: node.first_seen, last_seen: node.last_seen, node_id: "c", label: "c", protocol: "TCP", destination_port: 443, connection_count: 0, failed_connection_count: 2},
]};

/** The row for one observed link, found by the direction written on it. */
function row(pair: string): HTMLElement {
  return screen.getByText(pair).closest("li")!;
}

it("states each link's direction, port and counts — ADR-010 D-10.1", () => {
  render(<DetailsPanel node={node} detail={detail} loading={false} onClose={() => {}}/>);

  // The direction is on the row itself, not implied by the section heading above it.
  const healthy = row("a → b");
  expect(within(healthy).getByText("TCP:80")).toBeTruthy();
  expect(healthy).toHaveTextContent("3 successful");
  expect(healthy).toHaveTextContent("0 failed/aborted");
  expect(healthy).toHaveTextContent("mean TCP setup 4 ms");

  const failing = row("a → c");
  expect(within(failing).getByText("TCP:443")).toBeTruthy();
  expect(failing).toHaveTextContent("0 successful");
  expect(failing).toHaveTextContent("2 failed/aborted");
  // No samples means no reading, and the panel must not present that as 0 ms.
  expect(failing).toHaveTextContent("TCP setup time unmeasured");
});

it("never shows a workload kind — T-10.1", () => {
  render(<DetailsPanel node={node} detail={detail} loading={false} onClose={() => {}}/>);
  // The node above IS a Deployment; the panel knows that and deliberately does not say it.
  expect(screen.queryByText(/Deployment/)).toBeNull();
  // What it says instead is the namespace, in words as well as in the colour.
  expect(screen.getByText("web")).toBeTruthy();
});

it("offers Grafana links only when configured, in a new tab, with the source named — ADR-012 T-12.4", () => {
  const grafana = {url: "https://g.example.com", workloadDashboardUid: "wl", lokiDatasourceUid: "loki"};
  render(<DetailsPanel node={node} detail={detail} loading={false} onClose={() => {}} grafana={grafana}/>);

  const metrics = screen.getByRole("link", {name: "Metrics ↗"});
  expect(metrics.getAttribute("href")).toContain("var-workload=a");
  expect(metrics.getAttribute("target")).toBe("_blank");
  expect(metrics.getAttribute("rel")).toBe("noopener noreferrer");
  expect(screen.getByRole("link", {name: "Logs ↗"})).toBeTruthy();
  // A button here could read as this tool's own view of the workload; the line says otherwise.
  expect(screen.getByText(/Not collected by this tool/)).toBeTruthy();
});

it("shows no Grafana section without a config, and none for a Service even with one", () => {
  const grafana = {url: "https://g.example.com", workloadDashboardUid: "wl", lokiDatasourceUid: "loki"};
  const {unmount} = render(<DetailsPanel node={node} detail={detail} loading={false} onClose={() => {}}/>);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.queryByText("In Grafana")).toBeNull();
  unmount();

  const service = {...node, kind: "Service"} as GraphNode;
  render(<DetailsPanel node={service} detail={detail} loading={false} onClose={() => {}} grafana={grafana}/>);
  expect(screen.queryByRole("link")).toBeNull();
});

it("distinguishes an unmeasured failure count from zero failures", () => {
  const unmeasured: NodeDetail = {...detail, outgoing: [
    {first_seen: node.first_seen, last_seen: node.last_seen, node_id: "d", label: "d", protocol: "TCP", destination_port: 5432, connection_count: 7},
  ]};
  render(<DetailsPanel node={node} detail={unmeasured} loading={false} onClose={() => {}}/>);
  expect(row("a → d")).toHaveTextContent("failed/aborted unmeasured");
});

it("renders nothing until a component is selected", () => {
  // The canvas keeps the full width until then; an empty panel would only take it away.
  const {container} = render(<DetailsPanel node={null} detail={null} loading={false} onClose={() => {}}/>);
  expect(container).toBeEmptyDOMElement();
});

it("separates when a component was seen in the window from its whole stored history", () => {
  // Viewing 14:00–15:00; the node kept talking until 21:33, long after the window.
  const lifetime = {...node, first_seen: "2026-09-25T12:53:00Z", last_seen: "2026-09-25T18:33:00Z"};
  const inWindow: NodeDetail = {...detail, node: lifetime, incoming: [], outgoing: [
    {...detail.outgoing[0]!, first_seen: "2026-09-25T14:00:05Z", last_seen: "2026-09-25T14:40:00Z"},
    {...detail.outgoing[1]!, first_seen: "2026-09-25T14:10:00Z", last_seen: "2026-09-25T14:59:30Z"},
  ]};
  render(<DetailsPanel node={lifetime} detail={inWindow} loading={false} onClose={() => {}}/>);
  const value = (label: string) => screen.getByText(label).nextElementSibling!.textContent;
  expect(value("First seen in this window")).toBe(new Date("2026-09-25T14:00:05Z").toLocaleString());
  expect(value("Last seen in this window")).toBe(new Date("2026-09-25T14:59:30Z").toLocaleString());
  expect(value("Last seen in stored history")).toBe(new Date("2026-09-25T18:33:00Z").toLocaleString());
});

it("says a link was already active rather than show a first sighting before the window", () => {
  const inWindow: NodeDetail = {...detail, window: {start: "2026-09-25T14:00:00Z", end: "2026-09-25T15:00:00Z"}, incoming: [], outgoing: [
    {...detail.outgoing[0]!, first_seen: "2026-09-25T13:59:57Z", last_seen: "2026-09-25T14:30:00Z"},
  ]};
  render(<DetailsPanel node={node} detail={inWindow} loading={false} onClose={() => {}}/>);
  expect(screen.getByText("First seen in this window").nextElementSibling!.textContent)
    .toBe(`already active at ${new Date("2026-09-25T14:00:00Z").toLocaleString()}`);
});
