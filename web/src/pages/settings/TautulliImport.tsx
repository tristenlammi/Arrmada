import { useState } from "react";
import { Field, Section, input, inputStyle } from "../../components/settings/ui";
import { api } from "../../lib/api";
import { invalidate, useQuery } from "../../lib/query";
import { jobFailed, jobToast, useJob } from "../../lib/useJob";
import { Button, useConfirm, useToast } from "../../ui";

const OVERLAPS_KEY = "insights-import-overlaps";

function day(epochSecs: number): string {
  return new Date(epochSecs * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// TautulliImport backfills Insights with an existing Tautulli watch history so stats aren't
// blank, and repairs plays an earlier import counted twice.
export function TautulliImport() {
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [onlyBefore, setOnlyBefore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const overlaps = useQuery(OVERLAPS_KEY, api.importOverlaps, { staleMs: 0 });
  const firstLive = overlaps.data?.first_live_at ?? 0;

  const run = async () => {
    if (!url.trim() || !key.trim()) return;
    setBusy(true);
    setMsg(null);
    try {
      await api.importTautulli(url.trim(), key.trim(), onlyBefore && firstLive > 0 ? firstLive : undefined);
      setMsg({ ok: true, text: "Connected — importing your watch history in the background. It'll fill in the Insights graphs as it processes (large histories take a few minutes)." });
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section id="tautulli-import" title="Import from Tautulli" subtitle="Backfill Insights with your existing Tautulli watch history so your stats and graphs aren't empty on day one. Sessions are attributed to their Plex account (matching Sign in with Plex). Plays Arrmada already recorded live are skipped; re-running only adds what's missing.">
      <Field label="Tautulli URL">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.50.247:8181" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <Field label="API key">
        <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder="from Tautulli → Settings → Web Interface → API Key" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      {firstLive > 0 && (
        <label className="flex items-center gap-2 text-[12px]">
          <input type="checkbox" checked={onlyBefore} onChange={(e) => setOnlyBefore(e.target.checked)} />
          Only import plays from before {day(firstLive)}, when Arrmada started recording
        </label>
      )}
      <div className="flex items-center gap-3">
        <Button variant="primary" onClick={run} disabled={!url.trim() || !key.trim()} busy={busy} busyLabel="Connecting…">
          Import history
        </Button>
        {msg && <span className="text-[11.5px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</span>}
      </div>
      {!!overlaps.data?.count && <DoubleCounts count={overlaps.data.count} firstLive={firstLive} />}
    </Section>
  );
}

// DoubleCounts offers to remove imported plays that repeat plays Arrmada recorded live —
// left by imports from before the importer skipped those. The server backs the database
// up first and deletes nothing if the count changed since this was shown.
function DoubleCounts({ count, firstLive }: { count: number; firstLive: number }) {
  const confirm = useConfirm();
  const toast = useToast();
  const [jobId, setJobId] = useState<number | null>(null);
  const [starting, setStarting] = useState(false);
  const job = useJob(jobId, {
    onDone: (j) => {
      toast(jobToast(j, "Removed."), { tone: jobFailed(j) ? "error" : "good" });
      setJobId(null);
      invalidate("insights"); // the count here and every Insights total
    },
  });
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
      if (r.job_id) setJobId(r.job_id);
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
