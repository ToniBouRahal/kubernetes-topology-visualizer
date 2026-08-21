import { useCallback, useEffect, useMemo, useState } from "react";

import { fetchNamespaces, fetchNodeDetail } from "./api/client";
import type { GraphQuery, NodeDetail, WindowPreset } from "./api/types";
import { WINDOW_PRESETS } from "./api/types";
import { Header, type Mode } from "./components/Header";
import { EmptyState, ErrorBanner, LoadingState, TruncationBanner } from "./components/States";
import { WindowStrip } from "./components/WindowStrip";
import { DetailsPanel } from "./features/details/DetailsPanel";
import { FilterPanel } from "./features/filters/FilterPanel";
import { CompareCanvas } from "./features/graph/CompareCanvas";
import { TopologyCanvas } from "./features/graph/TopologyCanvas";
import { useDiff } from "./features/graph/useDiff";
import { CompareControls } from "./features/timerange/CompareControls";
import { adjacentPeriods, type CompareSpanId } from "./features/timerange/periods";
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
  const [compareSpan, setCompareSpan] = useState<CompareSpanId>("5m");
  const [includeUnchanged, setIncludeUnchanged] = useState(false);
  // Recomputed only when the span or a manual refresh changes it, so the compared periods stay
  // FIXED while the user reads them. Recomputing on every render would make the answer move.
  const [periods, setPeriods] = useState(() => adjacentPeriods(5));

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
    paused: paused || mode !== "live",
  });

  const diffQuery = useMemo(
    () =>
      mode === "compare"
        ? {
            ...periods,
            namespace: selectedNamespaces.length ? selectedNamespaces : undefined,
            query: search || undefined,
            includeExternal,
            includeUnchanged,
          }
        : null,
    [mode, periods, selectedNamespaces, search, includeExternal, includeUnchanged],
  );

  const { diff, loading: diffLoading, error: diffError, refresh: refreshDiff } = useDiff(diffQuery);

  const knownNodes = useMemo(
    () => new Map((graph?.nodes ?? []).map((n) => [n.id, n])),
    [graph],
  );

  const changeCompareSpan = useCallback((id: CompareSpanId) => {
    setCompareSpan(id);
    const minutes = { "5m": 5, "15m": 15, "1h": 60, "6h": 360 }[id];
    setPeriods(adjacentPeriods(minutes));
  }, []);

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

      {mode !== "compare" && graph && (
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
          extra={
            mode === "compare" ? (
              <CompareControls
                span={compareSpan}
                onSpanChange={changeCompareSpan}
                periods={periods}
                includeUnchanged={includeUnchanged}
                onToggleUnchanged={() => setIncludeUnchanged((v) => !v)}
                summary={diff?.summary ?? null}
                threshold={diff?.threshold_percent ?? null}
                onRefresh={() => {
                  const minutes = { "5m": 5, "15m": 15, "1h": 60, "6h": 360 }[compareSpan];
                  setPeriods(adjacentPeriods(minutes));
                }}
                loading={diffLoading}
              />
            ) : null
          }
        />

        <main className="canvas" aria-label="Topology graph">
          {mode === "compare" ? (
            <>
              {diffError && <ErrorBanner message={diffError} onRetry={refreshDiff} />}
              {diffLoading && !diff && <LoadingState />}
              {diff && diff.edges.length === 0 && (
                <div className="state" role="status">
                  <p className="state__title">No differences between these periods</p>
                  <p className="state__body">
                    Every observed relationship stayed within the {diff.threshold_percent}% change
                    threshold. Tick “Show unchanged” to see them anyway.
                  </p>
                </div>
              )}
              {diff && diff.edges.length > 0 && (
                <CompareCanvas
                  diff={diff}
                  knownNodes={knownNodes}
                  selectedId={selectedId}
                  onSelect={setSelectedId}
                />
              )}
            </>
          ) : (
          <>
          {error && <ErrorBanner message={error} onRetry={refresh} />}
          {graph?.summary.truncated && graph.summary.truncation_reason && (
            <TruncationBanner reason={graph.summary.truncation_reason} />
          )}

          {initialLoading && <LoadingState />}
          {!initialLoading && isEmpty && <EmptyState windowLabel={preset} />}
          {hasGraph && !isEmpty && (
            <TopologyCanvas graph={graph} selectedId={selectedId} onSelect={setSelectedId} />
          )}
          </>
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
