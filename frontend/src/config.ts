/**
 * Runtime configuration the browser reads once at start-up (ADR-012 D-12.2).
 *
 * Served as `/config.json` from a ConfigMap the chart renders, so one image works for any
 * environment. Everything here is optional: a missing file, a 404, a network error or malformed
 * JSON all mean "not configured", and the UI renders exactly as it did before this file existed.
 */
import { useEffect, useState } from "react";

export interface GrafanaConfig {
  /** Origin plus any path prefix, without a trailing slash. */
  url: string;
  /** Dashboard taking `var-namespace`, `var-type`, `var-workload`; empty disables the metrics link. */
  workloadDashboardUid: string;
  /** Loki datasource for Explore; empty disables the logs link. */
  lokiDatasourceUid: string;
}

export interface UiConfig {
  grafana: GrafanaConfig | null;
}

export const NO_CONFIG: UiConfig = { grafana: null };

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/**
 * Turn whatever `/config.json` held into a config, or into "not configured".
 *
 * Only `http(s)` URLs are accepted. The value ends up in an `href`, so anything else — a bare
 * hostname, a `javascript:` scheme — is dropped rather than rendered.
 */
export function parseUiConfig(raw: unknown): UiConfig {
  if (!raw || typeof raw !== "object") return NO_CONFIG;
  const grafana = (raw as { grafana?: unknown }).grafana;
  if (!grafana || typeof grafana !== "object") return NO_CONFIG;

  const g = grafana as Record<string, unknown>;
  const url = asString(g.url).replace(/\/+$/, "");
  if (!/^https?:\/\/[^\s"'<>]+$/i.test(url)) return NO_CONFIG;

  return {
    grafana: {
      url,
      workloadDashboardUid: asString(g.workloadDashboardUid),
      lokiDatasourceUid: asString(g.lokiDatasourceUid),
    },
  };
}

export async function loadUiConfig(fetchImpl: typeof fetch = fetch): Promise<UiConfig> {
  try {
    const response = await fetchImpl("/config.json", { cache: "no-store" });
    if (!response.ok) return NO_CONFIG;
    return parseUiConfig(await response.json());
  } catch {
    return NO_CONFIG;
  }
}

export function useUiConfig(): UiConfig {
  const [config, setConfig] = useState<UiConfig>(NO_CONFIG);
  useEffect(() => {
    let live = true;
    void loadUiConfig().then((loaded) => {
      if (live) setConfig(loaded);
    });
    return () => {
      live = false;
    };
  }, []);
  return config;
}
