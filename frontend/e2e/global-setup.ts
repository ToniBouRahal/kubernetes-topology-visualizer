import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { chromium, type FullConfig } from "@playwright/test";

/**
 * Sign in once, before any test, and keep the session for all of them (ADR-014 D-14.6).
 *
 * Every page and API call is behind sign-in, so without this every spec would start on the login
 * page. The bundled Dex's or Keycloak's demo user is used; its password is read from the cluster Secret unless
 * E2E_PASSWORD is set, so nothing is committed. A deployment with sign-in off simply never
 * redirects, and the empty session saved here changes nothing.
 */
export const STORAGE_STATE = "e2e/.auth/session.json";

export default async function globalSetup(config: FullConfig) {
  const base = config.projects[0]?.use.baseURL ?? process.env.E2E_BASE_URL ?? "http://localhost:18080";
  mkdirSync(dirname(STORAGE_STATE), { recursive: true });

  const browser = await chromium.launch();
  const page = await browser.newPage();
  await page.goto(base);

  // The bundled Dex (ADR-014 D-14.7) or Keycloak (ADR-017, T-17.7): the same demo user and
  // password, different field names.
  const provider = page.url().includes("/dex/") ? "dex" : page.url().includes("/keycloak/") ? "keycloak" : null;
  if (provider) {
    const user = provider === "dex" ? 'input[name="login"]' : 'input[name="username"]';
    await page.locator(user).fill(process.env.E2E_USER ?? "admin@topology.local");
    await page.locator('input[name="password"]').fill(process.env.E2E_PASSWORD ?? demoPassword());
    await page.locator(provider === "dex" ? 'button[type="submit"]' : "#kc-login").click();
    await page.waitForURL((url) => !url.pathname.startsWith(`/${provider}/`) && !url.pathname.startsWith("/oauth2/"), {
      timeout: 30_000,
    });
  }

  await page.context().storageState({ path: STORAGE_STATE });
  await browser.close();
}

function demoPassword(): string {
  const context = process.env.KIND_CONTEXT ?? "kind-topology";
  const namespace = process.env.NAMESPACE ?? "topology";
  const secret = `${process.env.RELEASE ?? "topology"}-visualizer-auth`;
  const encoded = execFileSync(
    "kubectl",
    ["--context", context, "-n", namespace, "get", "secret", secret, "-o", "jsonpath={.data.demo-password}"],
    { encoding: "utf8" },
  );
  return Buffer.from(encoded, "base64").toString("utf8");
}
