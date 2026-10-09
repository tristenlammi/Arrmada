import { useEffect, useMemo, useState } from "react";
import { api, type ImportReview, type ReviewFile, type ReviewTarget } from "../../lib/api";
import { formatBytes } from "../../lib/format";
import { Button, Modal } from "../../ui";
import { guessLabel, parseEpisodes, prefill } from "./episodes";

interface Row { file: ReviewFile; season: string; episodes: string }

// MapFilesModal imports a held show download whose episode numbering couldn't be read, by
// saying which episode each file is. Rows start from what each filename says; blank rows
// are skipped. The quality check doesn't apply — these are files you chose.
export function MapFilesModal({ review, onClose, onDone }: {
  review: ImportReview;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [target, setTarget] = useState<{ id: number; title: string } | null>(
    review.expected_id > 0 ? { id: review.expected_id, title: review.expected_title || "the show it was grabbed for" } : null,
  );
  const [picking, setPicking] = useState(review.expected_id <= 0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api.reviewFiles(review.id)
      .then((r) => {
        if (!live) return;
        setRows(r.files.filter((f) => f.video).map((f) => ({ file: f, ...prefill(f) })));
        setTruncated(r.truncated);
      })
      .catch((e) => { if (live) setLoadError((e as Error).message); });
    return () => { live = false; };
  }, [review.id]);

  // Which rows will be imported, and whether any filled-in row can't be read.
  const { mappings, invalid } = useMemo(() => {
    const out: { rel_path: string; season: number; episodes: number[] }[] = [];
    let bad = 0;
    for (const r of rows ?? []) {
      const eps = parseEpisodes(r.episodes);
      if (eps === null && r.season.trim() === "") continue; // blank row: skipped
      const season = Number(r.season);
      if (eps == null || r.season.trim() === "" || !Number.isInteger(season) || season < 0) { bad++; continue; }
      out.push({ rel_path: r.file.rel_path, season, episodes: eps });
    }
    return { mappings: out, invalid: bad };
  }, [rows]);

  const set = (i: number, patch: Partial<Row>) => setRows((rs) => (rs ?? []).map((r, j) => (j === i ? { ...r, ...patch } : r)));

  const submit = async () => {
    if (!target || mappings.length === 0 || invalid > 0) return;
    setBusy(true); setError(null);
    try {
      const res = await api.mapReview(review.id, target.id, mappings);
      onDone(res.job_id
        ? `Importing ${mappings.length} files into ${target.title} in the background — the review clears when it's done.`
        : `Imported ${res.placed ?? 0} episode${res.placed === 1 ? "" : "s"} into ${target.title}.`);
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };

  const n = mappings.length;
  return (
    <Modal
      onClose={onClose}
      dismissible={!busy}
      size="xl"
      title="Map files to episodes"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
          <Button variant="primary" onClick={submit} busy={busy} busyLabel="Importing…" disabled={!target || n === 0 || invalid > 0}>
            {`Import ${n} file${n === 1 ? "" : "s"}`}
          </Button>
        </>
      }
    >
      <p className="mb-3 mt-0 text-[11.5px] text-ink-dim">
        Say which episode each file of <span className="break-all font-mono">{review.name}</span> is. Leave a row blank to skip it;
        a file holding two episodes takes both (for example <span className="font-mono">3,4</span>). The quality check is skipped —
        an episode that already has a file gets this one instead, and the old file goes to the recycle bin.
      </p>

      <TargetPicker review={review} target={target} picking={picking} onPicking={setPicking} onPick={(t) => { setTarget(t); setPicking(false); }} />

      {loadError ? (
        <div className="p-4 text-[12px]" style={{ color: "var(--reject)" }}>{loadError}</div>
      ) : rows === null ? (
        <div className="p-4 text-[12px] text-ink-faint">Listing files…</div>
      ) : rows.length === 0 ? (
        <div className="p-4 text-[12px] text-ink-faint">No video files in this download.</div>
      ) : (
        <div className="thin-scroll max-h-[50vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          <div className="sticky top-0 grid grid-cols-[1fr_72px_60px_90px] gap-2 px-3 py-1.5 text-[10px] font-bold uppercase text-ink-faint" style={{ background: "var(--panel-2)" }}>
            <span>File</span><span className="text-right">Size</span><span>Season</span><span>Episode(s)</span>
          </div>
          {rows.map((r, i) => {
            const eps = parseEpisodes(r.episodes);
            const bad = (r.episodes.trim() !== "" || r.season.trim() !== "") && (eps == null || r.season.trim() === "");
            return (
              <div key={r.file.rel_path} className="grid grid-cols-[1fr_72px_60px_90px] items-center gap-2 px-3 py-1.5 font-mono text-[10.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
                <span className="min-w-0 truncate" title={`${r.file.rel_path} — ${guessLabel(r.file)}`}>{r.file.rel_path}</span>
                <span className="text-right text-ink-dim">{formatBytes(r.file.size)}</span>
                <input aria-label={`Season for ${r.file.rel_path}`} inputMode="numeric" value={r.season} onChange={(e) => set(i, { season: e.target.value })}
                  className="w-full rounded px-1.5 py-1 text-[11px]" style={{ background: "var(--panel-2)", border: `1px solid ${bad ? "var(--reject)" : "var(--line)"}`, color: "var(--ink)" }} />
                <input aria-label={`Episodes for ${r.file.rel_path}`} value={r.episodes} placeholder="—" onChange={(e) => set(i, { episodes: e.target.value })}
                  className="w-full rounded px-1.5 py-1 text-[11px]" style={{ background: "var(--panel-2)", border: `1px solid ${bad ? "var(--reject)" : "var(--line)"}`, color: "var(--ink)" }} />
              </div>
            );
          })}
        </div>
      )}
      {truncated && <p className="mb-0 mt-2 text-[11px] text-ink-faint">Only the first files of this download are listed.</p>}
      {invalid > 0 && <p className="mb-0 mt-2 text-[11.5px]" style={{ color: "var(--reject)" }}>{invalid} row{invalid === 1 ? " needs" : "s need"} a season and episode numbers like 3, 3,4 or 3-4.</p>}
      {error && <p className="mb-0 mt-2 text-[11.5px]" style={{ color: "var(--reject)" }}>{error}</p>}
    </Modal>
  );
}

