import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { bootUnreachable } from "./me";
import { retryDelay } from "../components/Unreachable";

const ok = (value: unknown): PromiseSettledResult<unknown> => ({ status: "fulfilled", value });
const fail = (reason: unknown): PromiseSettledResult<unknown> => ({ status: "rejected", reason });

describe("bootUnreachable", () => {
  it("treats a 401 or 403 from /me as signed out, not unreachable", () => {
    expect(bootUnreachable(fail(new ApiError("authentication required", 401)), ok({}))).toBe(false);
    expect(bootUnreachable(fail(new ApiError("insufficient permissions", 403)), ok({}))).toBe(false);
  });

  it("treats a network error, 5xx or proxy 502 as unreachable", () => {
    expect(bootUnreachable(fail(new TypeError("Failed to fetch")), fail(new TypeError("Failed to fetch")))).toBe(true);
    expect(bootUnreachable(fail(new ApiError("HTTP 502", 502)), ok({}))).toBe(true);
    expect(bootUnreachable(fail(new SyntaxError("Unexpected token <")), ok({}))).toBe(true);
  });

  it("treats a failed /status as unreachable even when /me answered", () => {
    expect(bootUnreachable(ok({ id: 1 }), fail(new ApiError("HTTP 500", 500)))).toBe(true);
    expect(bootUnreachable(fail(new ApiError("x", 401)), fail(new TypeError("Failed to fetch")))).toBe(true);
  });

  it("boots when both answered", () => {
    expect(bootUnreachable(ok({ id: 1 }), ok({}))).toBe(false);
  });
});

describe("retryDelay", () => {
  it("backs off 5s, 10s, 20s, then holds at 30s", () => {
    expect([0, 1, 2, 3, 4, 50].map(retryDelay)).toEqual([5000, 10000, 20000, 30000, 30000, 30000]);
  });
});
