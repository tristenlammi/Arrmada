import { useState } from "react";

// usePersisted is useState whose value survives navigating away: a view toggle
// (grid/table, by author/by book) that reset on every visit read as the page
// forgetting what you told it. Stored per key in localStorage; a missing or
// unparseable value, or a browser that blocks storage, falls back to the default.
export function usePersisted<T extends string>(key: string, initial: T, allowed?: readonly T[]): [T, (v: T) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      const v = localStorage.getItem(key);
      if (v !== null && (!allowed || (allowed as readonly string[]).includes(v))) return v as T;
    } catch { /* storage blocked */ }
    return initial;
  });
  const set = (v: T) => {
    setValue(v);
    try { localStorage.setItem(key, v); } catch { /* ignore quota / blocked storage */ }
  };
  return [value, set];
}
