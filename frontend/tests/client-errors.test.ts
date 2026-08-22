/**
 * Failure messages a user can act on — ADR-001 §7 (task P5-T17).
 *
 * The backend always returns a `detail`, so its own errors are already explained. What is NOT
 * explained is a failure that never reached it: while the backend is restarting, the frontend's
 * nginx returns a 502 whose status text is "Bad Gateway". That is accurate and useless — it was
 * what the UI actually displayed during an outage test.
 */
import { describe, expect, it, vi, afterEach } from "vitest";

import { ApiError, fetchGraph } from "../src/api/client";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function respondWith(status: number, body?: unknown, statusText = "Bad Gateway") {
  vi.stubGlobal(
    "fetch",
    vi.fn(() =>
      Promise.resolve(
        body === undefined
          ? new Response("<html>502 Bad Gateway</html>", { status, statusText })
          : new Response(JSON.stringify(body), { status, statusText }),
      ),
    ),
  );
}

describe("API error messages", () => {
  it("explains a proxy 502 in terms of what is actually wrong", async () => {
    respondWith(502);
    await expect(fetchGraph({ window: "5m" })).rejects.toSatisfy((err: unknown) => {
      expect(err).toBeInstanceOf(ApiError);
      const detail = (err as ApiError).detail;
      expect(detail).toMatch(/backend is not responding/i);
      // The point of the change: the raw status text alone is not what a user sees.
      expect(detail).not.toBe("Bad Gateway");
      return true;
    });
  });

  it.each([503, 504])("explains %d the same way", async (status) => {
    respondWith(status, undefined, "Service Unavailable");
    await expect(fetchGraph({ window: "5m" })).rejects.toThrow(/backend is not responding/i);
  });

  it("still prefers the backend's own detail when there is one", async () => {
    // The backend explains itself better than any generic mapping could, so its message wins.
    respondWith(400, { detail: "window must be one of 1m, 5m, 15m", request_id: "req-7" }, "Bad Request");
    await expect(fetchGraph({ window: "5m" })).rejects.toSatisfy((err: unknown) => {
      const e = err as ApiError;
      expect(e.detail).toBe("window must be one of 1m, 5m, 15m");
      expect(e.requestId).toBe("req-7");
      return true;
    });
  });

  it("never surfaces a raw status number with no explanation", async () => {
    respondWith(418, undefined, "I'm a teapot");
    await expect(fetchGraph({ window: "5m" })).rejects.toThrow(/failed with status 418/);
  });
});
