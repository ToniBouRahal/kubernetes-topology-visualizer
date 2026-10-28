import { expect, test } from "@playwright/test";

/**
 * The bundled Keycloak (ADR-017): nginx's allowlist (T-17.8) and the provider's own PKCE rule
 * (part of T-17.9), against the real nginx and Keycloak. Skipped on a release that bundles Dex or
 * an external provider instead.
 *
 * Lockout (the other half of T-17.9) is not run here: six wrong passwords would lock the demo user
 * every other spec signs in as. It is checked by hand on kind and recorded in the ADR.
 */

const BASE = process.env.E2E_BASE_URL ?? "http://localhost:18080";
const REALM = `${BASE}/keycloak/realms/topology`;

test.use({ storageState: { cookies: [], origins: [] } });

test.beforeAll(async ({ request }) => {
  const discovery = await request.get(`${REALM}/.well-known/openid-configuration`, { maxRedirects: 0 });
  test.skip(discovery.status() !== 200, "this release does not bundle Keycloak");
});

test("only the realm's sign-in endpoints are reachable — T-17.8", async ({ request }) => {
  for (const path of [
    "/keycloak/admin/",
    "/keycloak/admin/master/console/",
    "/keycloak/admin/realms/topology/users",
    "/keycloak/realms/master/.well-known/openid-configuration",
    "/keycloak/realms/master/protocol/openid-connect/auth",
    "/keycloak/realms/topology/account",
    "/keycloak/realms/topology/account/",
    "/keycloak/realms/topology/clients-registrations/openid-connect",
    "/keycloak/realms/topology/protocol/openid-connect/auth/device",
    "/keycloak/realms/topology/protocol/openid-connect/ext/ciba/auth",
    "/keycloak/realms/topology/protocol/openid-connect/token/introspect",
    "/keycloak/realms/topology/protocol/saml",
    "/keycloak/",
  ]) {
    const response = await request.get(`${BASE}${path}`, { maxRedirects: 0 });
    expect(response.status(), path).toBe(404);
  }
  const discovery = await request.get(`${REALM}/.well-known/openid-configuration`);
  expect(await discovery.json()).toMatchObject({ issuer: expect.stringMatching(/\/keycloak\/realms\/topology$/) });
});

test("an authorisation request without a PKCE challenge is refused — T-17.9", async ({ request }) => {
  for (const client of ["topology", "topology-cli"]) {
    const redirect = client === "topology" ? `${BASE}/oauth2/callback` : "http://127.0.0.1:38085/callback";
    const params = new URLSearchParams({ client_id: client, redirect_uri: redirect, response_type: "code", scope: "openid", state: "s" });
    const response = await request.get(`${REALM}/protocol/openid-connect/auth?${params}`, { maxRedirects: 0 });
    // Keycloak answers by redirecting the error to the client, never with a login page.
    expect(response.status(), client).toBe(302);
    expect(response.headers()["location"], client).toContain("error=invalid_request");
  }
});

test("the sign-in page is Keycloak's, under the Keycloak CSP", async ({ page }) => {
  const response = await page.goto(BASE);
  expect(page.url()).toContain("/keycloak/realms/topology/protocol/openid-connect/auth");
  const csp = response?.headers()["content-security-policy"] ?? "";
  expect(csp).toContain("form-action 'self' http://127.0.0.1:38085");
  await expect(page.locator('input[name="username"]')).toBeVisible();
});
