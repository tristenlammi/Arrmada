import { req, type ConvertAutoPreview, type ConvertBlocked, type ConvertCandidate, type ConvertCompareStatus, type ConvertEncoder, type ConvertExcludeResult, type ConvertHistoryEntry, type ConvertHistoryOutcome, type ConvertHoldCheck, type ConvertHoldStats, type ConvertJob, type ConvertLibraryStats, type ConvertPlan, type ConvertRequestResult, type ConvertSeriesRollup, type ConvertSettings, type ConvertSkipped, type ConvertStatus, type ConvertTitleExclusion, type JobRef } from "./api";

// Convert's calls. They live here rather than in api.ts so they only download with the
// Convert pages and the convert controls on detail pages, not with a requester's first load.
export const convertApi = {
  convertHardware: () => req<{ encoders: ConvertEncoder[]; using: string; reclaimed_bytes: number; scratch_dir: string; scratch_free_bytes: number; scratch_need_bytes?: number; scratch_need_title?: string; render_devices: { path: string; pci: string; vendor: string }[]; vaapi_device: string;
    hold_check?: ConvertHoldCheck[]; hold?: ConvertHoldStats }>("/api/v1/convert/hardware"),
  convertStatus: () => req<ConvertStatus>("/api/v1/convert/status"),
  convertSettings: () => req<ConvertSettings>("/api/v1/convert/settings"),
  updateConvertSettings: (patch: Partial<ConvertSettings>) => req<ConvertSettings>("/api/v1/convert/settings", { method: "PUT", body: JSON.stringify(patch) }),
  convertReindex: () => req<{ started: boolean; reason?: string } & JobRef>("/api/v1/convert/reindex", { method: "POST" }),
  convertReindexStatus: () => req<{ running: boolean }>("/api/v1/convert/reindex"),
  convertLibrary: (media: "movies" | "tv" = "movies", seriesID?: number, convertibleOnly = false) => {
    const q = new URLSearchParams();
    if (media === "tv") q.set("media", "tv");
    if (seriesID) q.set("series", String(seriesID));
    if (convertibleOnly) q.set("convertible", "1");
    const qs = q.toString();
    return req<{ items: ConvertCandidate[] }>(`/api/v1/convert/library${qs ? `?${qs}` : ""}`).then((r) => r.items);
  },
  convertStats: () => req<ConvertLibraryStats>("/api/v1/convert/stats"),
  convertRequest: (key: string) => req<ConvertRequestResult>("/api/v1/convert/requests", { method: "POST", body: JSON.stringify({ key }) }),
  convertCancelRequest: (key?: string) => req<{ cancelled: number }>(`/api/v1/convert/requests?${key ? `key=${encodeURIComponent(key)}` : "all=1"}`, { method: "DELETE" }),
  convertCancel: (id: number) => req<{ cancelled: number }>(`/api/v1/convert/jobs/${id}/cancel`, { method: "POST" }),
  convertBlocklist: () => req<{ items: ConvertBlocked[] }>("/api/v1/convert/blocklist").then((r) => r.items),
  convertSkips: () => req<{ items: ConvertSkipped[] }>("/api/v1/convert/skips").then((r) => r.items),
  convertSkipsClear: (key?: string) =>
    req<{ cleared: boolean }>(`/api/v1/convert/skips/clear?${key ? `key=${encodeURIComponent(key)}` : "all=1"}`, { method: "POST" }),
  // Every skip of one kind — a Problems group's "Try these again" — in one request.
  convertSkipsClearKind: (kind: string) =>
    req<{ cleared: boolean; count: number }>(`/api/v1/convert/skips/clear?kind=${encodeURIComponent(kind)}`, { method: "POST" }),
  convertBlocklistClear: (key?: string) =>
    req<{ cleared: boolean }>(`/api/v1/convert/blocklist/clear?${key ? `key=${encodeURIComponent(key)}` : "all=1"}`, { method: "POST" }),
  convertLibrarySeries: () => req<{ series: ConvertSeriesRollup[] }>("/api/v1/convert/library?media=tv").then((r) => r.series),
  convertSeries: (seriesID: number, season?: number) =>
    req<{ requested: number }>(`/api/v1/convert/series/${seriesID}${season === undefined ? "" : `?season=${season}`}`, { method: "POST" }),
  convertJobs: () => req<{ jobs: ConvertJob[] }>("/api/v1/convert/jobs").then((r) => r.jobs),
  convertLogs: () => req<{ lines: { at: number; level: string; msg: string }[] }>("/api/v1/convert/logs").then((r) => r.lines),
  convertHistory: (f: { outcome?: ConvertHistoryOutcome; media?: "movie" | "episode"; q?: string; before?: string; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (f.outcome) q.set("outcome", f.outcome);
    if (f.media) q.set("media", f.media);
    if (f.q) q.set("q", f.q);
    if (f.before) q.set("before", f.before);
    if (f.limit) q.set("limit", String(f.limit));
    const qs = q.toString();
    return req<{ items: ConvertHistoryEntry[]; next: string }>(`/api/v1/convert/history${qs ? `?${qs}` : ""}`);
  },
  convertHistoryEntry: (id: number) => req<ConvertHistoryEntry>(`/api/v1/convert/history/${id}`),
  convertCompare: (key: string) => req<ConvertCompareStatus>("/api/v1/convert/compare", { method: "POST", body: JSON.stringify({ key }) }),
  convertCompareStatus: () => req<ConvertCompareStatus>("/api/v1/convert/compare"),
  convertCompareFileURL: (name: string) => `/api/v1/convert/compare/files/${encodeURIComponent(name)}`,
  convertPlan: (key: string) => req<ConvertPlan>(`/api/v1/convert/plan?key=${encodeURIComponent(key)}`),
  convertAutoPreview: (seriesID?: number, season?: number) => {
    const q = new URLSearchParams();
    if (seriesID !== undefined) q.set("series", String(seriesID));
    if (seriesID !== undefined && season !== undefined) q.set("season", String(season));
    const qs = q.toString();
    return req<ConvertAutoPreview>(`/api/v1/convert/auto-preview${qs ? `?${qs}` : ""}`);
  },
  // "Don't convert this": the films, shows and seasons Convert leaves alone, and the switch.
  convertExclusions: () => req<{ items: ConvertTitleExclusion[] }>("/api/v1/convert/exclusions").then((r) => r.items),
  setConvertExclusion: (body: { kind: "movie" | "series"; movie_id?: number; series_id?: number; season?: number; excluded: boolean }) =>
    req<ConvertExcludeResult>("/api/v1/convert/exclusions", { method: "PUT", body: JSON.stringify(body) }),
};
