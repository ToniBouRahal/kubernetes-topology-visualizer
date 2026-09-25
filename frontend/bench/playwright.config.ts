import { defineConfig, devices } from "@playwright/test";

/**
 * Canvas scale benchmark (P5-F18). Not part of `npm test` or the cluster E2E suite: it builds a
 * production bundle and measures it against a mocked API. See bench/README.md.
 */
export default defineConfig({
  testDir: ".",
  testMatch: "*.bench.ts",
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: "http://localhost:5174",
    // The demo target resolution (ADR-006).
    viewport: { width: 1280, height: 720 },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 720 }, channel: undefined } }],
  webServer: {
    // A production build: development React is several times slower and would measure the
    // wrong thing.
    command: "npx vite build --outDir bench/dist --emptyOutDir && npx vite preview --outDir bench/dist --port 5174 --strictPort",
    cwd: "..",
    url: "http://localhost:5174",
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
