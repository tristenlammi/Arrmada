import { useCallback, useEffect, useRef, useState } from "react";
import { SIGNED_OUT_EVENT } from "./api";

// A small data cache for list pages. Going Back to a page renders its last data at
// once instead of an empty list (which also keeps scroll restoration working), then
// revalidates in the background when the data is older than staleMs. Polling stays
// in usePoll; this only remembers and shares answers.

interface Entry { data: unknown; at: number }
type QueryEvent = { kind: "data"; data: unknown } | { kind: "stale" };

// LRU-bounded: Map keeps insertion order, and every read or write re-inserts.
const MAX_ENTRIES = 50;
const cache = new Map<string, Entry>();
// One request per key at a time; a second caller joins the first.
const inflight = new Map<string, Promise<unknown>>();
// Bumped by invalidate, a forced refetch and sign-out, so an answer to a request
// made before that moment isn't cached over newer truth.
const versions = new Map<string, number>();
let generation = 0;
const listeners = new Map<string, Set<(e: QueryEvent) => void>>();

const versionOf = (key: string) => versions.get(key) ?? 0;
const bump = (key: string) => versions.set(key, versionOf(key) + 1);

function emit(key: string, e: QueryEvent) {
  listeners.get(key)?.forEach((fn) => fn(e));
}

function store(key: string, data: unknown) {
  cache.delete(key);
  cache.set(key, { data, at: Date.now() });
  while (cache.size > MAX_ENTRIES) cache.delete(cache.keys().next().value as string);
  emit(key, { kind: "data", data });
}

function read(key: string): Entry | undefined {
  const e = cache.get(key);
  if (e) { cache.delete(key); cache.set(key, e); }
  return e;
}

// fetchQuery runs fetcher for key, sharing an in-flight request unless fresh is set
// (a deliberate refetch after a change must not join a request made before it).
export function fetchQuery<T>(key: string, fetcher: () => Promise<T>, fresh = false): Promise<T> {
  if (!fresh) {
    const shared = inflight.get(key);
    if (shared) return shared as Promise<T>;
  } else {
    bump(key);
  }
  const gen = generation, ver = versionOf(key);
  const p: Promise<T> = fetcher().then((data) => {
    if (gen === generation && ver === versionOf(key)) store(key, data);
    return data;
  });
  inflight.set(key, p);
  const clear = () => { if (inflight.get(key) === p) inflight.delete(key); };
  p.then(clear, clear);
  return p;
}

/** The cached data for key, if any. */
export function getQueryData<T>(key: string): T | undefined {
  return cache.get(key)?.data as T | undefined;
}

/** Writes key's cached data (an optimistic add or remove); mounted pages see it at once. */
export function setQueryData<T>(key: string, update: T | ((old: T | undefined) => T)) {
  const old = cache.get(key)?.data as T | undefined;
  const next = typeof update === "function" ? (update as (o: T | undefined) => T)(old) : update;
  bump(key); // an older request still in flight mustn't overwrite this
  store(key, next);
}

/** Drops every cached key starting with prefix; pages showing one refetch now. */
export function invalidate(prefix: string) {
  for (const key of [...cache.keys()]) if (key.startsWith(prefix)) cache.delete(key);
  for (const key of [...inflight.keys()]) if (key.startsWith(prefix)) { inflight.delete(key); bump(key); }
  for (const key of [...listeners.keys()]) if (key.startsWith(prefix)) { bump(key); emit(key, { kind: "stale" }); }
}

/** Forgets everything. Runs on sign-out so a shared device never shows the last user's lists. */
export function clearQueryCache() {
  generation++;
  cache.clear();
  inflight.clear();
}
if (typeof window !== "undefined") window.addEventListener(SIGNED_OUT_EVENT, clearQueryCache);

export interface QueryOptions {
  /** false: don't fetch (yet); data stays undefined. */
  enabled?: boolean;
  /** Cached data younger than this is used without asking the server again. */
  staleMs?: number;
}

export interface QueryResult<T> {
  data: T | undefined;
  error: Error | null;
  /** A request for this key is out (the first load or a background revalidation). */
  loading: boolean;
  /** Ask the server again now. Never rejects; a failure lands in error. */
  refetch: () => Promise<void>;
  /** Write the cached data directly (optimistic update). */
  mutate: (update: T | ((old: T | undefined) => T)) => void;
}

interface State<T> { key: string; data: T | undefined; error: Error | null; loading: boolean }

// useQuery<T>(key, fetcher) gives a page honest states to render from:
//   no data + loading  -> a skeleton (never the "nothing here yet" text);
//   no data + error    -> an error with Retry (never the empty state);
//   data               -> the content, even while it revalidates; an error then is
//                         a stale-data notice, not a blank page.
export function useQuery<T>(key: string, fetcher: () => Promise<T>, { enabled = true, staleMs = 15_000 }: QueryOptions = {}): QueryResult<T> {
  const initial = (): State<T> => {
    const hit = enabled ? read(key) : undefined;
    return { key, data: hit?.data as T | undefined, error: null, loading: enabled && (!hit || Date.now() - hit.at >= staleMs) };
  };
  const [state, setState] = useState<State<T>>(initial);
  // A new key (another page of results, another id) starts from its own cache entry
  // straight away, rather than showing the previous key's data for a render.
  let current = state;
  if (state.key !== key) {
    current = initial();
    setState(current);
  }

  const fetcherRef = useRef(fetcher);
  useEffect(() => { fetcherRef.current = fetcher; });
  const seq = useRef(0);
  // Bumping seq orphans every answer still in flight for this hook.
  const orphan = useCallback(() => { seq.current++; }, []);

  const run = useCallback((fresh: boolean): Promise<void> => {
    const my = ++seq.current;
    const started = Date.now();
    setState((s) => (s.key === key ? { ...s, loading: true } : s));
    return fetchQuery(key, () => fetcherRef.current(), fresh).then(
      (data) => {
        if (seq.current !== my) return;
        // A mutate that landed while this request was out is newer than its answer.
        const hit = cache.get(key);
        setState({ key, data: hit && hit.at >= started ? (hit.data as T) : data, error: null, loading: false });
      },
      (err: unknown) => {
        if (seq.current !== my) return;
        const error = err instanceof Error ? err : new Error(String(err));
        setState((s) => ({ key, data: s.key === key ? s.data : undefined, error, loading: false }));
      },
    );
  }, [key]);

  useEffect(() => {
    if (!enabled) return;
    const hit = cache.get(key);
    if (!hit || Date.now() - hit.at >= staleMs) run(false);
    const onEvent = (e: QueryEvent) => {
      if (e.kind === "stale") { run(false); return; }
      setState((s) => ({ key, data: e.data as T, error: null, loading: s.key === key ? s.loading : false }));
    };
    let set = listeners.get(key);
    if (!set) listeners.set(key, (set = new Set()));
    set.add(onEvent);
    return () => {
      set.delete(onEvent);
      if (set.size === 0) listeners.delete(key);
      orphan(); // ignore answers that arrive after we've moved on
    };
    // staleMs is read once per key; changing it shouldn't refetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, enabled, run, orphan]);

  const refetch = useCallback(() => run(true), [run]);
  const mutate = useCallback((update: T | ((old: T | undefined) => T)) => setQueryData<T>(key, update), [key]);

  return { data: current.data, error: current.error, loading: current.loading, refetch, mutate };
}
