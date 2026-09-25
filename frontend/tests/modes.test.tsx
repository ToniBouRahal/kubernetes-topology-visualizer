import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

// jsdom cannot measure a canvas; the canvas's own behaviour is TopologyCanvas.test.tsx. This file
// is about which period the app asks for, so the drawing boundary is replaced.
vi.mock("@xyflow/react", () => ({
  ReactFlow: () => <div data-testid="canvas" />,
  BaseEdge: () => null, Background: () => null, Controls: () => null, Handle: () => null,
  BackgroundVariant: { Lines: "lines", Dots: "dots" }, MarkerType: { ArrowClosed: "arrow" }, Position: {},
  useReactFlow: () => ({ fitView: () => {} }), useStore: () => null,
}));

import App from "../src/App";

const window = { start: "2026-09-25T17:25:00Z", end: "2026-09-25T17:30:00Z" };
const graph = {
  generated_at: window.end, window,
  filters: { namespaces: [], include_external: true, include_unresolved: false },
  nodes: [{ id: "a", kind: "Deployment", namespace: "demo", name: "a", label: "a", first_seen: window.start, last_seen: window.end, attributes: {} },
    { id: "b", kind: "Deployment", namespace: "demo", name: "b", label: "b", first_seen: window.start, last_seen: window.end, attributes: {} }],
  edges: [{ id: "ab", source_id: "a", target_id: "b", protocol: "TCP", destination_port: 80, connection_count: 1, first_seen: window.start, last_seen: window.end }],
  summary: { node_count: 2, edge_count: 1, total_connections: 1, truncated: false },
};

let requests: URL[] = [];
const graphRequests = () => requests.filter((u) => u.pathname === "/api/v1/graph");
const last = () => graphRequests().at(-1)!;
/** The strip, not the header: the mode button also reads "Live". */
const stripSays = async (text: string) =>
  within(await screen.findByLabelText("Observation window")).findByText(text);

beforeEach(() => {
  requests = [];
  vi.stubGlobal("fetch", vi.fn(async (input: string) => {
    const url = new URL(input, "http://localhost");
    requests.push(url);
    if (url.pathname === "/config.json") return new Response("", { status: 404 });
    const body = url.pathname === "/api/v1/namespaces" ? { namespaces: ["demo"], window } : graph;
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

it("Live asks for a window relative to now and says LIVE — T-6.4", async () => {
  render(<App />);
  await waitFor(() => expect(graphRequests().length).toBeGreaterThan(0));
  expect(last().searchParams.get("window")).toBe("5m");
  expect(last().searchParams.has("from")).toBe(false);
  expect(await stripSays("Live")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Back to live" })).toBeNull();
});

it("History asks for a fixed from/to, looks different, and steps a whole window — T-6.4", async () => {
  render(<App />);
  await stripSays("Live");

  fireEvent.click(screen.getByRole("button", { name: "History" }));
  await waitFor(() => expect(last().searchParams.has("from")).toBe(true));
  const from = new Date(last().searchParams.get("from")!).getTime();
  const to = new Date(last().searchParams.get("to")!).getTime();
  expect(last().searchParams.has("window")).toBe(false);
  expect(to - from).toBe(5 * 60_000);

  // Stated in words, with the way back, and the length select reads as a length.
  expect(screen.getByText("History · not live")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Back to live" })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "5 min" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Pause/ })).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: /^Earlier/ }));
  await waitFor(() => expect(new Date(last().searchParams.get("from")!).getTime()).toBe(from - 5 * 60_000));

  // The namespace list is scoped to the same period, not to "the last 5 minutes".
  await waitFor(() => {
    const ns = requests.filter((u) => u.pathname === "/api/v1/namespaces").at(-1)!;
    expect(ns.searchParams.get("from")).toBe(last().searchParams.get("from"));
  });

  fireEvent.click(screen.getByRole("button", { name: "Back to live" }));
  await waitFor(() => expect(last().searchParams.get("window")).toBe("5m"));
  expect(await stripSays("Live")).toBeInTheDocument();
});

it("History does not poll; Live does", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  render(<App />);
  await stripSays("Live");
  const liveBefore = graphRequests().length;
  await act(async () => { await vi.advanceTimersByTimeAsync(5_100); });
  expect(graphRequests().length).toBeGreaterThan(liveBefore);

  fireEvent.click(screen.getByRole("button", { name: "History" }));
  await waitFor(() => expect(last().searchParams.has("from")).toBe(true));
  const historyBefore = graphRequests().length;
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(graphRequests().length).toBe(historyBefore);
});

it("a start in the future is refused in words, and the last good period stays on screen", async () => {
  render(<App />);
  await stripSays("Live");
  fireEvent.click(screen.getByRole("button", { name: "History" }));
  await waitFor(() => expect(last().searchParams.has("from")).toBe(true));
  const shown = last().searchParams.get("from");

  fireEvent.change(screen.getByLabelText("Start of the period"), { target: { value: "2099-01-01T00:00" } });
  expect(await screen.findByRole("alert")).toHaveTextContent("not happened yet");
  expect(last().searchParams.get("from")).toBe(shown);
});
