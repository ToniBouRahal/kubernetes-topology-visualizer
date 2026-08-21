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

// Every surface a text token can land on. --panel-high is the lightest and therefore the binding
// constraint; testing only against --ink would pass tokens that fail in the panels.
const SURFACES = ["--ink", "--panel", "--panel-high"] as const;
const TEXT_TOKENS = ["--text", "--text-dim", "--text-faint"] as const;

describe("WCAG AA text contrast", () => {
  for (const text of TEXT_TOKENS) {
    for (const surface of SURFACES) {
      it(`${text} on ${surface} meets 4.5:1`, () => {
        const ratio = contrast(token(text), token(surface));
        expect(
          ratio,
          `${text} (${token(text)}) on ${surface} (${token(surface)}) = ${ratio.toFixed(2)}:1`,
        ).toBeGreaterThanOrEqual(4.5);
      });
    }
  }

  it("keeps the three text tokens visually distinct, brightest to dimmest", () => {
    // Indexed access, not destructuring: noUncheckedIndexedAccess widens the latter to
    // `number | undefined`, and asserting on a possibly-undefined value is exactly the kind of
    // silent pass this file exists to prevent.
    const luminances = TEXT_TOKENS.map((t) => relativeLuminance(token(t)));
    expect(luminances).toHaveLength(3);
    for (let i = 1; i < luminances.length; i += 1) {
      expect(luminances[i - 1]!).toBeGreaterThan(luminances[i]!);
    }
  });

  it("the focus ring meets the 3:1 non-text contrast requirement", () => {
    // WCAG 2.2 §1.4.11. The ring is --ns-5 and can sit against any surface.
    for (const surface of SURFACES) {
      const ratio = contrast(token("--ns-5"), token(surface));
      expect(ratio, `focus ring on ${surface} = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(3);
    }
  });

  it("regression: --text-faint is not the pre-Phase-4 value that failed AA", () => {
    // Named explicitly because reverting it would break 23 elements at once and the failure is
    // invisible without measurement.
    expect(token("--text-faint")).not.toBe("#6b7f96");
  });
});
