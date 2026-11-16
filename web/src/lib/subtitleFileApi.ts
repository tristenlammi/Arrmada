import { req, type SubLangHealth, type SubLangStatus, type SubTrack, type SubtitleAttempt, type SubtitleJob, type SubtitleProviderState, type SubtitleResult } from "./api";

// The per-file subtitle drawer's calls (GET /subtitles/file, shift, delete, sync check,
// redo, interactive search). Staff-only,
// and only the drawer and the detail pages' subtitle status import this module, so it
// rides in their lazy chunks rather than lib/api.ts, which every page (the requester's
// first load included) pays for.

/** One video, named by ids: the server finds the path, the client never sends one. */
export type SubMediaRef =
  | { kind: "movie"; movie_id: number }
  | { kind: "episode"; series_id: number; season: number; episode: number };

export interface SubCuePreview { start_ms: number; end_ms: number; text: string }

/** A subtitle file paired with the video, joined with where it came from. */
export interface SubSidecar {
  name: string;
  lang: string; // "" = untagged <base>.srt
  variant?: "" | "forced" | "sdh";
  ext: string;
  size: number;
  modified_at: number;
  cue_count: number;
  first_cues: SubCuePreview[];
  shiftable: boolean; // SRT: the nudges can rewrite it
  owned: boolean; // Arrmada wrote or adopted it
  source: string; // embedded | release | opensubtitles | ai-transcribe | ai-translate | adopted | manual | unknown
  provider?: string;
  provider_file_id?: string;
  release?: string;
  hash_match?: boolean;
  score?: number;
  model?: string;
  health: SubLangHealth; // the last sync check, in words
  shift_ms?: number; // the shift applied to it (automatic or a nudge)
  locked?: boolean;
  embedded_index?: number;
  created_at?: number;
}

export interface SubAudioTrack { index: number; lang?: string; title?: string; default?: boolean }
export interface SubOrphan { name: string; lang?: string; variant?: string }
export interface SubBackoff {
  lang: string; variant?: string; primary_reason: SubtitleResult; detail?: string; count: number;
  next_retry_at?: number; clear_on?: string[]; updated_at: number;
}

/** GET /subtitles/file: one video's whole subtitle picture. */
export interface SubFileView {
  kind: "movie" | "episode";
  movie_id?: number; series_id?: number; season?: number; episode?: number;
  media_key: string;
  title: string;
  video_path: string;
  kept: string[]; // the title's profile languages, full wants first
  languages: SubLangStatus[]; // each want of the title's profile
  /** The language profile the title uses: its own override, else Default. */
  profile?: { id: number; name: string; override: boolean };
  audio: SubAudioTrack[];
  embedded: SubTrack[];
  sidecars: SubSidecar[];
  orphans: SubOrphan[];
  backoffs: SubBackoff[];
  attempts: SubtitleAttempt[]; // the newest per language, variant and rung
  jobs: SubtitleJob[]; // queued and running for this file
}

function refQuery(m: SubMediaRef): URLSearchParams {
  return m.kind === "movie"
    ? new URLSearchParams({ kind: "movie", id: String(m.movie_id) })
    : new URLSearchParams({ kind: "episode", series: String(m.series_id), season: String(m.season), episode: String(m.episode) });
}

export function subMediaKey(m: SubMediaRef): string {
  return m.kind === "movie" ? `m:${m.movie_id}` : `e:${m.series_id}:${m.season}:${m.episode}`;
}

/** A redo of one language (POST /subtitles/redo): the drawer's Extract, Generate and Use this. */
export interface SubRedo {
  lang: string;
  variant?: "" | "sdh";
  mode: "next" | "ai" | "extract" | "pick";
  track_index?: number; // extract: 0:s:N
  audio_stream?: number; // ai: 0:a:N
  file_id?: string; // pick: the OpenSubtitles file
}

/** One OpenSubtitles candidate, scored against the file (internal/subtitles/scoring.go). */
export interface SubCandidate {
  file_id: string;
  language: string;
  release: string;
  file_name?: string;
  downloads: number;
  hearing_impaired: boolean;
  hash_match: boolean;
  fps?: number;
  trusted?: boolean;
  uploader_rank?: string;
  score: number;
  reasons: string[]; // "+25 same group (FLUX)", "−40 25 vs 23.976 fps"
  rejected?: string; // why it can never be used for this want
  blocklisted?: boolean;
}

/** POST /subtitles/search: the candidates and what a download would cost. */
export interface SubSearchView {
  lang: string;
  variant?: string;
  candidates: SubCandidate[];
  current_file_id?: string;
  min_score: number;
  can_download: boolean;
  quota_remaining: number; // -1 = unknown
  quota_reset_at: number; // unix seconds; 0 = not paused
  provider_state: SubtitleProviderState;
  searched_at: number;
}

export const subtitleFileApi = {
  view: (m: SubMediaRef) => req<SubFileView>(`/api/v1/subtitles/file?${refQuery(m).toString()}`),
  // offsetMs > 0 moves every cue later.
  shift: (m: SubMediaRef, name: string, offsetMs: number) =>
    req<SubSidecar>("/api/v1/subtitles/file/shift", { method: "POST", body: JSON.stringify({ ...m, name, offset_ms: offsetMs }) }),
  // To the recycle bin, never deleted outright; a bin failure leaves the file and errors.
  remove: (m: SubMediaRef, name: string) => {
    const q = refQuery(m);
    q.set("name", name);
    return req<{ removed: string }>(`/api/v1/subtitles/file?${q.toString()}`, { method: "DELETE" });
  },
  // Measures one language's sidecar against the audio, as a job (reading a soundtrack takes a while).
  syncCheck: (m: SubMediaRef, lang: string, variant = "") =>
    req<{ job_id?: number; file: string }>("/api/v1/subtitles/sync-check", { method: "POST", body: JSON.stringify({ ...m, lang, variant }) }),
  // Check sync, then move the file onto the speech when the measurement is trusted. A job.
  syncFix: (m: SubMediaRef, lang: string, variant = "") =>
    req<{ job_id?: number; file: string }>("/api/v1/subtitles/sync-fix", { method: "POST", body: JSON.stringify({ ...m, lang, variant }) }),
  // The same for every episode of a show (or one season), one after another. A long job.
  syncFixSeries: (seriesId: number, season?: number) =>
    req<{ job_id?: number }>(`/api/v1/subtitles/sync-fix/series/${seriesId}${season === undefined ? "" : `?season=${season}`}`, { method: "POST" }),
  // Queues a manual-priority job for one language; the old file goes to the bin only once the new one exists.
  redo: (m: SubMediaRef, r: SubRedo) =>
    req<SubtitleJob>("/api/v1/subtitles/redo", { method: "POST", body: JSON.stringify({ ...m, ...r }) }),
  // No download quota is spent; refresh asks OpenSubtitles again instead of reusing the last answer.
  search: (m: SubMediaRef, lang: string, refresh = false) =>
    req<SubSearchView>("/api/v1/subtitles/search", { method: "POST", body: JSON.stringify({ ...m, lang, refresh }) }),
};
