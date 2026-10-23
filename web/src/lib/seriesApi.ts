import { req, type BlockEntry, type DeletePreview, type JobRef, type ManualImportList, type RenameSkip, type SceneOverride, type SeriesImportCandidate, type SeriesNumbering, type SeriesRenameItem } from "./api";

/** One episode file outside its show's folder, and what putting it back does with it:
 * move it into the show's season folder; recycle it (the folder already has this episode,
 * as good or better); or leave it for you (it isn't clear which copy to keep). */
export interface MisplacedItem {
  from: string;
  to?: string;
  season: number;
  episode: number;
  action: "move" | "recycle" | "leave";
  existing?: string;
  note?: string;
}

/** GET /series/{id}/misplaced: where the show's misplaced files go. why: nothing can go back. */
export interface MisplacedPlan { show_folder: string; new_folder?: boolean; why?: string; items: MisplacedItem[] }

/** POST /series/{id}/misplaced's answer. */
export interface MisplacedResult { moved: number; recycled: number; skipped: RenameSkip[]; removed_folders: string[] }

/** PUT /series/{id}/monitor's answer. resumed: this change turned the show's searching on. */
export interface MonitorReply { monitored: boolean; monitor_new_seasons: boolean; monitor_preset?: string; resumed?: boolean }

/** DELETE /series/{id}/seasons's answer: which seasons went, how many files moved, and the
 * files the recycle bin refused (empty on success). */
export interface BulkDeleteSeasonsReply { deleted_seasons: number[]; deleted_files: number; bytes: number; errors?: string[] }

// Series management calls (manual import, renames, numbering, monitoring, scene overrides,
// deletes). They live here rather than in api.ts so they only download with the staff
// pages that use them, not with a requester's first load.
export const seriesApi = {
  seriesManualImportList: (id: number) =>
    req<ManualImportList<SeriesImportCandidate>>(`/api/v1/series/${id}/manualimport`),
  seriesManualImport: (id: number, path: string) =>
    req<{ status: string; background?: boolean } & JobRef>(`/api/v1/series/${id}/manualimport`, { method: "POST", body: JSON.stringify({ path }) }),
  seriesRenamePreview: (id: number) =>
    req<{ items: SeriesRenameItem[]; matches: boolean }>(`/api/v1/series/${id}/rename`),
  // Applies only the previewed items: anything that changed since the preview is skipped
  // and reported, never moved blind.
  renameSeries: (id: number, items: SeriesRenameItem[]) =>
    req<{ renamed: number; skipped: RenameSkip[] }>(`/api/v1/series/${id}/rename`, { method: "POST", body: JSON.stringify({ items }) }),
  // Episode files outside the show's folder (#56): a preview, then putting back exactly the
  // previewed items — anything that changed in between is skipped and reported.
  misplacedPreview: (id: number) => req<MisplacedPlan>(`/api/v1/series/${id}/misplaced`),
  putMisplacedBack: (id: number, items: MisplacedItem[]) =>
    req<MisplacedResult>(`/api/v1/series/${id}/misplaced`, { method: "POST", body: JSON.stringify({ items }) }),
  // A numbering change waiting for review (pending: null when there's none).
  seriesNumbering: (id: number) => req<SeriesNumbering>(`/api/v1/series/${id}/numbering`),
  // Applies the reviewed plan as a job (its result is a NumberingApplied). A plan that
  // changed since it was shown is refused with a 409 and nothing moves.
  applySeriesNumbering: (id: number, plan_hash: string) =>
    req<JobRef>(`/api/v1/series/${id}/numbering/apply`, { method: "POST", body: JSON.stringify({ plan_hash }) }),
  dismissSeriesNumbering: (id: number) =>
    req<void>(`/api/v1/series/${id}/numbering/pending`, { method: "DELETE" }),
  // The show's searching switch (the monitored field): season and episode choices are kept
  // either way.
  setSeriesMonitored: (id: number, monitored: boolean) =>
    req<MonitorReply>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  // Applies a monitoring preset to the show's episodes and turns its searching on (resumed
  // says it was off), in one request.
  applySeriesMonitorPreset: (id: number, preset: string) =>
    req<MonitorReply>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ preset }) }),
  setSeriesMonitorNewSeasons: (id: number, monitor_new_seasons: boolean) =>
    req<{ monitored: boolean; monitor_new_seasons: boolean }>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitor_new_seasons }) }),
  setSeriesProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string }>(`/api/v1/series/${id}/profile`, { method: "PUT", body: JSON.stringify({ quality_profile }) }),
  setSeriesType: (id: number, series_type: string) =>
    req<{ series_type: string }>(`/api/v1/series/${id}/type`, { method: "PUT", body: JSON.stringify({ series_type }) }),
  // Manual scene-season mapping (anime whose broadcast cours don't match TMDB numbering).
  sceneOverrides: (id: number) =>
    req<{ overrides: SceneOverride[] }>(`/api/v1/series/${id}/scene-map`),
  setSceneOverride: (id: number, o: SceneOverride) =>
    req<SceneOverride>(`/api/v1/series/${id}/scene-map`, { method: "PUT", body: JSON.stringify(o) }),
  deleteSceneOverride: (id: number, sceneSeason: number) =>
    req<void>(`/api/v1/series/${id}/scene-map/${sceneSeason}`, { method: "DELETE" }),
  setSeasonMonitored: (id: number, season: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/${id}/seasons/${season}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setEpisodeMonitored: (eid: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/episodes/${eid}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  // confirm is the series title, required by the server when deleting files over its size
  // threshold. A 409 (ApiError) carries body.moved / body.failed.
  deleteSeries: (id: number, deleteFiles?: boolean, confirm?: string) => {
    const q = new URLSearchParams();
    if (deleteFiles) q.set("delete_files", "true");
    if (deleteFiles && confirm) q.set("confirm", confirm);
    const qs = q.toString();
    return req<void>(`/api/v1/series/${id}${qs ? `?${qs}` : ""}`, { method: "DELETE" });
  },
  // seasons narrows the preview to a bulk season delete; omitted, it's the whole show.
  seriesDeletePreview: (id: number, seasons?: number[]) =>
    req<DeletePreview>(`/api/v1/series/${id}/delete-preview${seasons?.length ? `?seasons=${seasons.join(",")}` : ""}`),
  seriesBlocklist: (id: number) => req<{ blocklist: BlockEntry[] }>(`/api/v1/series/${id}/blocklist`).then((r) => r.blocklist),
  unblockSeries: (id: number, bid: number) => req<void>(`/api/v1/series/${id}/blocklist/${bid}`, { method: "DELETE" }),
  regrabEpisode: (id: number, season: number, episode: number) =>
    req<{ status: string } & JobRef>(`/api/v1/series/${id}/seasons/${season}/episodes/${episode}/regrab`, { method: "POST" }),
  deleteEpisodeFile: (id: number, season: number, episode: number) =>
    req<void>(`/api/v1/series/${id}/seasons/${season}/episodes/${episode}/file`, { method: "DELETE" }),
  bulkDeleteSeasons: (id: number, seasons: number[], deleteFiles: boolean, confirm?: string) =>
    req<BulkDeleteSeasonsReply>(`/api/v1/series/${id}/seasons`, {
      method: "DELETE",
      body: JSON.stringify({ seasons, delete_files: deleteFiles, confirm }),
    }),

  // Books
};
