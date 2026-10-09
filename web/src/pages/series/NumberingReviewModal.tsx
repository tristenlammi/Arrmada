import { useEffect, useRef, useState } from "react";
import { api, type NumberingApplied, type NumberingPending } from "../../lib/api";
import { useQuery } from "../../lib/query";
import { jobFailed, useJob } from "../../lib/useJob";
import { Button, Modal } from "../../ui";

const SOURCE: Record<string, string> = { tvdb: "TVDB", tvmaze: "TVmaze", tmdb: "TMDB" };
const sourceName = (s: string) => SOURCE[s] ?? "an unrecorded source";
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

// NumberingBanner says when another source numbers this show differently. A refresh never
// moves files for that on its own; the owner reviews the moves here and applies or
// dismisses them. Checked again whenever the show was refreshed.
export function NumberingBanner({ seriesId, refreshKey, onApplied }: { seriesId: number; refreshKey?: string; onApplied: () => void }) {
  const q = useQuery(`series-numbering:${seriesId}`, () => api.seriesNumbering(seriesId), { staleMs: 0 });
  const { refetch } = q;
  // The mount's own fetch covers the first render; after that, every refresh re-checks.
  const mounted = useRef(false);
  useEffect(() => {
    if (!mounted.current) { mounted.current = true; return; }
    void refetch();
  }, [refreshKey, refetch]);
  const [open, setOpen] = useState(false);
  const pending = q.data?.pending;
  if (!pending) return null;
  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3 rounded-lg px-3 py-2.5 text-[12.5px]" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)" }}>
        <span className="min-w-0 flex-1" style={{ color: "var(--ink)" }}>
          Numbering from {sourceName(pending.to)} differs — {plural(pending.files, "file")} would move. New episodes aren't added until it's applied or dismissed.
        </span>
        <Button size="sm" variant="primary" onClick={() => setOpen(true)}>Review</Button>
      </div>
      {open && (
        <NumberingReviewModal
          seriesId={seriesId}
          pending={pending}
          onClose={() => setOpen(false)}
          onSettled={() => { void refetch(); onApplied(); }}
        />
      )}
    </>
  );
}

// NumberingReviewModal lists every file the renumber would move — old episode, new
// episode, file — before anything moves. Apply sends back the plan it showed; if the
// metadata has changed since, the server refuses and nothing moves.
export function NumberingReviewModal({ seriesId, pending, onClose, onSettled }: { seriesId: number; pending: NumberingPending; onClose: () => void; onSettled: () => void }) {
  const [busy, setBusy] = useState<"apply" | "dismiss" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [jobId, setJobId] = useState<number | null>(null);
  const [result, setResult] = useState<NumberingApplied | null>(null);
  useJob(jobId, {
    onDone: (j) => {
      setJobId(null);
      setBusy(null);
      if (jobFailed(j)) setError(j.error || "That didn't work — nothing was moved.");
      else setResult((j.result as NumberingApplied | undefined) ?? { moved: 0, skipped: [] });
      onSettled();
    },
  });

  const apply = async () => {
    setBusy("apply");
    setError(null);
    try {
      const r = await api.applySeriesNumbering(seriesId, pending.plan_hash);
      if (r.job_id) setJobId(r.job_id);
      else { setBusy(null); onSettled(); onClose(); }
    } catch (e) {
      setBusy(null);
      setError((e as Error).message);
      onSettled(); // a stale plan: show the current one
    }
  };
  const dismiss = async () => {
    setBusy("dismiss");
    setError(null);
    try {
      await api.dismissSeriesNumbering(seriesId);
      onSettled();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const unplaced = pending.remaps.filter((r) => !r.new).length;
  const footer = result ? (
    <Button onClick={onClose}>Close</Button>
  ) : (
    <>
      <Button variant="ghost" onClick={onClose} disabled={busy !== null}>Cancel</Button>
      <Button onClick={dismiss} busy={busy === "dismiss"} busyLabel="Dismissing…" disabled={busy !== null}>Dismiss</Button>
      <Button variant="primary" onClick={apply} busy={busy === "apply"} busyLabel="Applying…" disabled={busy !== null}>Apply</Button>
    </>
  );

  return (
    <Modal onClose={onClose} title="Numbering change" size="lg" dismissible={busy === null} footer={footer}>
      <p className="mb-3 text-[12px] text-ink-dim">
        {sourceName(pending.to)} numbers this show differently from what's stored ({sourceName(pending.from)}). Apply moves each file to its new episode and renames it — no existing file is ever replaced. Dismiss keeps everything as it is, and this same change won't be offered again.
      </p>
      {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
      {result ? (
        <div>
          <div className="mb-3 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)", color: "var(--accent)" }}>
            Applied. Renamed {plural(result.moved, "file")}{result.skipped.length > 0 ? ` · skipped ${result.skipped.length}` : ""}{result.unplaced ? ` · ${plural(result.unplaced, "file")} with no episode left where they are` : ""}.
          </div>
          {(result.skipped.length > 0 || !!result.unplaced) && (
            <p className="mb-2 text-[11.5px] text-ink-dim">The rescan was held back: it reads episodes from file names, and those files still carry their old ones. Use Rename to finish them first.</p>
          )}
          {result.skipped.length > 0 && (
            <div className="thin-scroll max-h-[40vh] overflow-y-auto">
              {result.skipped.map((sk) => (
                <div key={sk.from} className="py-1.5" style={{ borderBottom: "1px solid var(--line-soft)" }}>
                  <div className="truncate font-mono text-[11px]" title={sk.from}>{sk.from.slice(Math.max(sk.from.lastIndexOf("/"), sk.from.lastIndexOf("\\")) + 1)}</div>
                  <div className="mt-0.5 text-[11px]" style={{ color: "var(--reject)" }}>{sk.reason}</div>
                </div>
              ))}
            </div>
          )}
        </div>
      ) : (
        <>
          <div className="mb-2 font-mono text-[11px] text-ink-faint">
            {plural(pending.files, "file")} would move
            {unplaced > 0 && <span style={{ color: "var(--reject)" }}> · {unplaced} with no episode in the new numbering</span>}
          </div>
          <div className="thin-scroll max-h-[52vh] overflow-y-auto">
            <table className="w-full text-left text-[11.5px]">
              <thead>
                <tr className="text-ink-faint">
                  <th className="py-1 pr-3 font-semibold">Now</th>
                  <th className="py-1 pr-3 font-semibold">Becomes</th>
                  <th className="py-1 font-semibold">File</th>
                </tr>
              </thead>
              <tbody>
                {pending.remaps.map((r, i) => (
                  <tr key={`${r.old}:${r.file}:${i}`} style={{ borderTop: "1px solid var(--line-soft)" }}>
                    <td className="py-1.5 pr-3 font-mono">{r.old}</td>
                    <td className="py-1.5 pr-3 font-mono" style={{ color: r.new ? "var(--ink)" : "var(--reject)" }}>
                      {r.new || "no episode"}
                    </td>
                    <td className="max-w-[360px] truncate py-1.5 font-mono text-ink-dim" title={r.file}>
                      {r.file}
                      {!r.new && <div className="text-[10.5px]" style={{ color: "var(--reject)" }}>Stays where it is; the next rescan decides what it is.</div>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </Modal>
  );
}
