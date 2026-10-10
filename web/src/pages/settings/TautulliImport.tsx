import { useState } from "react";
import { Field, Section, input, inputStyle } from "../../components/settings/ui";
import { api, type ImportRun } from "../../lib/api";
import { invalidate, useQuery } from "../../lib/query";
import { jobFailed, jobToast, useJob } from "../../lib/useJob";
import { usePoll } from "../../lib/usePoll";
import { Button, StatusChip, useConfirm, useToast } from "../../ui";
import { RUN_STATUS, canRetry, runProgress, runSummary } from "./importRuns";

// Every query here starts with "insights", so invalidate("insights") after an import, an
// undo or a repair refreshes this card and every Insights total at once.
const CONFIG_KEY = "insights-import-tautulli";
const RUNS_KEY = "insights-import-runs";
const OVERLAPS_KEY = "insights-import-overlaps";
const RUN_POLL_MS = 2000;

function day(epochSecs: number): string {
  return new Date(epochSecs * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}
function when(epochSecs: number): string {
  return new Date(epochSecs * 1000).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

// useFollowedJob follows one background job (an import, a retry, an undo or a repair) and
// refreshes everything Insights shows when it ends.
function useFollowedJob(fallback: string) {
  const toast = useToast();
  const [jobId, setJobId] = useState<number | null>(null);
  const job = useJob(jobId, {
    onDone: (j) => {
      setJobId(null);
      invalidate("insights");
      toast(jobToast(j, fallback), { tone: jobFailed(j) ? "error" : "good" });
    },
  });
  return { follow: (id?: number) => { if (id) setJobId(id); }, job };
}

// TautulliImport backfills Insights with an existing Tautulli watch history so stats aren't
// blank: each import is a tracked run with progress, a summary, Retry and undo. It also
// repairs plays an earlier import counted twice.
export function TautulliImport() {
  const toast = useToast();
  const confirm = useConfirm();
  const cfg = useQuery(CONFIG_KEY, api.tautulliConfig, { staleMs: 0 });
  const runs = useQuery(RUNS_KEY, api.importRuns, { staleMs: 0 });
  const overlaps = useQuery(OVERLAPS_KEY, api.importOverlaps, { staleMs: 0 });
  const { follow, job } = useFollowedJob("Done.");
  const [urlEdit, setUrl] = useState<string | null>(null); // null: show the saved URL
  const [key, setKey] = useState("");
  const [onlyBefore, setOnlyBefore] = useState(false);
  const [starting, setStarting] = useState(false);

  const savedURL = cfg.data?.url ?? "";
  const url = urlEdit ?? savedURL;
  // The saved key only goes with the saved address; a new one needs its key typed.
  const keySaved = !!cfg.data?.api_key_set && url.trim() === savedURL;
  const firstLive = overlaps.data?.first_live_at ?? 0;
  const list = runs.data ?? [];
  const running = job.running || list.some((r) => r.status === "running");
  usePoll(() => runs.refetch(), running ? RUN_POLL_MS : null, { immediate: false });

  const start = async (call: () => Promise<{ job_id?: number }>): Promise<boolean> => {
    setStarting(true);
    try {
      const r = await call();
      follow(r.job_id);
      void runs.refetch();
      return true;
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
      return false;
    } finally {
      setStarting(false);
    }
  };

  const run = async () => {
    if (!url.trim() || (!key.trim() && !keySaved)) return;
    const ok = await start(() => api.importTautulli(url.trim(), key.trim(), onlyBefore && firstLive > 0 ? firstLive : undefined));
    if (ok) {
      // The server saved the connection: show it as saved rather than as typed.
      setKey("");
      setUrl(null);
      invalidate(CONFIG_KEY);
    }
  };

  const remove = async (r: ImportRun) => {
    const plays = `${r.rows.toLocaleString()} play${r.rows === 1 ? "" : "s"}`;
    const ok = await confirm({
      title: `Remove this import's ${plays}?`,
      body: (
        <>
          Deletes the {plays} the import from {when(r.started_at)} brought in, and nothing else: plays
          Arrmada recorded live and plays from other imports stay.
          <p className="mb-0 mt-2">A database backup is taken first (Settings → System → Backups), so this can be undone by restoring it.</p>
        </>
      ),
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (ok) void start(() => api.removeImportRun(r.id, r.rows));
  };

  const busy = starting || job.running;
  return (
    <Section id="tautulli-import" title="Import from Tautulli" subtitle="Backfill Insights with your existing Tautulli watch history so your stats and graphs aren't empty on day one. Sessions are attributed to their Plex account (matching Sign in with Plex). Plays Arrmada already recorded live are skipped; re-running only adds what's missing.">
      <Field label="Tautulli URL">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.50.247:8181" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <Field label="API key">
        <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder={keySaved ? "saved — leave blank to keep it" : "from Tautulli → Settings → Web Interface → API Key"} className={input} style={inputStyle} autoComplete="off" />
      </Field>
      {firstLive > 0 && (
        <label className="flex items-center gap-2 text-[12px]">
          <input type="checkbox" checked={onlyBefore} onChange={(e) => setOnlyBefore(e.target.checked)} />
          Only import plays from before {day(firstLive)}, when Arrmada started recording
        </label>
      )}
      <div className="flex items-center gap-3">
        <Button variant="primary" onClick={run} disabled={job.running || !url.trim() || (!key.trim() && !keySaved)} busy={starting} busyLabel="Connecting…">
          Import history
        </Button>
        {job.running && job.message && <span className="text-[11.5px] text-ink-faint">{job.message}</span>}
      </div>
      {list.length > 0 && (
        <div className="flex flex-col gap-2">
          {list.map((r) => (
            <RunRow key={r.id} run={r} busy={busy} onRetry={() => void start(() => api.retryImportRun(r.id))} onRemove={() => void remove(r)} />
          ))}
        </div>
      )}
      {!!overlaps.data?.count && <DoubleCounts count={overlaps.data.count} firstLive={firstLive} />}
    </Section>
  );
}

function RunRow({ run: r, busy, onRetry, onRemove }: { run: ImportRun; busy: boolean; onRetry: () => void; onRemove: () => void }) {
  const st = RUN_STATUS[r.status] ?? { label: r.status, tone: "faint" as const };
  const pct = Math.round(runProgress(r) * 100);
  return (
    <div className="flex flex-col gap-1.5 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[12px] font-semibold">Import · {when(r.started_at)}</span>
        <StatusChip tone={st.tone}>{st.label}</StatusChip>
        {r.cutoff_at > 0 && <span className="text-[11px] text-ink-faint">plays before {day(r.cutoff_at)}</span>}
        <span className="flex-1" />
        {canRetry(r) && <Button size="sm" onClick={onRetry} disabled={busy}>Retry</Button>}
        {r.rows > 0 && r.status !== "running" && <Button size="sm" variant="danger" onClick={onRemove} disabled={busy}>Remove this import</Button>}
      </div>
      {r.status === "running" && (
        <div className="flex items-center gap-2">
          <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }} role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="Import progress">
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: "var(--accent)" }} />
          </div>
          <span className="font-mono text-[11px] tabular-nums text-ink-faint">{r.total > 0 ? `${r.processed.toLocaleString()} / ${r.total.toLocaleString()}` : "starting…"}</span>
        </div>
      )}
      <span className="text-[11.5px] text-ink-dim">{runSummary(r)}</span>
      {r.error && <span className="text-[11.5px]" style={{ color: "var(--reject)" }}>{r.error}</span>}
      {r.removed_at > 0 && <span className="text-[11.5px] text-ink-faint">Removed {r.removed_rows.toLocaleString()} of its plays on {day(r.removed_at)}.</span>}
    </div>
  );
}

