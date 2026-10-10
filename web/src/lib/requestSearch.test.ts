import { describe, expect, it } from "vitest";
import { searchingDetail } from "./requestSearch";

const now = Date.parse("2026-10-10T12:00:00Z");

describe("searchingDetail", () => {
  it("says a request nobody has searched for yet is being looked for", () => {
    expect(searchingDetail({ stage: "searching" }, "", now)).toBe("Looking for a release");
    expect(searchingDetail({ stage: "searching" }, "3 of 8 episodes", now)).toBe("3 of 8 episodes · looking for more");
  });

  it("adds when it was last checked", () => {
    expect(searchingDetail({ stage: "searching", last_search_at: "2026-10-10T11:55:00Z" }, "", now)).toBe("Looking for a release (checked 5m ago)");
  });

  it("says searches came up empty, without saying why", () => {
    const line = searchingDetail({ stage: "searching", misses: 3, last_search_at: "2026-10-10T10:00:00Z" }, "", now);
    expect(line).toBe("Still looking — nothing suitable yet (checked 2h ago)");
  });

  it("keeps a book's next check, and says when one has slowed to monthly", () => {
    expect(searchingDetail({ stage: "searching", misses: 3, note: "Not found yet", next_check_at: "2026-10-09T09:00:00Z", last_search_at: "2026-10-07T12:00:00Z" }, "", now))
      .toBe("Not found yet — last checked 3d ago, next check soon");
    expect(searchingDetail({ stage: "searching", misses: 12, search_stopped: true, next_check_at: "2026-11-01T09:00:00Z" }, "", now))
      .toBe("Couldn't find it yet — searching again later");
  });

  it("passes the server's own note through", () => {
    expect(searchingDetail({ stage: "searching", note: "Not out yet" }, "", now)).toBe("Not out yet");
  });
});
