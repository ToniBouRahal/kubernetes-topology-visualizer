/**
 * Accessibility gate — test T-6.9 (ADR-006 D-6.7), Phase 4 P4-F12.
 *
 * `tests/contrast.test.ts` checks the token palette in isolation, which catches a bad token but
 * NOT a component that hardcodes a colour or puts light text on an unexpected surface. This runs
 * against the real rendered page, so it catches both.
 *
 * The Phase 4 pass found 23 failures this way; without a test they would come back unnoticed.
 */
import { expect, test } from "@playwright/test";

/** Computes WCAG contrast for every visible text element on the page. */
const AUDIT = `() => {
  const srgb = (c) => { c /= 255; return c <= 0.03928 ? c/12.92 : Math.pow((c+0.055)/1.055, 2.4); };
  const lum = ([r,g,b]) => 0.2126*srgb(r) + 0.7152*srgb(g) + 0.0722*srgb(b);
  const parse = (s) => { const m = s.match(/rgba?\\(([\\d.]+),\\s*([\\d.]+),\\s*([\\d.]+)(?:,\\s*([\\d.]+))?\\)/);
    return m ? [+m[1],+m[2],+m[3], m[4]===undefined?1:+m[4]] : null; };
  const over = (fg,bg) => [0,1,2].map(i => fg[i]*fg[3] + bg[i]*(1-fg[3]));
  const effBg = (el) => { let acc=null,n=el;
    while (n && n !== document.documentElement.parentNode) {
      const c = parse(getComputedStyle(n).backgroundColor);
      if (c && c[3] > 0) { acc = acc===null ? c : [...over(acc,c),1]; if (c[3]===1) return acc.slice(0,3); }
      n = n.parentElement;
    }
    return acc ? acc.slice(0,3) : [255,255,255]; };
  const ratio = (a,b) => { const [l1,l2] = [lum(a),lum(b)].sort((x,y)=>y-x); return (l1+0.05)/(l2+0.05); };

  const failures = [];
  document.querySelectorAll('*').forEach(el => {
    const text = [...el.childNodes].filter(n => n.nodeType === 3 && n.textContent.trim())
      .map(n => n.textContent.trim()).join(' ');
    if (!text) return;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.display === 'none' || +cs.opacity === 0) return;
    const r = el.getBoundingClientRect();
    if (!r.width || !r.height) return;
    const fg = parse(cs.color);
    if (!fg) return;
    const bg = effBg(el);
    const cr = ratio(fg[3] < 1 ? over(fg,bg) : fg.slice(0,3), bg);
    const px = parseFloat(cs.fontSize);
    const required = (px >= 24 || (px >= 18.66 && +cs.fontWeight >= 700)) ? 3.0 : 4.5;
    if (cr < required) {
      failures.push(text.slice(0,40) + ' [' + (el.className||el.tagName) + '] ' +
        cr.toFixed(2) + ':1 (needs ' + required + ')');
    }
  });
  return failures;
}`;

test.describe("accessibility", () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto("/");
    // Wait for real data, so the audit covers node and edge labels rather than an empty canvas.
    await expect(page.locator(".react-flow__node").first()).toBeVisible({ timeout: 30_000 });
  });

  test("every visible text element meets WCAG AA contrast", async ({ page }) => {
    // Wrapped in parentheses and CALLED: page.evaluate on a string evaluates it as an
    // expression, so a bare `() => {...}` would return the function itself, not its result.
    const failures = await page.evaluate<string[]>(`(${AUDIT})()`);
    expect(failures, `contrast failures:\n${failures.join("\n")}`).toEqual([]);
  });

  test("every interactive element is keyboard reachable and named", async ({ page }) => {
    const result = await page.evaluate(() => {
      const focusable = [
        ...document.querySelectorAll(
          'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])',
        ),
      ].filter((el) => {
        const cs = getComputedStyle(el);
        const r = el.getBoundingClientRect();
        return (
          cs.visibility !== "hidden" &&
          cs.display !== "none" &&
          r.width > 0 &&
          r.height > 0 &&
          !(el as HTMLButtonElement).disabled
        );
      });
      const name = (el: Element) =>
        (
          el.getAttribute("aria-label") ||
          ((el as HTMLInputElement).labels?.[0]?.textContent ?? "") ||
          el.textContent ||
          ""
        )
          .replace(/\s+/g, " ")
          .trim();
      return {
        count: focusable.length,
        unnamed: focusable.filter((el) => !name(el)).map((el) => el.tagName),
        // A positive tabindex overrides document order and is almost always a bug.
        positiveTabindex: focusable.filter((el) => Number(el.getAttribute("tabindex") ?? 0) > 0)
          .length,
      };
    });

    expect(result.count).toBeGreaterThan(10);
    expect(result.unnamed, "controls with no accessible name").toEqual([]);
    expect(result.positiveTabindex, "positive tabindex breaks focus order").toBe(0);
  });

  test("the canvas has a keyboard-navigable equivalent", async ({ page }) => {
    // ADR-006 D-6.7: a React Flow canvas cannot be driven by keyboard alone, so the node list is
    // the accessible path to selecting a node. If it disappears, the graph becomes mouse-only.
    const nodeList = page.getByRole("button", { name: /^backend/ }).first();
    await expect(nodeList).toBeVisible();
    await nodeList.focus();
    await page.keyboard.press("Enter");
    // Targeted by its accessible name rather than a CSS class: the class is an implementation
    // detail, the label is the thing a screen-reader user actually relies on.
    await expect(page.getByRole("complementary", { name: /^Details for / })).toContainText(
      /Incoming|Outgoing/,
    );
  });

  test("focus is always visible", async ({ page }) => {
    const search = page.getByLabel("Search");
    await search.focus();
    const outline = await search.evaluate((el) => {
      const cs = getComputedStyle(el);
      return { width: parseFloat(cs.outlineWidth), style: cs.outlineStyle };
    });
    expect(outline.style).not.toBe("none");
    expect(outline.width).toBeGreaterThanOrEqual(2);
  });
});
