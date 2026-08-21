import { defineConfig, devices } from "@playwright/test";

/**
 * Cluster E2E configuration.
 *
 * Deliberately NOT part of `npm test`: these need a running cluster and a port-forward, so they
 * belong to the phase gate rather than the per-commit path (ADR-008 D-8.1).
 */
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: [["list"]],
  timeout: 60_000,
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:18080",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  // No `channel`: use Playwright's bundled browser. Specifying channel:"chromium" demands the
  // full branded build, while CI and this machine install only the headless shell.
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], channel: undefined } }],
});
