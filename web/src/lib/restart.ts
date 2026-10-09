import { api, type PendingRestart } from "./api";

// Fired after library folders are saved, so the restart banner re-checks at once instead
// of on the next page load.
export const FOLDERS_SAVED_EVENT = "arrmada:folders-saved";

export function announceFoldersSaved() {
  window.dispatchEvent(new Event(FOLDERS_SAVED_EVENT));
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export const RESTART_TIMEOUT_MESSAGE = "Arrmada hasn't come back — check docker compose logs arrmada-app";

// restartAndWait restarts Arrmada and resolves once the NEW process is answering, then
// runs `then` (default: reload the page). The first OK isn't proof: the old process keeps
// answering for its last half-second before it exits, so this waits for /status to report
// a different started_at. Rejects with RESTART_TIMEOUT_MESSAGE after `timeoutMs`.
export async function restartAndWait(opts: {
  then?: () => void | Promise<void>;
  intervalMs?: number;
  timeoutMs?: number;
} = {}): Promise<void> {
  const { then = () => window.location.reload(), intervalMs = 1500, timeoutMs = 120_000 } = opts;
  const before = (await api.status()).started_at;
  await api.restartApp();
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await sleep(intervalMs);
    try {
      if ((await api.status()).started_at !== before) {
        await then();
        return;
      }
    } catch {
      // Down between the old process and the new one: keep waiting.
    }
  }
  throw new Error(RESTART_TIMEOUT_MESSAGE);
}

// fmtAge is a running time the way people say it: "3h 12m", "12m", "40s".
export function fmtAge(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${s}s`;
}

// busyLines says what a restart would interrupt, for the confirm.
export function busyLines(b: PendingRestart["busy"]): string[] {
  const out: string[] = [];
  const pct = `${Math.round(b.convert_progress * 100)}%`;
  if (b.convert_running === 1) {
    out.push(`A conversion has been running ${fmtAge(b.convert_longest_sec)} (${pct}) and will start over.`);
  } else if (b.convert_running > 1) {
    out.push(`${b.convert_running} conversions are running (the longest for ${fmtAge(b.convert_longest_sec)}, ${pct}) and will start over.`);
  }
  const subs = b.subtitles_running + b.subtitles_queued;
  if (subs > 0) {
    out.push(`${subs} subtitle job${subs === 1 ? "" : "s"} will stop; the next subtitle sweep queues ${subs === 1 ? "it" : "them"} again.`);
  }
  return out;
}

// LIBRARY_LABEL names each folder the way Settings → Library does.
export const LIBRARY_LABEL: Record<string, string> = {
  movies: "Movies", tv: "TV", ebooks: "Ebooks", audiobooks: "Audiobooks", music: "Music", downloads: "Downloads",
};
