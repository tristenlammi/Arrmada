import { describe, expect, it } from "vitest";
import { emptyReleasesMessage, issueCounts, issueHeadline } from "./IndexerIssues";

const tl = { indexer: "TorrentLeech", error: "login failed" };
const x = { indexer: "1337x", error: "FlareSolverr unreachable" };

describe("issueHeadline", () => {
  it("counts the failures against every indexer asked", () => {
    expect(issueHeadline({ indexer_issues: [tl, x], searched: 6 })).toBe("2 of 6 indexers failed");
  });

  it("says when every indexer failed", () => {
    expect(issueHeadline({ indexer_issues: [tl, x], searched: 2 })).toBe("All 2 indexers failed");
    expect(issueHeadline({ indexer_issues: [tl], searched: 1 })).toBe("Your indexer failed");
  });

  it("names paused indexers apart from failed ones", () => {
    const list = { indexer_issues: [tl, { indexer: "MAM", error: "paused until 15:00", skipped: true }], searched: 3 };
    expect(issueCounts(list)).toMatchObject({ failed: 1, paused: 1, total: 4, allFailed: false });
    expect(issueHeadline(list)).toBe("1 of 4 indexers failed · 1 paused after repeated failures");
  });
});

describe("emptyReleasesMessage", () => {
  it("never says nothing was found when nobody could answer", () => {
    expect(emptyReleasesMessage({ indexer_issues: [tl, x], searched: 2 })).toMatch(/^Couldn't search: every indexer failed/);
  });

  it("says how many indexers had nothing", () => {
    expect(emptyReleasesMessage({ indexer_issues: [tl], searched: 4 })).toBe("No releases found on 3 indexers.");
    expect(emptyReleasesMessage({ searched: 1 }, "ebook releases")).toBe("No ebook releases found on 1 indexer.");
    expect(emptyReleasesMessage(null)).toBe("No releases found on your indexers.");
  });
});
