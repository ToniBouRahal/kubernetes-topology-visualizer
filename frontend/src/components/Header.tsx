export type Mode = "live" | "history";

/**
 * Header: what you are looking at, and whether it is still moving.
 *
 * A paused or historical view that looks identical to a live one is a demonstration hazard, so
 * the mode is stated in words and the connection dot never carries the meaning alone.
 */
export function Header({
  mode,
  onModeChange,
  paused,
  onTogglePause,
  onRefresh,
  refreshing,
  connected,
  windowPreset,
  onWindowChange,
  presets,
}: {
  mode: Mode;
  onModeChange: (mode: Mode) => void;
  paused: boolean;
  onTogglePause: () => void;
  onRefresh: () => void;
  refreshing: boolean;
  connected: boolean;
  windowPreset: string;
  onWindowChange: (preset: string) => void;
  presets: readonly string[];
}) {
  return (
    <header className="header">
      <div className="header__brand">
        <span className="header__mark" aria-hidden="true" />
        <h1 className="header__title">Runtime Topology</h1>
        <span className="header__sub">observed, not declared</span>
      </div>

      <div className="header__group" role="group" aria-label="View mode">
        {(["live", "history"] as const).map((value) => (
          <button
            key={value}
            type="button"
            className={`seg ${mode === value ? "seg--on" : ""}`}
            aria-pressed={mode === value}
            onClick={() => onModeChange(value)}
          >
            {value === "live" ? "Live" : "History"}
          </button>
        ))}
      </div>

      <label className="header__window">
        <span className="visually-hidden">Observation window</span>
        <select value={windowPreset} onChange={(e) => onWindowChange(e.target.value)}>
          {presets.map((p) => (
            <option key={p} value={p}>
              last {p}
            </option>
          ))}
        </select>
      </label>

      <div className="header__spacer" />

      <button type="button" onClick={onRefresh} disabled={refreshing}>
        {refreshing ? "Refreshing…" : "Refresh"}
      </button>

      {mode === "live" && (
        <button type="button" onClick={onTogglePause} aria-pressed={paused}>
          {paused ? "Resume" : "Pause"}
        </button>
      )}

      <span className={`status ${connected ? "status--ok" : "status--down"}`}>
        <span className="status__dot" aria-hidden="true" />
        {/* The word, not just the dot. */}
        {connected ? "Connected" : "No data"}
      </span>
    </header>
  );
}
