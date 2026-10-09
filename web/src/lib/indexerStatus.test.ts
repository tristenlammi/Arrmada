import { describe, expect, it } from "vitest";
import type { IndexerStatus } from "./api";
import { INDEXER_DOT, hhmm, indexerStatusLine, prowlarrSyncMessage } from "./indexerStatus";

const now = Date.parse("2026-10-09T12:00:00Z");
const at = (minutes: number) => new Date(now + minutes * 60_000).toISOString();
const base: IndexerStatus = { state: "ok", consecutive_failures: 0, queries_24h: 0, failures_24h: 0 };

describe("indexerStatusLine", () => {
  it("says when a working indexer last answered and how much it was used", () => {
    const line = indexerStatusLine({ ...base, last_ok_at: at(-3), queries_24h: 214, failures_24h: 3 }, now);
    expect(line?.text).toBe("Last OK 3 min ago · 214 searches, 3 failed (24h)");
    expect(line?.color).toBe("var(--ink-dim)");
    expect(indexerStatusLine({ ...base, last_ok_at: at(-3), queries_24h: 1 }, now)?.text).toBe("Last OK 3 min ago · 1 search (24h)");
  });

  it("names the error and the next try for a paused indexer", () => {
    const line = indexerStatusLine({
      ...base, state: "backing_off", failing_since: at(-58), last_error: "login failed", backoff_until: at(60), consecutive_failures: 3,
    }, now);
    expect(line?.text).toBe(`Failing since ${hhmm(at(-58))}: login failed · next try ${hhmm(at(60))}`);
    expect(line?.color).toBe("var(--reject-text)");
  });

  it("shows a single failure without a next try", () => {
    const line = indexerStatusLine({ ...base, state: "failing", failing_since: at(-1), last_error: "HTTP 500", consecutive_failures: 1 }, now);
    expect(line?.text).toBe(`Failing since ${hhmm(at(-1))}: HTTP 500`);
    expect(line?.color).toBe("var(--avoid-text)");
  });

  it("says an unused indexer hasn't been used, and leaves a disabled one alone", () => {
    expect(indexerStatusLine({ ...base, state: "unknown" }, now)?.text).toBe("Not used yet");
    expect(indexerStatusLine({ ...base, state: "disabled" }, now)).toBeNull();
  });
});

describe("INDEXER_DOT", () => {
  it("uses the palette's good, avoid and reject tokens", () => {
    expect(INDEXER_DOT.ok.color).toBe("var(--good)");
    expect(INDEXER_DOT.failing.color).toBe("var(--avoid)");
    expect(INDEXER_DOT.backing_off.color).toBe("var(--reject)");
    expect(INDEXER_DOT.unknown.color).toBe("var(--ink-faint)");
  });
});

describe("prowlarrSyncMessage", () => {
  const none = { added: 0, updated: 0, unchanged: 0, disabled: 0, reenabled: 0, skipped_usenet: 0, flaresolverr_ready: false };
  it("lists what changed and never claims FlareSolverr unless Prowlarr has it", () => {
    expect(prowlarrSyncMessage({ ...none, added: 2, updated: 1, disabled: 1, unchanged: 4, skipped_usenet: 1 })).toBe(
      "Added 2, updated 1, turned off 1 (disabled or removed in Prowlarr). 4 indexers unchanged. Skipped 1 usenet indexer: Arrmada has no usenet download client.",
    );
    expect(prowlarrSyncMessage({ ...none, unchanged: 3 })).toBe("Nothing changed.");
    expect(prowlarrSyncMessage({ ...none, reenabled: 1, flaresolverr_ready: true, notes: ["Note."] })).toBe(
      "Turned 1 back on. Prowlarr has a FlareSolverr proxy for Cloudflare trackers. Note.",
    );
  });
});

describe("hhmm", () => {
  it("formats a 24-hour local time and a dash for nothing", () => {
    const d = new Date(2026, 9, 9, 7, 5);
    expect(hhmm(d.toISOString())).toBe("07:05");
    expect(hhmm(undefined)).toBe("—");
  });
});
