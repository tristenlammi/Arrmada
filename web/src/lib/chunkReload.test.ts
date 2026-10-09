import { describe, expect, it } from "vitest";
import { claimChunkReload, isChunkLoadError } from "./chunkReload";

function memStore(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init));
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    map: m,
  };
}

describe("isChunkLoadError", () => {
  it("matches each browser's failed dynamic import message", () => {
    expect(isChunkLoadError(new TypeError("Failed to fetch dynamically imported module: https://x/assets/a.js"))).toBe(true);
    expect(isChunkLoadError(new TypeError("Importing a module script failed."))).toBe(true);
    expect(isChunkLoadError(new TypeError("error loading dynamically imported module: /assets/a.js"))).toBe(true);
    const e = new Error("Loading chunk 7 failed");
    e.name = "ChunkLoadError";
    expect(isChunkLoadError(e)).toBe(true);
  });

  it("ignores ordinary render errors", () => {
    expect(isChunkLoadError(new TypeError("Cannot read properties of null (reading 'title')"))).toBe(false);
    expect(isChunkLoadError(null)).toBe(false);
    expect(isChunkLoadError(undefined)).toBe(false);
  });
});

describe("claimChunkReload", () => {
  it("allows the first reload and records it", () => {
    const s = memStore();
    expect(claimChunkReload(s, 100_000)).toBe(true);
    expect(s.map.get("arrmada.chunkReload")).toBe("100000");
  });

  it("refuses a second reload within 10s, so a missing chunk can't loop", () => {
    const s = memStore();
    expect(claimChunkReload(s, 100_000)).toBe(true);
    expect(claimChunkReload(s, 109_999)).toBe(false);
  });

  it("allows another reload once the 10s window has passed", () => {
    const s = memStore({ "arrmada.chunkReload": "100000" });
    expect(claimChunkReload(s, 110_000)).toBe(true);
    expect(s.map.get("arrmada.chunkReload")).toBe("110000");
  });

  it("refuses when storage is missing or throws", () => {
    expect(claimChunkReload(null, 1)).toBe(false);
    const broken = {
      getItem: () => {
        throw new Error("SecurityError");
      },
      setItem: () => {},
    };
    expect(claimChunkReload(broken, 1)).toBe(false);
  });
});
