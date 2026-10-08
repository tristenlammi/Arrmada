import { useEffect, useMemo, useState } from "react";
import { api, type RenameSkip, type SeriesRenameItem } from "../../lib/api";

const base = (p: string) => p.slice(Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\")) + 1);
const seasonName = (n: number) => (n === 0 ? "Specials" : `Season ${n}`);

// RenameModal shows every file a rename would move before anything moves. The button used
// to fetch this list and then rename straight away, so a renumber's surprises were only
// discovered on disk. Confirm sends back exactly the previewed moves; anything that changed
// in between is skipped by the server and listed here, never applied blind.
export function RenameModal({ seriesId, title, onClose, onRenamed }: { seriesId: number; title: string; onClose: () => void; onRenamed: () => void }) {
  const [items, setItems] = useState<SeriesRenameItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ renamed: number; skipped: RenameSkip[] } | null>(null);

  useEffect(() => {
    let alive = true;
    api.seriesRenamePreview(seriesId)
      .then((r) => { if (alive) setItems(r.items ?? []); })
      .catch((e: Error) => { if (alive) setError(e.message); });
    return () => { alive = false; };
  }, [seriesId]);

  const moves = useMemo(() => (items ?? []).filter((it) => !it.conflict), [items]);
  const conflicts = (items ?? []).length - moves.length;
  const bySeason = useMemo(() => {
    const m = new Map<number, SeriesRenameItem[]>();
    for (const it of items ?? []) m.set(it.season, [...(m.get(it.season) ?? []), it]);
    // Specials last, as on the series page.
    return [...m.entries()].sort(([a], [b]) => (a === 0 ? 1 : b === 0 ? -1 : a - b));
  }, [items]);

  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.renameSeries(seriesId, moves);
      setResult({ renamed: res.renamed, skipped: res.skipped ?? [] });
      onRenamed();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[760px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Rename files</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">
          Episode files of <b>{title}</b> that aren't named to the library scheme yet. Nothing moves until you confirm, and no existing file is ever replaced.
        </p>
        {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}

        {result ? (
          <div>
            <div className="mb-3 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)", color: "var(--accent)" }}>
              Renamed {result.renamed} file{result.renamed === 1 ? "" : "s"}{result.skipped.length > 0 ? ` · skipped ${result.skipped.length}` : ""}.
            </div>
            {result.skipped.length > 0 && (
              <div className="thin-scroll max-h-[40vh] overflow-y-auto">
                {result.skipped.map((sk) => (
                  <div key={sk.from} className="py-1.5" style={{ borderBottom: "1px solid var(--line-soft)" }}>
                    <div className="truncate font-mono text-[11px]" title={sk.from}>{base(sk.from)}</div>
                    <div className="mt-0.5 text-[11px]" style={{ color: "var(--reject)" }}>{sk.reason}</div>
                  </div>
                ))}
              </div>
            )}
            <div className="mt-4 flex justify-end">
              <button onClick={onClose} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Close</button>
            </div>
          </div>
        ) : items === null ? (
          !error && <div className="p-6 text-center text-[12.5px] text-ink-dim">Working out the new names…</div>
        ) : items.length === 0 ? (
          <div className="p-6 text-center text-[12.5px] text-ink-dim">Already named correctly.</div>
        ) : (
          <>
            <div className="mb-2 font-mono text-[11px] text-ink-faint">
              {moves.length} file{moves.length === 1 ? "" : "s"} to rename
              {conflicts > 0 && <span style={{ color: "var(--reject)" }}> · {conflicts} can't be renamed</span>}
            </div>
            <div className="thin-scroll max-h-[52vh] overflow-y-auto">
              {bySeason.map(([season, list]) => (
                <div key={season} className="mb-3">
                  <div className="mb-1 text-[11px] font-semibold text-ink-dim">{seasonName(season)}</div>
                  {list.map((it) => (
                    <div key={it.from} className="py-1.5" style={{ borderBottom: "1px solid var(--line-soft)" }}>
                      <div className="truncate font-mono text-[11px] text-ink-faint" title={it.from}>{base(it.from)}</div>
                      <div className="truncate font-mono text-[11px]" title={it.to} style={{ color: it.conflict ? "var(--reject)" : "var(--ink)" }}>→ {base(it.to)}</div>
                      {it.conflict && <div className="mt-0.5 text-[11px]" style={{ color: "var(--reject)" }}>Not renamed: {it.conflict}</div>}
                    </div>
                  ))}
                </div>
              ))}
            </div>
            <div className="mt-4 flex justify-end gap-2">
              <button onClick={onClose} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Cancel</button>
              <button
                onClick={confirm}
                disabled={busy || moves.length === 0}
                className="rounded-lg px-3 py-2 text-[12.5px] font-semibold disabled:opacity-50"
                style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
              >
                {busy ? "Renaming…" : `Rename ${moves.length} file${moves.length === 1 ? "" : "s"}`}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
