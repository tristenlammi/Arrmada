// Chunk-load recovery. After a deploy, a tab still running the old index.html asks
// for hashed chunks that no longer exist on the server, and the dynamic import
// fails. One reload fetches the new index.html and fixes it. The timestamp guard
// makes sure a chunk that is missing for good shows the error card instead of
// reloading forever.

const KEY = "arrmada.chunkReload";
const WINDOW_MS = 10_000;

// The messages Chrome, Safari, Firefox and webpack-style loaders use for a failed
// dynamic import or chunk fetch.
const CHUNK_ERROR =
  /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|ChunkLoadError/i;

export function isChunkLoadError(err: unknown): boolean {
  if (!err) return false;
  const e = err as { name?: unknown; message?: unknown };
  return CHUNK_ERROR.test(`${String(e.name ?? "")} ${String(e.message ?? err)}`);
}

type Store = Pick<Storage, "getItem" | "setItem">;

// claimChunkReload reports whether a reload is allowed now, and records it if so.
// A reload within the last 10s means the previous one didn't help, so it says no.
// If storage is unavailable (private mode, blocked) it also says no: without the
// guard a reload could loop.
export function claimChunkReload(store: Store | null | undefined, now: number): boolean {
  if (!store) return false;
  try {
    const last = Number(store.getItem(KEY));
    if (last && now - last >= 0 && now - last < WINDOW_MS) return false;
    store.setItem(KEY, String(now));
    return true;
  } catch {
    return false;
  }
}

function sessionStore(): Storage | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

// reloadOnce reloads the page if the guard allows it, and returns true when a
// reload is under way.
export function reloadOnce(): boolean {
  if (!claimChunkReload(sessionStore(), Date.now())) return false;
  window.location.reload();
  return true;
}

// reloadForChunkError reloads once if err is a chunk-load failure.
export function reloadForChunkError(err: unknown): boolean {
  return isChunkLoadError(err) && reloadOnce();
}
