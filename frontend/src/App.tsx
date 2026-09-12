import { useCallback, useEffect, useMemo, useState } from "react";

import { fetchNamespaces, fetchNodeDetail } from "./api/client";
import type { GraphQuery, NodeDetail, WindowPreset } from "./api/types";
import { WINDOW_PRESETS } from "./api/types";
import { Header, type Mode } from "./components/Header";
import {
  EmptyState,
  ErrorBanner,
  LoadingState,
  RenderBudgetBanner,
  TruncationBanner,
} from "./components/States";
import { WindowStrip } from "./components/WindowStrip";
import { DetailsPanel } from "./features/details/DetailsPanel";
import { FilterPanel } from "./features/filters/FilterPanel";
import { CompareCanvas } from "./features/graph/CompareCanvas";
import { NodeList } from "./features/graph/NodeList";
import { TopologyCanvas } from "./features/graph/TopologyCanvas";
import { useDiff } from "./features/graph/useDiff";
import { CompareControls, type CompareMode } from "./features/timerange/CompareControls";
import {
  adjacentPeriods,
  periodsFromMoments,
  periodsProblem,
  toLocalInputValue,
  type CompareSpanId,
} from "./features/timerange/periods";
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
  // What the canvas had to leave out to stay responsive; null when the whole graph fits.
  const [renderCap, setRenderCap] = useState<{
    shownEdges: number;
    totalEdges: number;
    hiddenNodes: number;
  } | null>(null);
  const [detail, setDetail] = useState<NodeDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [compareSpan, setCompareSpan] = useState<CompareSpanId>("5m");
  const [compareMode, setCompareMode] = useState<CompareMode>("recent");
  // datetime-local values, in the viewer's own wall-clock. Seeded an hour and two hours back so
  // the inputs open on something valid and non-overlapping rather than empty.
  const [baselineStart, setBaselineStart] = useState(() =>
    toLocalInputValue(new Date(Date.now() - 2 * 60 * 60_000)),
  );
  const [currentStart, setCurrentStart] = useState(() =>
    toLocalInputValue(new Date(Date.now() - 60 * 60_000)),
  );
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

  // A pair of periods the backend would reject is not requested at all. Letting it through would
  // spend a round trip to be told what is already known locally, and would leave the PREVIOUS
  // comparison's counts on screen beside the error — numbers that no longer describe the periods
  // in the inputs, which is worse than showing none.
  const diffQuery = useMemo(
    () =>
      mode === "compare" && periodsProblem(periods) === null
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

  const {
    diff,
    nodes: comparedNodes,
    loading: diffLoading,
    error: diffError,
    refresh: refreshDiff,
  } = useDiff(diffQuery);

  // Both periods' nodes, with the live graph's underneath as a fallback while the comparison is
  // still loading. useDiff fetches the real records rather than deriving them from ids, which
  // `contracts/ids.md` §2 forbids.
  const knownNodes = useMemo(() => {
    const merged = new Map((graph?.nodes ?? []).map((n) => [n.id, n]));
    for (const [id, node] of comparedNodes) merged.set(id, node);
    return merged;
  }, [graph, comparedNodes]);

  const spanMinutes = useCallback(
    (id: CompareSpanId) => ({ "5m": 5, "15m": 15, "1h": 60, "6h": 360 })[id],
    [],
  );

  // Recomputing the periods is always an explicit act — a span change, a moment change, or the
  // Compare button. The windows a reader is looking at must not move underneath them.
  const recomputePeriods = useCallback(
    (mode: CompareMode, id: CompareSpanId, baseline: string, current: string) => {
      const minutes = spanMinutes(id);
      if (mode === "recent") {
        setPeriods(adjacentPeriods(minutes));
        return;
      }
      // `new Date("YYYY-MM-DDTHH:mm")` parses as LOCAL time, which is what the input offers and
      // what the viewer meant; toISOString then converts to the UTC the contract requires.
      setPeriods(periodsFromMoments(new Date(baseline), new Date(current), minutes));
    },
    [spanMinutes],
  );

  const changeCompareMode = useCallback(
    (next: CompareMode) => {
      setCompareMode(next);
      recomputePeriods(next, compareSpan, baselineStart, currentStart);
    },
    [compareSpan, baselineStart, currentStart, recomputePeriods],
  );

  const changeCompareSpan = useCallback(
    (id: CompareSpanId) => {
      setCompareSpan(id);
      recomputePeriods(compareMode, id, baselineStart, currentStart);
    },
    [compareMode, baselineStart, currentStart, recomputePeriods],
  );

  const changeBaselineStart = useCallback(
    (value: string) => {
      setBaselineStart(value);
      recomputePeriods(compareMode, compareSpan, value, currentStart);
    },
    [compareMode, compareSpan, currentStart, recomputePeriods],
  );

  const changeCurrentStart = useCallback(
    (value: string) => {
      setCurrentStart(value);
      recomputePeriods(compareMode, compareSpan, baselineStart, value);
    },
    [compareMode, compareSpan, baselineStart, recomputePeriods],
  );

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
          nodeList={
            mode !== "compare" && graph ? (
              <NodeList
                nodes={graph.nodes}
                selectedId={selectedId}
                onSelect={setSelectedId}
              />
            ) : null
          }
          extra={
            mode === "compare" ? (
              <CompareControls
                mode={compareMode}
                onModeChange={changeCompareMode}
                span={compareSpan}
                onSpanChange={changeCompareSpan}
                baselineStart={baselineStart}
                currentStart={currentStart}
                onBaselineStartChange={changeBaselineStart}
                onCurrentStartChange={changeCurrentStart}
                periods={periods}
                includeUnchanged={includeUnchanged}
                onToggleUnchanged={() => setIncludeUnchanged((v) => !v)}
                summary={diff?.summary ?? null}
                threshold={diff?.threshold_percent ?? null}
                // In `recent` mode this re-reads the clock, which is the point of the button.
                // With fixed moments the periods do not move, so it re-runs the same comparison.
                onRefresh={() =>
                  recomputePeriods(compareMode, compareSpan, baselineStart, currentStart)
                }
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
          {(error || (graph?.summary.truncated && graph.summary.truncation_reason) || renderCap) && (
            <div className="banner-stack">
              {error && <ErrorBanner message={error} onRetry={refresh} />}
              {graph?.summary.truncated && graph.summary.truncation_reason && (
                <TruncationBanner reason={graph.summary.truncation_reason} />
              )}
              {renderCap && <RenderBudgetBanner {...renderCap} />}
            </div>
          )}

          {initialLoading && <LoadingState />}
          {!initialLoading && isEmpty && <EmptyState windowLabel={preset} />}
          {hasGraph && !isEmpty && (
            <TopologyCanvas
              graph={graph}
              selectedId={selectedId}
              onSelect={setSelectedId}
              onBudget={setRenderCap}
            />
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
