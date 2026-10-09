import { useSearchParams } from "react-router-dom";

// pickTab is the validation behind useTabParam: a value the page doesn't offer (a typo, an
// old link, or a tab this viewer isn't allowed to see) falls back instead of opening a blank tab.
export function pickTab<T extends string>(value: string | null, allowed: readonly T[], fallback: T): T {
  return value !== null && (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

// withTab returns the page's query with the tab switched. The default tab is left out, so
// a page's plain address is its default view; every other param on the page is kept.
export function withTab(params: URLSearchParams, key: string, value: string, fallback: string): URLSearchParams {
  const next = new URLSearchParams(params);
  if (value === fallback) next.delete(key);
  else next.set(key, value);
  return next;
}

// useTabParam keeps a page's tab in the address (?tab= by default), so copy elsewhere can
// link straight to it ("Settings → System → API keys"), a reload stays put, and a tab can
// be bookmarked. Each switch is a new history entry, so Back steps back through the tabs
// before it leaves the page. Pass only the tabs this viewer may see.
export function useTabParam<T extends string>(allowed: readonly T[], fallback: T, key = "tab"): [T, (t: T) => void] {
  const [params, setParams] = useSearchParams();
  const tab = pickTab(params.get(key), allowed, fallback);
  const setTab = (t: T) => {
    if (t === tab) return;
    setParams((p) => withTab(p, key, t, fallback));
  };
  return [tab, setTab];
}
