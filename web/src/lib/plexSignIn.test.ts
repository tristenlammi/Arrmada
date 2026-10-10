// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { plexNav, runPlexFlow, takePendingPlexPin, type PlexFlow } from "./plexSignIn";

function fakePopup() {
  return { closed: false, location: { href: "" }, close: vi.fn(function (this: { closed: boolean }) { this.closed = true; }) };
}

function flow(answers: (string | null)[], extra: Partial<PlexFlow<string>> = {}): PlexFlow<string> & { polls: number } {
  let polls = 0;
  return {
    kind: "login" as const,
    get polls() { return polls; },
    start: vi.fn(async () => ({ id: 42, auth_url: "https://app.plex.tv/auth#?code=X" })),
    poll: vi.fn(async (): Promise<string | null> => answers[Math.min(polls++, answers.length - 1)]),
    ...extra,
  };
}

describe("runPlexFlow", () => {
  beforeEach(() => { vi.useFakeTimers(); sessionStorage.clear(); });
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

  it("points the popup at plex.tv and closes it once Plex approves", async () => {
    const w = fakePopup();
    const run = runPlexFlow(flow([null, "user"]), w as unknown as Window);
    await vi.advanceTimersByTimeAsync(4100);
    await expect(run.outcome).resolves.toEqual({ done: "user" });
    expect(w.location.href).toBe("https://app.plex.tv/auth#?code=X");
    expect(w.close).toHaveBeenCalled();
  });

  it("stops within one poll when the Plex window is closed, after a last look", async () => {
    const w = fakePopup();
    const f = flow([null]);
    const run = runPlexFlow(f, w as unknown as Window);
    const settled = run.outcome.catch((e: Error) => e.message);
    await vi.advanceTimersByTimeAsync(2100);
    w.closed = true;
    await vi.advanceTimersByTimeAsync(2100);
    await expect(settled).resolves.toMatch(/closed before you finished/);
    expect(f.poll).toHaveBeenCalledTimes(2);
  });

  it("keeps waiting when the window reads closed from the start (its handle was cut, not closed)", async () => {
    const w = fakePopup();
    w.closed = true;
    const run = runPlexFlow(flow([null, null, "user"]), w as unknown as Window);
    await vi.advanceTimersByTimeAsync(6100);
    await expect(run.outcome).resolves.toEqual({ done: "user" });
  });

  it("waits out a network blip while polling", async () => {
    const w = fakePopup();
    let n = 0;
    const f = flow([null], { poll: async () => { if (n++ === 0) throw new TypeError("Failed to fetch"); return "user"; } });
    const run = runPlexFlow(f, w as unknown as Window);
    await vi.advanceTimersByTimeAsync(4100);
    await expect(run.outcome).resolves.toEqual({ done: "user" });
  });

  it("goes by redirect when there is no window, remembering the PIN for this tab", async () => {
    const assign = vi.spyOn(plexNav, "assign").mockImplementation(() => {});
    const before = vi.fn();
    const f = flow([null], { beforeRedirect: before });
    const run = runPlexFlow(f, null);
    await expect(run.outcome).resolves.toBe("redirected");
    expect(f.start).toHaveBeenCalledWith("redirect");
    expect(before).toHaveBeenCalled();
    expect(assign).toHaveBeenCalledWith("https://app.plex.tv/auth#?code=X");
    expect(sessionStorage.getItem("arrmada.plexpin.login")).toBe("42");
  });

  it("closes the empty window when the PIN can't be started", async () => {
    const w = fakePopup();
    const run = runPlexFlow(flow([null], { start: async () => { throw new Error("Plex sign-in is disabled"); } }), w as unknown as Window);
    await expect(run.outcome).rejects.toThrow("Plex sign-in is disabled");
    expect(w.close).toHaveBeenCalled();
  });
});

describe("takePendingPlexPin", () => {
  beforeEach(() => sessionStorage.clear());

  it("takes the PIN this tab sent to plex.tv and strips it from the address", () => {
    sessionStorage.setItem("arrmada.plexpin.login", "42");
    window.history.replaceState(null, "", "/?plexpin=42&x=1");
    expect(takePendingPlexPin("login", "plexpin")).toBe(42);
    expect(window.location.search).toBe("?x=1");
    expect(sessionStorage.getItem("arrmada.plexpin.login")).toBeNull();
  });

  it("ignores a PIN this tab didn't start (someone else's link)", () => {
    window.history.replaceState(null, "", "/?plexpin=99");
    expect(takePendingPlexPin("login", "plexpin")).toBeNull();
    expect(window.location.search).toBe("");
    sessionStorage.setItem("arrmada.plexpin.login", "42");
    window.history.replaceState(null, "", "/?plexpin=43");
    expect(takePendingPlexPin("login", "plexpin")).toBeNull();
  });

  it("keeps each kind apart and uses the caller's strip", () => {
    sessionStorage.setItem("arrmada.plexpin.connect", "42");
    window.history.replaceState(null, "", "/settings/plex?plexpin=42");
    expect(takePendingPlexPin("login", "plexpin", () => {})).toBeNull();
    sessionStorage.setItem("arrmada.plexpin.connect", "42");
    const strip = vi.fn();
    expect(takePendingPlexPin("connect", "plexpin", strip)).toBe(42);
    expect(strip).toHaveBeenCalled();
  });
});
