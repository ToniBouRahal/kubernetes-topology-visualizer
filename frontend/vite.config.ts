import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    // The backend is reached by port-forward during development, so requests are proxied
    // rather than pointed at an absolute origin. That keeps CORS_ALLOWED_ORIGINS meaningful
    // in production instead of being widened just to make local development work.
    proxy: {
      "/api": { target: "http://localhost:8000", changeOrigin: true },
      "/health": { target: "http://localhost:8000", changeOrigin: true },
    },
  },
  test: {
    // e2e/ belongs to Playwright, not vitest: it needs a running cluster and a browser, so it
    // is part of the phase gate rather than the per-commit suite (ADR-008 D-8.1).
    exclude: ["node_modules/**", "dist/**", "e2e/**"],
    environment: "jsdom",
    setupFiles: ["./tests/setup.ts"],
    globals: true,
  },
});
