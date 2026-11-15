import { req } from "./api";

// Staff-only calls for Activity → Queue that the requester's first load never needs.

/** What a bulk removal does with the files: the single Delete's first two choices. */
export type BulkRemoveMode = "keep_files" | "delete_files";

/** POST /queue/remove's answer: how many went, and each one the client refused, with why. */
export interface BulkRemoveResult {
  mode: BulkRemoveMode;
  removed: number;
  failed: { hash: string; error: string }[];
}

/** The most downloads one bulk removal takes (the server's cap). */
export const MAX_BULK_REMOVE = 500;

export const queueApi = {
  // Several downloads removed in one request, each exactly as the single Delete removes it.
  // name finds a download's grab when it has no recorded hash, as the single route's ?name= does.
  removeDownloads: (items: { hash: string; name: string }[], mode: BulkRemoveMode) =>
    req<BulkRemoveResult>("/api/v1/queue/remove", { method: "POST", body: JSON.stringify({ items, mode }) }),
};
