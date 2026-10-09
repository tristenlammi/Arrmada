import { useSearchParams } from "react-router-dom";

// pickTab is the validation behind useTabParam: a value the page doesn't offer (a typo, an
// old link, or a tab this viewer isn't allowed to see) falls back instead of opening a blank tab.
export function pickTab<T extends string>(value: string | null, allowed: readonly T[], fallback: T): T {
  return value !== null && (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

// useTabParam keeps a page's tab in ?tab=, so copy elsewhere can link straight to it
// ("Insights → Settings") and a reload stays put. Pass only the tabs this viewer
// may see. Switching tabs replaces the history entry, so Back still leaves the page, and
// any other query params on the page are kept.
export function useTabParam<T extends string>(allowed: readonly T[], fallback: T): [T, (t: T) => void] {
  const [params, setParams] = useSearchParams();
  const tab = pickTab(params.get("tab"), allowed, fallback);
  const setTab = (t: T) => {
    setParams((p) => {
      const next = new URLSearchParams(p);
      next.set("tab", t);
      return next;
    }, { replace: true });
  };
  return [tab, setTab];
}