// DoubleCounts offers to remove imported plays that repeat plays Arrmada recorded live —
// left by imports from before the importer skipped those. The server backs the database
// up first and deletes nothing if the count changed since this was shown.
function DoubleCounts({ count, firstLive }: { count: number; firstLive: number }) {
  const confirm = useConfirm();
  const toast = useToast();
  const { follow, job } = useFollowedJob("Removed.");
  const [starting, setStarting] = useState(false);
  const plays = `${count.toLocaleString()} double-counted play${count === 1 ? "" : "s"}`;

  const remove = async () => {
    const ok = await confirm({
      title: `Remove ${plays}?`,
      body: (
        <>
          These plays came in from Tautulli but Arrmada had already recorded them live
          {firstLive > 0 ? ` (it has been recording since ${day(firstLive)})` : ""}, so each one is counted twice.
          Only the imported copies are removed; plays recorded live and plays only Tautulli saw stay.
          <p className="mb-0 mt-2">A database backup is taken first (Settings → System → Backups), so this can be undone by restoring it.</p>
        </>
      ),
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setStarting(true);
    try {
      const r = await api.removeImportOverlaps(count);
      follow(r.job_id);
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setStarting(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      <span className="flex-1 text-[12px]">
        An earlier import counted {plays} twice: they were also recorded live.
      </span>
      <Button variant="danger" size="sm" onClick={remove} busy={starting || job.running} busyLabel={job.message || "Removing…"}>
        Remove {plays}
      </Button>
    </div>
  );
}
