import { describe, expect, it } from "vitest";
import type { Attention } from "./api";
import { LINKS } from "./links";
import { needsYouRows } from "./needsYou";

const base: Attention = {
  at: "2026-10-10T12:00:00Z",
  stale: false,
  counts: { requests: 2, reviews: 0, downloads: 0, imports: 1, searches: 0, health: 1, health_errors: 1, total: 4 },
  groups: [
    { kind: "health", level: "error", count: 1, title: "1 health problem", link: "/settings/status", link_key: "status" },
    { kind: "request", level: "warning", count: 2, title: "2 requests are waiting for approval", link: "/requests?tab=needs", link_key: "requestsWaiting", sample: ["Sam requested Dune (2021)"] },
    { kind: "import", level: "warning", count: 3, title: "3 imports keep failing", link: "/downloads?show=problems", link_key: "downloadProblems" },
  ],
  items: [
    { key: "health:downloads.client.1", kind: "health", level: "error", title: "qBittorrent isn't answering", link: "/downloadclients", link_key: "downloadClients", since: 1 },
    { key: "request:1", kind: "request", level: "warning", title: "Sam requested Dune (2021)", since: 2 },
    { key: "import:abc", kind: "import", level: "warning", title: "Importing X keeps failing (3 tries)", detail: "permission denied", link: "/downloads?show=problems", link_key: "downloadProblems", since: 3 },
  ],
};

describe("needsYouRows", () => {
  it("has no rows before the first answer or when nothing is waiting", () => {
    expect(needsYouRows(undefined)).toEqual([]);
    expect(needsYouRows({ ...base, groups: [], items: [] })).toEqual([]);
  });

  it("lists health problems one per row with their own fix, and other kinds as one counted line", () => {
    const rows = needsYouRows(base);
    expect(rows[0]).toMatchObject({ key: "health:downloads.client.1", level: "error", text: "qBittorrent isn't answering", to: LINKS.downloadClients, action: "Fix" });
    expect(rows[1]).toMatchObject({ key: "group:request", text: "2 requests are waiting for approval", to: LINKS.requestsWaiting, action: "Review them", sample: ["Sam requested Dune (2021)"] });
  });

  it("counts itemized rows that didn't fit in the answer", () => {
    const rows = needsYouRows(base);
    expect(rows.slice(2).map((r) => r.text)).toEqual(["Importing X keeps failing (3 tries)", "…and 2 more"]);
    expect(rows[2].detail).toBe("permission denied");
    expect(rows[3].to).toBe(LINKS.downloadProblems);
  });
});
