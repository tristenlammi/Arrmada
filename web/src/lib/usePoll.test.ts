// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { usePoll, usePollBurst } from "./usePoll";
import { SIGNED_OUT_EVENT } from "./api";

let visibility: DocumentVisibilityState = "visible";
const setVisibility = (v: DocumentVisibilityState) => {
  visibility = v;
  document.dispatchEvent(new Event("visibilitychange"));
};

beforeEach(() => {
  vi.useFakeTimers();
  visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// Let resolved promises (the awaited fn) run their continuations.
const flush = () => vi.advanceTimersByTimeAsync(0);

describe("usePoll", () => {
  it("runs now, then every ms", async () => {
    const fn = vi.fn();
    renderHook(() => usePoll(fn, 1000));
    await flush();
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fn).toHaveBeenCalledTimes(5);
  });

  it("waits for a slow request to settle before the next wait starts", async () => {
    let release: () => void = () => {};
    const fn = vi.fn(() => new Promise<void>((r) => { release = r; }));
    renderHook(() => usePoll(fn, 1000));
    await flush();
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5000); // still in flight: no pile-up
    expect(fn).toHaveBeenCalledTimes(1);
    release();
    await flush();
    await vi.advanceTimersByTimeAsync(999);
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("stops while hidden and fires straight away when visible again", async () => {
    const fn = vi.fn();
    renderHook(() => usePoll(fn, 1000));
    await flush();
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fn).toHaveBeenCalledTimes(1);
    setVisibility("visible");
    await flush();
    expect(fn).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it("keeps going while hidden when pauseHidden is false", async () => {
    const fn = vi.fn();
    renderHook(() => usePoll(fn, 1000, { pauseHidden: false }));
    await flush();
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(2000);
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it("is off when ms is null, and re-arms when ms changes", async () => {
    const fn = vi.fn();
    const { rerender } = renderHook(({ ms }: { ms: number | null }) => usePoll(fn, ms), { initialProps: { ms: null as number | null } });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fn).not.toHaveBeenCalled();
    rerender({ ms: 500 });
    await flush();
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(500);
    expect(fn).toHaveBeenCalledTimes(2);
    rerender({ ms: null });
    await vi.advanceTimersByTimeAsync(5000);
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("waits first when immediate is false", async () => {
    const fn = vi.fn();
    renderHook(() => usePoll(fn, 1000, { immediate: false }));
    await flush();
    expect(fn).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("stops for good on sign-out", async () => {
    const fn = vi.fn();
    renderHook(() => usePoll(fn, 1000));
    await flush();
    window.dispatchEvent(new CustomEvent(SIGNED_OUT_EVENT));
    await vi.advanceTimersByTimeAsync(5000);
    setVisibility("hidden");
    setVisibility("visible");
    await flush();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("uses the latest fn without re-arming", async () => {
    const a = vi.fn(), b = vi.fn();
    const { rerender } = renderHook(({ f }: { f: () => void }) => usePoll(f, 1000), { initialProps: { f: a } });
    await flush();
    rerender({ f: b });
    await vi.advanceTimersByTimeAsync(1000);
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
  });

  it("keeps polling after fn throws", async () => {
    const fn = vi.fn(async () => { throw new Error("down"); });
    renderHook(() => usePoll(fn, 1000));
    await flush();
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(2);
  });
});

describe("usePollBurst", () => {
  it("runs `times` times after start, then calls onDone and stops", async () => {
    const fn = vi.fn();
    const done = vi.fn();
    const { result } = renderHook(() => usePollBurst(fn, 1000, 3, done));
    await vi.advanceTimersByTimeAsync(5000);
    expect(fn).not.toHaveBeenCalled(); // idle until started
    act(() => result.current());
    await vi.advanceTimersByTimeAsync(2999);
    expect(fn).toHaveBeenCalledTimes(2);
    expect(done).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(fn).toHaveBeenCalledTimes(3);
    expect(done).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fn).toHaveBeenCalledTimes(3);
  });
});