// TargetPicker shows which show the files go into, defaulting to the one it was grabbed
// for, and lets you pick another from the library.
function TargetPicker({ review, target, picking, onPicking, onPick }: {
  review: ImportReview;
  target: { id: number; title: string } | null;
  picking: boolean;
  onPicking: (v: boolean) => void;
  onPick: (t: { id: number; title: string }) => void;
}) {
  const [q, setQ] = useState("");
  const [items, setItems] = useState<ReviewTarget[] | null>(null);
  useEffect(() => {
    if (!picking) return;
    let live = true;
    const t = window.setTimeout(() => {
      api.reviewTargets(review.id, q.trim())
        .then((r) => { if (live) setItems(r.targets); })
        .catch(() => { if (live) setItems([]); });
    }, q ? 250 : 0);
    return () => { live = false; window.clearTimeout(t); };
  }, [review.id, q, picking]);

  return (
    <div className="mb-3">
      <div className="flex items-center gap-2 text-[12px]">
        <span className="text-ink-dim">Into:</span>
        <b>{target ? target.title : "choose a show"}</b>
        {!picking && <button onClick={() => onPicking(true)} className="text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>Change…</button>}
      </div>
      {picking && (
        <div className="mt-2">
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter your shows…" aria-label="Filter your shows"
            className="mb-2 w-full rounded-lg px-3 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <div className="thin-scroll max-h-[160px] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
            {items === null ? (
              <div className="p-3 text-[11.5px] text-ink-faint">Loading…</div>
            ) : items.length === 0 ? (
              <div className="p-3 text-[11.5px] text-ink-faint">No matching shows.</div>
            ) : items.map((i) => (
              <button key={i.id} onClick={() => onPick({ id: i.id, title: i.title })} className="block w-full px-3 py-1.5 text-left text-[12px] hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
                {i.title}{i.year ? ` (${i.year})` : ""}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
