/**
 * WCAG AA contrast, asserted against the real token file — test T-6.9 (ADR-006 D-6.7).
 *
 * The Phase 4 accessibility pass found 23 text elements below AA, all tracing to a single token
 * (`--text-faint` at 4.41 / 3.99 / 3.58 against the three surfaces). A one-off fix in the browser
 * would not stay fixed: the next person to nudge a colour has no way to know which combinations
 * are load-bearing.
 *
 * So the ratios are computed here from `tokens.css` itself. Editing a colour below AA fails the
 * build rather than quietly shipping unreadable text.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

// Read from disk rather than importing the stylesheet. Vitest stubs CSS imports by default, so
// `import "...tokens.css?raw"` silently yields an EMPTY STRING — every assertion would then throw
// "token not found" instead of checking a colour, and a test that cannot see its input is worse
// than no test.
const tokensCss = readFileSync(resolve(__dirname, "../src/styles/tokens.css"), "utf8");

/** Read a custom property's value out of the stylesheet source. */
function token(name: string): string {
  // Only the `:root` declaration, not the commented-out reference table above it.
  const match = tokensCss.match(new RegExp(`^\\s*${name}:\\s*(#[0-9a-fA-F]{6});`, "m"));
  const value = match?.[1];
  if (!value) throw new Error(`token ${name} not found in tokens.css`);
  return value;
}

function channel(hex: string, offset: number): number {
  const value = parseInt(hex.replace("#", "").slice(offset, offset + 2), 16);
  const s = value / 255;
  return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

function relativeLuminance(hex: string): number {
  return 0.2126 * channel(hex, 0) + 0.7152 * channel(hex, 2) + 0.0722 * channel(hex, 4);
}

function contrast(a: string, b: string): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const hi = Math.max(la, lb);
  const lo = Math.min(la, lb);
  return (hi + 0.05) / (lo + 0.05);
}

// Every surface a text token can land on, the canvas included: React Flow's attribution and the
// canvas toolbar both set text directly on --ink, so exempting it would exempt real text.
const SURFACES = ["--ink", "--panel", "--panel-high"] as const;
const TEXT_SURFACES = SURFACES;
const TEXT_TOKENS = ["--text", "--text-dim", "--text-faint"] as const;
const NAMESPACE_TOKENS = ["--ns-1", "--ns-2", "--ns-3", "--ns-4", "--ns-5", "--ns-6", "--external"] as const;

describe("WCAG AA text contrast", () => {
  for (const text of TEXT_TOKENS) {
    for (const surface of TEXT_SURFACES) {
      it(`${text} on ${surface} meets 4.5:1`, () => {
        const ratio = contrast(token(text), token(surface));
        expect(
          ratio,
          `${text} (${token(text)}) on ${surface} (${token(surface)}) = ${ratio.toFixed(2)}:1`,
        ).toBeGreaterThanOrEqual(4.5);
      });
    }
  }

  it("keeps the three text tokens in a hierarchy, strongest to faintest — T-10.8", () => {
    // Stated as contrast against the ground rather than as raw luminance, because the palette is
    // light (ADR-010 D-10.7): --text is now the DARKEST of the three, so an assertion on
    // luminance ordering would have silently inverted with the redesign. Contrast is what the
    // hierarchy actually means, and it reads the same on either ground.
    //
    // Indexed access, not destructuring: noUncheckedIndexedAccess widens the latter to
    // `number | undefined`, and asserting on a possibly-undefined value is exactly the kind of
    // silent pass this file exists to prevent.
    const ratios = TEXT_TOKENS.map((t) => contrast(token(t), token("--panel")));
    expect(ratios).toHaveLength(3);
    for (let i = 1; i < ratios.length; i += 1) {
      expect(ratios[i - 1]!).toBeGreaterThan(ratios[i]!);
    }
  });

  it("every namespace fill carries its node's name at 4.5:1 — T-10.8", () => {
    // A namespace hue is no longer only a tint: since ADR-010 D-10.2 it is a FILL with the
    // component's name set on it in --on-fill. A hue that is merely distinguishable from its
    // neighbours can still be unreadable under the label it now carries.
    for (const ns of NAMESPACE_TOKENS) {
      const ratio = contrast(token(ns), token("--on-fill"));
      expect(
        ratio,
        `${ns} (${token(ns)}) under --on-fill (${token("--on-fill")}) = ${ratio.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("keeps the namespace hues distinguishable from one another", () => {
    // Six hues that all pass the label test above could still be six shades of one colour.
    const luminances = NAMESPACE_TOKENS.map((t) => token(t));
    expect(new Set(luminances).size).toBe(NAMESPACE_TOKENS.length);
  });

  it("the focus ring meets the 3:1 non-text contrast requirement", () => {
    // WCAG 2.2 §1.4.11. The ring owns its own token now, so it can be solved for this one job.
    for (const surface of SURFACES) {
      const ratio = contrast(token("--focus"), token(surface));
      expect(ratio, `focus ring on ${surface} = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(3);
    }
  });

  it("edges meet the 3:1 graphical-object requirement on the canvas", () => {
    // An edge is the load-bearing mark in a dependency graph and is drawn on --ink, which no
    // text-contrast assertion above covers.
    for (const stroke of ["--edge", "--edge-strong"] as const) {
      const ratio = contrast(token(stroke), token("--ink"));
      expect(ratio, `${stroke} on --ink = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(3);
    }
  });

  it("regression: the palette is the light one ADR-010 specifies, not the console it replaced", () => {
    // Named explicitly because a revert would restore dark tokens that pass every ratio above
    // while making --on-fill labels, the blueprint grid, and the edge strokes wrong at once.
    expect(relativeLuminance(token("--ink"))).toBeGreaterThan(0.5);
    expect(relativeLuminance(token("--text"))).toBeLessThan(0.1);
  });
});
