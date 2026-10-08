import { describe, expect, it } from "vitest";
import { disposalLine, fmtBytes } from "./disposal";
import type { RecycleMode } from "./api";

const GB = 1024 ** 3;
const mode = (over: Partial<RecycleMode>): RecycleMode => ({ enabled: true, dirs: [], retention_days: 30, max_gb: 0, ...over });

describe("fmtBytes", () => {
  it("treats zero, negative and missing sizes as 0 MB", () => {
    expect(fmtBytes(0)).toBe("0 MB");
    expect(fmtBytes(-5)).toBe("0 MB");
    expect(fmtBytes(NaN)).toBe("0 MB");
  });

  it("picks MB, then GB (1 dp), then TB (2 dp)", () => {
    expect(fmtBytes(512 * 1024 ** 2)).toBe("512 MB");
    expect(fmtBytes(GB)).toBe("1.0 GB");
    expect(fmtBytes(1.25 * GB)).toBe("1.3 GB");
    expect(fmtBytes(1024 * GB)).toBe("1.00 TB");
    expect(fmtBytes(2.5 * 1024 * GB)).toBe("2.50 TB");
  });
});

describe("disposalLine", () => {
  it("says it is still checking while the mode loads", () => {
    expect(disposalLine(GB, null)).toBe("Deletes 1.0 GB — checking whether the recycle bin is on…");
  });

  it("warns that deletes are permanent when the bin is off", () => {
    expect(disposalLine(GB, mode({ enabled: false }))).toBe("Permanently deletes 1.0 GB — the recycle bin is switched off");
  });

  it("says how long a binned file can come back", () => {
    expect(disposalLine(GB, mode({ retention_days: 1 }))).toMatch(/for 1 day$/);
    expect(disposalLine(GB, mode({ retention_days: 7 }))).toMatch(/for 7 days$/);
    expect(disposalLine(GB, mode({ retention_days: 0 }))).toMatch(/until you empty it$/);
  });

  it("still says where it goes when the size is unknown", () => {
    expect(disposalLine(0, mode({ enabled: false }))).toBe("Permanently deletes it — the recycle bin is switched off");
  });
});
