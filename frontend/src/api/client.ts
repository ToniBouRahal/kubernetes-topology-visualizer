/**
 * Typed API client.
 *
 * Thin by design: query construction, cancellation, and error normalisation. Everything else is
 * the generated schema's job.
 */
import type { DiffQuery, DiffResponse, GraphQuery, GraphResponse, NamespaceList, NodeDetail } from "./types";

/** Same-origin in production; Vite proxies to the port-forwarded backend in development. */
const BASE = import.meta.env.VITE_API_BASE ?? "";

/**
 * A failed request, carrying the backend's own explanation when it sent one.
 *
 * The backend returns a stable envelope with a `detail` and a `request_id`; surfacing both means
 * a user-visible error can be matched to a backend log line instead of being a dead end.
 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly detail: string,
    readonly requestId?: string,
  ) {
    super(detail || `request failed with status ${status}`);
    this.name = "ApiError";
  }
}

function buildQuery(query: GraphQuery): string {
  const params = new URLSearchParams();

  if (query.window) params.set("window", query.window);
  if (query.from) params.set("from", query.from);
  if (query.to) params.set("to", query.to);
  if (query.kind) params.set("kind", query.kind);
  if (query.query) params.set("query", query.query);

  // Repeated key, not a comma-joined value: the API declares `namespace` as repeatable, and a
  // namespace legitimately containing a comma would otherwise split into two filters.
  for (const ns of query.namespace ?? []) params.append("namespace", ns);

  if (query.includeExternal !== undefined) {
    params.set("include_external", String(query.includeExternal));
  }
  if (query.includeUnresolved !== undefined) {
    params.set("include_unresolved", String(query.includeUnresolved));
  }

  const encoded = params.toString();
  return encoded ? `?${encoded}` : "";
}

async function request<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`${BASE}${path}`, {
    signal,
    headers: { Accept: "application/json" },
  });

  if (!response.ok) {
    let detail = response.statusText;
    let requestId: string | undefined;
    try {
      const body = await response.json();
      if (typeof body?.detail === "string") detail = body.detail;
      if (typeof body?.request_id === "string") requestId = body.request_id;
    } catch {
      // A non-JSON error body (a proxy 502, say) is still a real failure; keep the status text.
    }
    throw new ApiError(response.status, detail, requestId);
  }

  return (await response.json()) as T;
}

export function fetchGraph(query: GraphQuery, signal?: AbortSignal): Promise<GraphResponse> {
  return request<GraphResponse>(`/api/v1/graph${buildQuery(query)}`, signal);
}

export function fetchNamespaces(query: GraphQuery, signal?: AbortSignal): Promise<NamespaceList> {
  const scoped: GraphQuery = { window: query.window, from: query.from, to: query.to };
  return request<NamespaceList>(`/api/v1/namespaces${buildQuery(scoped)}`, signal);
}

export function fetchNodeDetail(
  nodeId: string,
  query: GraphQuery,
  signal?: AbortSignal,
): Promise<NodeDetail> {
  // Node ids contain ':' separators, so they must be encoded as a single path segment. The id
  // stays opaque — it is passed through, never parsed (contracts/ids.md §2).
  const scoped: GraphQuery = { window: query.window, from: query.from, to: query.to };
  return request<NodeDetail>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}${buildQuery(scoped)}`,
    signal,
  );
}

export function fetchDiff(query: DiffQuery, signal?: AbortSignal): Promise<DiffResponse> {
  const params = new URLSearchParams({
    baseline_from: query.baselineFrom,
    baseline_to: query.baselineTo,
    current_from: query.currentFrom,
    current_to: query.currentTo,
  });

  for (const ns of query.namespace ?? []) params.append("namespace", ns);
  if (query.kind) params.set("kind", query.kind);
  if (query.query) params.set("query", query.query);
  if (query.includeExternal !== undefined) {
    params.set("include_external", String(query.includeExternal));
  }
  if (query.includeUnchanged !== undefined) {
    params.set("include_unchanged", String(query.includeUnchanged));
  }

  return request<DiffResponse>(`/api/v1/diff?${params.toString()}`, signal);
}

export function checkReady(signal?: AbortSignal): Promise<{ status: string }> {
  return request<{ status: string }>("/health/ready", signal);
}
