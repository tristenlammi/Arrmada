// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { clearQueryCache, fetchQuery, getQueryData, invalidate, setQueryData, useQuery } from "./query";
import { SIGNED_OUT_EVENT } from "./api";

beforeEach(() => {
  vi.useFakeTimers();
  clearQueryCache();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const flush = () => act(() => vi.advanceTimersByTimeAsync(0));

// A fetcher whose answers are released by hand.
function deferred<T>() {
  const calls: { resolve: (v: T) => void; reject: (e: Error) => void }[] = [];
  const fn = vi.fn(() => new Promise<T>((resolve, reject) => { calls.push({ resolve, reject }); }));
  return { fn, calls };
}

describe("useQuery", () => {
  it("starts loading with no data (a skeleton, never the empty state), then shows the answer", async () => {
    const f = deferred<string[]>();
    const { result } = renderHook(() => useQuery("movies", f.fn));
    expect(result.current).toMatchObject({ data: undefined, loading: true, error: null });
    f.calls[0].resolve(["Dune"]);
    await flush();
    expect(result.current).toMatchObject({ data: ["Dune"], loading: false, error: null });
  });

  it("renders cached data at once on remount and revalidates only when stale", async () => {
    const fetcher = vi.fn(async () => ["Dune"]);
    const first = renderHook(() => useQuery("movies", fetcher));
    await flush();
    first.unmount();

    const again = renderHook(() => useQuery("movies", fetcher));
    expect(again.result.current.data).toEqual(["Dune"]); // first render, no flash
    expect(again.result.current.loading).toBe(false);
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(1); // still fresh
    again.unmount();

    vi.advanceTimersByTime(15_000);
    const later = renderHook(() => useQuery("movies", fetcher));
    expect(later.result.current.data).toEqual(["Dune"]);
    expect(later.result.current.loading).toBe(true); // revalidating behind the cached grid
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("shares one request between concurrent callers", async () => {
    const f = deferred<number>();
    const a = renderHook(() => useQuery("count", f.fn));
    const b = renderHook(() => useQuery("count", f.fn));
    expect(f.fn).toHaveBeenCalledTimes(1);
    f.calls[0].resolve(7);
    await flush();
    expect(a.result.current.data).toBe(7);
    expect(b.result.current.data).toBe(7);
  });

  it("reports an error with no data, then recovers on retry", async () => {
    const f = deferred<string>();
    const { result } = renderHook(() => useQuery("x", f.fn));
    f.calls[0].reject(new Error("backend down"));
    await flush();
    expect(result.current).toMatchObject({ data: undefined, loading: false });
    expect(result.current.error?.message).toBe("backend down");

    let retry: Promise<void> = Promise.resolve();
    act(() => { retry = result.current.refetch(); });
    expect(result.current.loading).toBe(true);
    f.calls[1].resolve("ok");
    await act(() => retry);
    expect(result.current).toMatchObject({ data: "ok", error: null, loading: false });
  });

  it("keeps showing data when a later refresh fails", async () => {
    let fail = false;
    const fetcher = vi.fn(async () => { if (fail) throw new Error("blip"); return "v1"; });
    const { result } = renderHook(() => useQuery("k", fetcher));
    await flush();
    fail = true;
    await act(() => result.current.refetch());
    expect(result.current.data).toBe("v1");
    expect(result.current.error?.message).toBe("blip");
  });

  it("invalidate(prefix) drops matching keys and refetches mounted ones", async () => {
    let n = 0;
    const fetcher = vi.fn(async () => ++n);
    const { result } = renderHook(() => useQuery("movies:list", fetcher));
    await flush();
    await fetchQuery("movies:7", async () => "detail");
    await fetchQuery("series:list", async () => "other");
    expect(result.current.data).toBe(1);

    act(() => invalidate("movies:"));
    await flush();
    expect(getQueryData("movies:7")).toBeUndefined();
    expect(getQueryData("series:list")).toBe("other");
    expect(result.current.data).toBe(2);
  });

  it("mutate updates every page showing the key", async () => {
    const fetcher = vi.fn(async () => ["a", "b"]);
    const one = renderHook(() => useQuery<string[]>("list", fetcher));
    const two = renderHook(() => useQuery<string[]>("list", fetcher));
    await flush();
    act(() => one.result.current.mutate((xs) => (xs ?? []).filter((x) => x !== "a")));
    expect(one.result.current.data).toEqual(["b"]);
    expect(two.result.current.data).toEqual(["b"]);
  });

  it("does not let an answer from before a mutate overwrite it", async () => {
    const f = deferred<string[]>();
    const { result } = renderHook(() => useQuery<string[]>("list", f.fn));
    act(() => setQueryData<string[]>("list", ["optimistic"]));
    f.calls[0].resolve(["old"]);
    await flush();
    expect(getQueryData("list")).toEqual(["optimistic"]);
    expect(result.current.data).toEqual(["optimistic"]);
  });

  it("clears the whole cache on sign-out", async () => {
    await fetchQuery("movies", async () => ["mine"]);
    expect(getQueryData("movies")).toEqual(["mine"]);
    window.dispatchEvent(new CustomEvent(SIGNED_OUT_EVENT));
    expect(getQueryData("movies")).toBeUndefined();
  });

  it("does not cache an answer that arrives after sign-out", async () => {
    const f = deferred<string>();
    const p = fetchQuery("me", f.fn);
    window.dispatchEvent(new CustomEvent(SIGNED_OUT_EVENT));
    f.calls[0].resolve("previous user");
    await p;
    expect(getQueryData("me")).toBeUndefined();
  });

  it("evicts the least recently used entry beyond 50", async () => {
    for (let i = 0; i < 50; i++) await fetchQuery(`k${i}`, async () => i);
    getQueryData("k0"); // a peek doesn't count as use
    renderHook(() => useQuery("k0", async () => 0)); // a render does
    await fetchQuery("k50", async () => 50);
    expect(getQueryData("k0")).toBe(0);
    expect(getQueryData("k1")).toBeUndefined();
    expect(getQueryData("k50")).toBe(50);
  });

  it("does nothing while disabled", async () => {
    const fetcher = vi.fn(async () => 1);
    const { result, rerender } = renderHook(({ on }: { on: boolean }) => useQuery("d", fetcher, { enabled: on }), { initialProps: { on: false } });
    await flush();
    expect(fetcher).not.toHaveBeenCalled();
    expect(result.current.loading).toBe(false);
    rerender({ on: true });
    await flush();
    expect(result.current.data).toBe(1);
  });
});
