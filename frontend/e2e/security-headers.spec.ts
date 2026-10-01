import { expect, test, type APIResponse } from "@playwright/test";

/**
 * T-14.9 — browser security headers (ADR-014 D-14.10), on the real nginx.
 *
 * Checked on three kinds of response because nginx drops the server-level add_header set in any
 * location that declares its own: the app shell, a hashed asset and a proxied API response each
 * come from a different location block, and a header present on one says nothing about another.
 */

const BASE = process.env.E2E_BASE_URL ?? "http://localhost:18080";

function expectHardened(response: APIResponse, what: string) {
  const h = response.headers();
  const csp = h["content-security-policy"] ?? "";
  expect(csp, `${what}: CSP`).toContain("default-src 'self'");
  expect(csp, `${what}: no inline or eval script`).toMatch(/script-src 'self'(;|$)/);
  expect(csp, `${what}: not frameable`).toContain("frame-ancestors 'none'");
  expect(h["x-content-type-options"], `${what}: nosniff`).toBe("nosniff");
  expect(h["x-frame-options"], `${what}: X-Frame-Options`).toBe("DENY");
  expect(h["referrer-policy"], `${what}: Referrer-Policy`).toBe("no-referrer");
  expect(h["permissions-policy"], `${what}: Permissions-Policy`).toContain("camera=()");
  // No nginx version on the wire.
  expect(h["server"] ?? "", `${what}: Server header`).not.toMatch(/\d/);
}

test("the app shell, its assets and the API all carry the security headers — T-14.9", async ({ page }) => {
  // page.request shares the signed-in session; the bare `request` fixture does not.
  const request = page.request;
  const shell = await page.goto(BASE);
  expect(shell).not.toBeNull();
  expectHardened(await request.get(shell!.url()), "index.html");

  const asset = await page.locator('script[type="module"]').getAttribute("src");
  expect(asset, "the shell loads a hashed script").toMatch(/^\/assets\//);
  expectHardened(await request.get(new URL(asset!, BASE).toString()), "asset");

  expectHardened(await request.get(`${BASE}/api/v1/namespaces?window=5m`), "API");
});

test("the page runs under its CSP without a single violation", async ({ page }) => {
  const violations: string[] = [];
  page.on("console", (message) => {
    if (/Content Security Policy|Content-Security-Policy/i.test(message.text())) violations.push(message.text());
  });
  await page.goto(BASE);
  await expect(page.locator(".react-flow__node").first()).toBeVisible({ timeout: 30_000 });
  expect(violations).toEqual([]);
});
