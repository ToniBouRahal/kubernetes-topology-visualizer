/**
 * Runtime config — ADR-012 T-12.5.
 *
 * Every way /config.json can be wrong must land on "not configured", because the alternative is
 * a details panel with a button that goes nowhere. A dev build has no config.json at all.
 */
import { describe, expect, it } from "vitest";
import { NO_CONFIG, loadUiConfig, parseUiConfig } from "../src/config";

function fakeFetch(body: string | null, status = 200): typeof fetch {
  return (async () =>
    new Response(body, { status, headers: { "Content-Type": "application/json" } })) as typeof fetch;
}

describe("parseUiConfig", () => {
  it("accepts an http(s) URL and trims a trailing slash", () => {
    expect(parseUiConfig({ grafana: { url: "https://g.example.com/grafana/", lokiDatasourceUid: "l" } })).toEqual({
      grafana: { url: "https://g.example.com/grafana", workloadDashboardUid: "", lokiDatasourceUid: "l" },
    });
  });

  it("treats an empty URL as not configured", () => {
    expect(parseUiConfig({ grafana: { url: "" } })).toBe(NO_CONFIG);
  });

  it("refuses anything that is not an http(s) URL, because it lands in an href", () => {
    expect(parseUiConfig({ grafana: { url: "grafana.example.com" } })).toBe(NO_CONFIG);
    expect(parseUiConfig({ grafana: { url: "javascript:alert(1)" } })).toBe(NO_CONFIG);
  });

  it("tolerates a shape that is not what was expected", () => {
    expect(parseUiConfig(null)).toBe(NO_CONFIG);
    expect(parseUiConfig("nope")).toBe(NO_CONFIG);
    expect(parseUiConfig({ grafana: 42 })).toBe(NO_CONFIG);
    expect(parseUiConfig({ grafana: { url: 42 } })).toBe(NO_CONFIG);
  });
});

describe("loadUiConfig", () => {
  it("parses a good file", async () => {
    const cfg = await loadUiConfig(fakeFetch(JSON.stringify({ grafana: { url: "https://g" } })));
    expect(cfg.grafana?.url).toBe("https://g");
  });

  it("is not configured on a 404 — the dev server has no config.json", async () => {
    expect(await loadUiConfig(fakeFetch("not found", 404))).toBe(NO_CONFIG);
  });

  it("is not configured on malformed JSON", async () => {
    expect(await loadUiConfig(fakeFetch("{oops"))).toBe(NO_CONFIG);
  });

  it("is not configured when the fetch itself fails", async () => {
    const failing = (async () => {
      throw new TypeError("network down");
    }) as typeof fetch;
    expect(await loadUiConfig(failing)).toBe(NO_CONFIG);
  });
});
