/**
 * No informational text below 12px — the floor the 2026-09-21 critique set.
 *
 * The in-page detector found eighteen elements of 10px functional text: every namespace band on
 * the canvas, every edge label, and the longest node name. All of it was load-bearing — the
 * namespace a component sits in, the port a link runs over — and all of it was set as a raw
 * pixel value rather than through the type scale, which is exactly why it drifted below the
 * scale's own floor without anyone noticing.
 *
 * The product is delivered on a projector (PRODUCT.md §demo). 10px text there is a decorative
 * mark, not a reading. So the rule is enforced from the stylesheet source, the same way
 * contrast.test.ts enforces the palette: a raw font-size that undercuts --step--1 fails the
 * build instead of shipping.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

// Read from disk, not through an import. Vitest stubs CSS imports, so `?raw` yields an empty
// string and every assertion below would pass against nothing.
const appCss = readFileSync(resolve(__dirname, "../src/styles/app.css"), "utf8");
const tokensCss = readFileSync(resolve(__dirname, "../src/styles/tokens.css"), "utf8");
const canvasSource = readFileSync(
  resolve(__dirname, "../src/features/graph/TopologyCanvas.tsx"),
  "utf8",
);

/** The scale's floor, in px. */
const FLOOR_PX = 12;

describe("type scale floor", () => {
  it("keeps --step--1 at or above the 12px floor", () => {
    const match = tokensCss.match(/^\s*--step--1:\s*([\d.]+)rem;/m);
    expect(match?.[1], "--step--1 not found in tokens.css").toBeDefined();
    // rem, because the scale is expressed in rem and the root size is the browser default.
    expect(parseFloat(match![1]!) * 16).toBeGreaterThanOrEqual(FLOOR_PX);
  });

  it("app.css sets no font-size below the floor", () => {
    // Every raw px font-size in the stylesheet, with its line number, so a failure names the
    // offending rule rather than just asserting that one exists.
    const offenders: string[] = [];
    appCss.split("\n").forEach((line, index) => {
      const match = line.match(/font-size:\s*(\d+(?:\.\d+)?)px/);
      if (match && parseFloat(match[1]!) < FLOOR_PX) {
        offenders.push(`app.css:${index + 1} — ${line.trim()}`);
      }
    });
    expect(offenders, `font-size below ${FLOOR_PX}px:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("app.css sets no font-size below the floor in rem either", () => {
    const offenders: string[] = [];
    appCss.split("\n").forEach((line, index) => {
      const match = line.match(/font-size:\s*([\d.]+)rem/);
      if (match && parseFloat(match[1]!) * 16 < FLOOR_PX) {
        offenders.push(`app.css:${index + 1} — ${line.trim()}`);
      }
    });
    expect(offenders, `font-size below ${FLOOR_PX}px:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("the canvas sets no inline font size below the floor", () => {
    // React Flow writes a labelStyle into an SVG presentation attribute, where var() does not
    // resolve, so any size there is a literal the CSS rules above cannot see. Edges carry no text
    // now; this keeps the floor if a label ever returns.
    const sizes = [...canvasSource.matchAll(/fontSize:\s*(\d+)/g)].map((m) => parseInt(m[1]!, 10));
    expect(sizes.filter((px) => px < FLOOR_PX)).toEqual([]);
  });
});
