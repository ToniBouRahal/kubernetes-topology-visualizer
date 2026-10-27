/**
 * Diagram export — ADR-016 T-16.7.
 *
 * The Go CLI writes the same Mermaid text, and both are held to ONE golden file under
 * cli/testdata. Read from disk rather than copied here: a copy would let the two drift apart
 * while each still passed its own test.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import type { GraphResponse } from "../src/api/types";
import { escapeMermaid, exportFileName, toMermaid } from "../src/features/export/mermaid";

const TESTDATA = resolve(__dirname, "../../cli/testdata");
const fixture = JSON.parse(readFileSync(resolve(TESTDATA, "graph.export.json"), "utf8")) as GraphResponse;
const golden = readFileSync(resolve(TESTDATA, "graph.export.mmd"), "utf8");

describe("toMermaid", () => {
  it("matches the golden file the CLI is also held to, byte for byte", () => {
    expect(toMermaid(fixture)).toBe(golden);
  });

  it("cannot be steered by a hostile label", () => {
    const hostile = 'x"]\nclick n1 "javascript:alert(1)" %% <b>|`&#';
    const out = toMermaid({
      ...fixture,
      nodes: fixture.nodes.map((n) => ({ ...n, label: hostile })),
    });
    // Every label sits in one quoted string on one line: no quote, newline or pipe survives.
    for (const line of out.split("\n").filter((l) => l.includes("click"))) {
      expect(line).not.toMatch(/x"\]/);
      expect(line.match(/"/g)?.length ?? 0).toBe(2);
    }
    expect(out).not.toContain("<b>");
    expect(out.split("\n").some((l) => l.startsWith("click"))).toBe(false);
  });

  it("escapes in one pass, so an entity it writes is never escaped again", () => {
    expect(escapeMermaid('#"')).toBe("#35;#quot;");
  });

  it("states a server-side truncation in the header", () => {
    const out = toMermaid({ ...fixture, summary: { ...fixture.summary, truncated: true } });
    expect(out.split("\n")[2]).toBe("%% Truncated by the server; this is not the whole graph.");
  });

  it("names the download after the window, with no colons", () => {
    expect(exportFileName(fixture)).toBe("topology-2026-08-10T110000Z.mmd");
  });
});
