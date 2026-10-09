import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, resetSignedOut, SIGNED_OUT_EVENT } from "./api";

// req() tells the app once when the session is lost, and never for the auth endpoints,
// which legitimately answer 401 (a wrong password, a pending Plex PIN, a signed-out /me).
describe("signed-out signal", () => {
  let events: number;
  let status: number;

  beforeEach(() => {
    events = 0;
    status = 401;
    const win = new EventTarget();
    win.addEventListener(SIGNED_OUT_EVENT, () => { events++; });
    vi.stubGlobal("window", win);
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ message: "authentication required" }), { status })));
    resetSignedOut();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fires exactly once for concurrent 401s", async () => {
    const results = await Promise.allSettled([api.movies(), api.movies()]);
    expect(results.every((r) => r.status === "rejected")).toBe(true);
    expect(events).toBe(1);
  });

  it("never fires for the auth endpoints", async () => {
    await expect(api.login("kid@example.com", "wrong")).rejects.toBeInstanceOf(ApiError);
    await expect(api.me()).rejects.toBeTruthy();
    expect(events).toBe(0);
  });

  it("throws ApiError carrying the status, path and server message", async () => {
    const err = await api.movies().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
    expect((err as ApiError).path).toBe("/api/v1/movies");
    expect((err as ApiError).message).toBe("authentication required");
  });

  it("doesn't fire for a 403 (a role refusal, not a lost session)", async () => {
    status = 403;
    await api.movies().catch(() => {});
    expect(events).toBe(0);
  });

  it("covers the raw uploads too", async () => {
    await api.uploadBookCover(1, new File(["x"], "c.jpg")).catch(() => {});
    expect(events).toBe(1);
  });
});
