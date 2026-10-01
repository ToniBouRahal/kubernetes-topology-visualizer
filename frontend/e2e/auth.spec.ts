import { expect, test } from "@playwright/test";

/**
 * Sign-in (ADR-014 T-14.5, T-14.6), against the real nginx, oauth2-proxy and bundled Dex.
 *
 * Every other spec runs signed in (global-setup.ts). These start from a fresh browser context
 * with no session, which is what someone who can reach the Service but has no account sees.
 */

const BASE = process.env.E2E_BASE_URL ?? "http://localhost:18080";

test.describe("without a session", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("the UI sends you to sign in, and shows nothing of the topology — T-14.5", async ({ page }) => {
    await page.goto(BASE);
    await expect(page).toHaveURL(/\/dex\//);
    await expect(page.locator(".react-flow__node")).toHaveCount(0);
  });

  test("every data path refuses: API 401, config and assets redirect — T-14.5", async ({ page }) => {
    const api = await page.request.get(`${BASE}/api/v1/graph?window=5m`, { maxRedirects: 0 });
    expect(api.status()).toBe(401);
    expect(await api.json()).toMatchObject({ error: "unauthorized" });

    for (const path of ["/", "/config.json", "/index.html", "/health/ready"]) {
      const response = await page.request.get(`${BASE}${path}`, { maxRedirects: 0 });
      expect(response.status(), path).toBe(302);
      expect(response.headers()["location"], path).toContain("/oauth2/start");
    }
  });

  test("a forged identity header does not get you in", async ({ page }) => {
    const api = await page.request.get(`${BASE}/api/v1/graph?window=5m`, {
      maxRedirects: 0,
      headers: { "X-Forwarded-Email": "admin@topology.local", "X-Auth-Request-Email": "admin@topology.local" },
    });
    expect(api.status()).toBe(401);
  });

  test("a wrong password is refused", async ({ page }) => {
    await page.goto(BASE);
    await page.locator('input[name="login"]').fill("admin@topology.local");
    await page.locator('input[name="password"]').fill("not-the-password");
    await page.locator('button[type="submit"]').click();
    await expect(page).toHaveURL(/\/dex\//);
    await expect(page.getByText(/invalid/i)).toBeVisible();
  });
});

test("signed in, the demo topology renders and the details panel loads — T-14.6", async ({ page }) => {
  await page.goto(BASE);
  await expect(page.getByLabel("Observation window")).toContainText(/[1-9]\d* links/, { timeout: 30_000 });
  await page
    .locator(".react-flow__node")
    .filter({ has: page.locator(".topology-node__name", { hasText: /^backend$/ }) })
    .first()
    .click();
  await expect(page.getByLabel(/^Details for/)).toContainText("Outgoing");
});

test("signing out ends the session", async ({ browser }) => {
  // Its own context, so signing out does not end the session the other specs share.
  const context = await browser.newContext({ storageState: "e2e/.auth/session.json" });
  const page = await context.newPage();
  await page.goto(`${BASE}/oauth2/sign_out`);
  const api = await page.request.get(`${BASE}/api/v1/graph?window=5m`, { maxRedirects: 0 });
  expect(api.status()).toBe(401);
  await context.close();
});
