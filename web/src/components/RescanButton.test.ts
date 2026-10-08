import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ago, scanTitle } from "./RescanButton";

const NOW = 1_800_000_000; // unix seconds

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW * 1000);
});
afterEach(() => {
  vi.useRealTimers();
});

describe("ago", () => {
  it("rounds down into just now / min / h / d buckets", () => {
    expect(ago(NOW)).toBe("just now");
    expect(ago(NOW - 59)).toBe("just now");
    expect(ago(NOW - 60)).toBe("1 min ago");
    expect(ago(NOW - 3599)).toBe("59 min ago");
    expect(ago(NOW - 3600)).toBe("1 h ago");
    expect(ago(NOW - 86399)).toBe("23 h ago");
    expect(ago(NOW - 86400 * 3)).toBe("3 d ago");
  });

  it("treats a timestamp in the future (clock skew) as just now", () => {
    expect(ago(NOW + 500)).toBe("just now");
  });
});

describe("scanTitle", () => {
  it("covers no scan, a running scan, a never-run scan and a finished one", () => {
    expect(scanTitle(null)).toBe("Rescan the library");
    expect(scanTitle({ scanned_at: NOW - 120, scanning: true })).toBe("Scanning the library…");
    expect(scanTitle({ scanned_at: 0, scanning: false })).toBe("Rescan the library");
    expect(scanTitle({ scanned_at: NOW - 120, scanning: false })).toBe("Rescan the library · last scanned 2 min ago");
  });
});
