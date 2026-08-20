import { useCallback, useEffect, useMemo, useState } from "react";

import { fetchNamespaces, fetchNodeDetail } from "./api/client";
import type { GraphQuery, NodeDetail, WindowPreset } from "./api/types";
import { WINDOW_PRESETS } from "./api/types";
import { Header, type Mode } from "./components/Header";
import { EmptyState, ErrorBanner, LoadingState, TruncationBanner } from "./components/States";
import { WindowStrip } from "./components/WindowStrip";
import { DetailsPanel } from "./features/details/DetailsPanel";
import { FilterPanel } from "./features/filters/FilterPanel";
import { TopologyCanvas } from "./features/graph/TopologyCanvas";
import { useGraph } from "./features/graph/useGraph";

export default function App() {
  const [mode, setMode] = useState<Mode>("live");
  const [preset, setPreset] = useState<WindowPreset>("5m");
  const [paused, setPaused] = useState(false);
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [selectedNamespaces, setSelectedNamespaces] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [includeExternal, setIncludeExternal] = useState(true);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<NodeDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);

  const query = useMemo<GraphQuery>(
    () => ({
      window: preset,
      namespace: selectedNamespaces.length ? selectedNamespaces : undefined,
      query: search || undefined,
      includeExternal,
    }),
    [preset, selectedNamespaces, search, includeExternal],
  );

  // History mode freezes the window, so polling would only add load without changing anything.
  const { graph, initialLoading, refreshing, error, lastUpdated, refresh } = useGraph(query, {
    paused: paused || mode === "history",
  });

  // The namespace list comes from the unfiltered window: filtering it by the current selection
  // would make a namespace disappear the moment you deselected it.
  useEffect(() => {
    const controller = new AbortController();
    fetchNamespaces({ window: preset }, controller.signal)
      .then((response) => setNamespaces(response.namespaces))
      .catch(() => {
        /* The filter list is a convenience; its failure must not disturb the graph. */
      });
    return () => controller.abort();
  }, [preset, lastUpdated]);

  useEffect(() => {
    if (!selectedId) {
      setDetail(null);
      return;
    }
    const controller = new AbortController();
    setDetailLoading(true);
    fetchNodeDetail(selectedId, { window: preset }, controller.signal)
      .then(setDetail)
      .catch(() => setDetail(null))
      .finally(() => setDetailLoading(false));
    return () => controller.abort();
  }, [selectedId, preset]);

  const selectedNode = useMemo(
    () => graph?.nodes.find((n) => n.id === selectedId) ?? null,
    [graph, selectedId],
  );

  const toggleNamespace = useCallback((ns: string) => {
    setSelectedNamespaces((current) =>
      current.includes(ns) ? current.filter((n) => n !== ns) : [...current, ns],
    );
  }, []);

  const clearFilters = useCallback(() => {
    setSelectedNamespaces([]);
    setSearch("");
    setIncludeExternal(true);
  }, []);

  const hasGraph = graph !== null;
  const isEmpty = hasGraph && graph.edges.length === 0;

  return (
    <div className="app">
      <Header
        mode={mode}
        onModeChange={setMode}
        paused={paused}
        onTogglePause={() => setPaused((p) => !p)}
        onRefresh={refresh}
        refreshing={refreshing}
        connected={hasGraph && error === null}
        windowPreset={preset}
        onWindowChange={(value) => setPreset(value as WindowPreset)}
        presets={WINDOW_PRESETS}
      />

      {graph && (
        <WindowStrip
          window={graph.window}
          summary={graph.summary}
          lastUpdated={lastUpdated}
          live={mode === "live" && !paused}
        />
      )}

      <div className="app__body">
        <FilterPanel
          namespaces={namespaces}
          selectedNamespaces={selectedNamespaces}
          onToggleNamespace={toggleNamespace}
          search={search}
          onSearch={setSearch}
          includeExternal={includeExternal}
          onToggleExternal={() => setIncludeExternal((v) => !v)}
          onClear={clearFilters}
        />

        <main className="canvas" aria-label="Topology graph">
          {error && <ErrorBanner message={error} onRetry={refresh} />}
          {graph?.summary.truncated && graph.summary.truncation_reason && (
            <TruncationBanner reason={graph.summary.truncation_reason} />
          )}

          {initialLoading && <LoadingState />}
          {!initialLoading && isEmpty && <EmptyState windowLabel={preset} />}
          {hasGraph && !isEmpty && (
            <TopologyCanvas graph={graph} selectedId={selectedId} onSelect={setSelectedId} />
          )}
        </main>

        <DetailsPanel
          node={selectedNode}
          detail={detail}
          loading={detailLoading}
          onClose={() => setSelectedId(null)}
        />
      </div>
    </div>
  );
}
