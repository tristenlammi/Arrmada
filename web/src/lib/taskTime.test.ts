import { describe, expect, it } from "vitest";
import { ago, every, toMs, took, until } from "./taskTime";

const now = Date.parse("2026-10-09T12:00:00Z");

describe("toMs", () => {
  it("reads RFC 3339, unix seconds and milliseconds", () => {
    expect(toMs("2026-10-09T12:00:00Z")).toBe(now);
    expect(toMs(now / 1000)).toBe(now);
    expect(toMs(now)).toBe(now);
  });

  it("treats null, empty and Go's zero time as unset", () => {
    expect(toMs(null)).toBeNull();
    expect(toMs("")).toBeNull();
    expect(toMs(0)).toBeNull();
    expect(toMs("0001-01-01T00:00:00Z")).toBeNull();
    expect(toMs("not a time")).toBeNull();
  });
});

describe("wording", () => {
  it("says how long ago", () => {
    expect(ago("2026-10-09T11:59:40Z", now)).toBe("20s ago");
    expect(ago("2026-10-09T11:56:00Z", now)).toBe("4 min ago");
    expect(ago(null, now)).toBe("never");
  });

  it("says how long until, and now when overdue", () => {
    expect(until("2026-10-09T12:04:00Z", now)).toBe("in 4 min");
    expect(until("2026-10-09T11:00:00Z", now)).toBe("now");
    expect(until(null, now)).toBe("—");
  });

  it("names intervals and durations", () => {
    expect(every(10)).toBe("10 s");
    expect(every(900)).toBe("15 min");
    expect(every(6 * 3600)).toBe("6 h");
    expect(every(86400)).toBe("1 day");
    expect(took(120)).toBe("120 ms");
    expect(took(4200)).toBe("4.2 s");
  });
});
