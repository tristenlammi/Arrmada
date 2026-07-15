import { useEffect, useMemo, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type ImportReview, type Movie, type Series } from "../lib/api";

// Reviews — downloads Arrmada grabbed but held back because their content doesn't
// match what they were grabbed for. The admin reviews each: reject, import anyway,
// import into a different library item, or dismiss.
export function Reviews() {
  const [list, setList] = useState<ImportReview[] | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [reassign, setReassign] = useState<ImportReview | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  const refresh = () => api.reviews().then(setList).catch(() => setList([]));
  useEffect(() => { refresh(); }, []);

  const act = async (id: number, fn: () => Promise<unknown>, msg: string) => {
    setBusy(id);
    try { await fn(); setList((xs) => (xs ?? []).filter((r) => r.id !== id)); flash(msg); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(null); }
  };

  return (
    <>
      <PageHeader title="Review" crumb="Activity / Review" />
      <div className="mx-auto w-full max-w-[960px] px-4 py-6 sm:px-6">
        <p className="mb-5 max-w-[70ch] text-[12.5px] text-ink-dim">
          Downloads that finished but whose content didn't match what they were grabbed for are held here instead of
          being imported. Reject to remove + blocklist them, import anyway if it's a false alarm, import into a
          different show/movie, or dismiss to handle it yourself.
        </p>

        {list === null ? (
          <div className="p-10 text-center text-[12.5px] text-ink-dim">Loading…</div>
        ) : list.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            Nothing to review — every download matched what it was grabbed for. 🎉
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            {list.map((r) => (
              <div key={r.id} className="rounded-xl p-4" style={{ border: "1px solid var(--avoid)", background: "var(--panel)" }}>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{r.media_type}</span>
                      <span className="rounded px-1.5 py-0.5 text-[9.5px] font-bold uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }}>Held</span>
                      <span className="truncate font-mono text-[12px]">{r.name}</span>
                    </div>
                    <div className="mt-2 text-[12.5px]" style={{ color: "var(--avoid)" }}>{r.reason}</div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[10.5px] text-ink-faint">
                      <span>Grabbed for: <b className="text-ink-dim">{r.expected_title}</b></span>
                      <span>Looks like: <b className="text-ink-dim">{r.parsed_title || "?"}</b></span>
                      {r.size_bytes > 0 && <span>{gb(r.size_bytes)}</span>}
                      {r.indexer && <span>{r.indexer}</span>}
                    </div>
                  </div>
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2">
                  <button onClick={() => act(r.id, () => api.rejectReview(r.id), "Rejected + blocklisted.")} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>Reject</button>
                  <button onClick={() => act(r.id, () => api.importReview(r.id), `Imported into ${r.expected_title}.`)} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Import anyway</button>
                  <button onClick={() => setReassign(r)} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Import into a different {r.media_type === "series" ? "show" : "movie"}…</button>
                  <button onClick={() => act(r.id, () => api.dismissReview(r.id), "Dismissed.")} disabled={busy === r.id} className="ml-auto rounded-lg px-3 py-1.5 text-[11.5px] text-ink-dim hover:text-[var(--ink)]">Dismiss</button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
      {reassign && (
        <ReassignModal
          review={reassign}
          onClose={() => setReassign(null)}
          onPicked={(targetId, label) => {
            const id = reassign.id;
            setReassign(null);
            act(id, () => api.importReview(id, targetId), `Imported into ${label}.`);
          }}
        />
      )}
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

function gb(bytes: number): string {
  const g = bytes / 1024 ** 3;
  return g >= 1 ? `${g.toFixed(2)} GB` : `${(bytes / 1024 ** 2).toFixed(0)} MB`;
}

// ReassignModal lets the admin pick an existing library item (of the review's
// media type) to import the held content into.
function ReassignModal({ review, onClose, onPicked }: { review: ImportReview; onClose: () => void; onPicked: (targetId: number, label: string) => void }) {
  const [items, setItems] = useState<{ id: number; title: string; year: number }[] | null>(null);
  const [q, setQ] = useState("");

  useEffect(() => {
    if (review.media_type === "series") {
      api.series().then((r) => setItems(r.series.map((s: Series) => ({ id: s.id, title: s.title, year: s.year })))).catch(() => setItems([]));
    } else {
      api.movies().then((r) => setItems(r.movies.map((m: Movie) => ({ id: m.id, title: m.title, year: m.year })))).catch(() => setItems([]));
    }
  }, [review.media_type]);

  const filtered = useMemo(() => {
    const list = (items ?? []).slice().sort((a, b) => a.title.localeCompare(b.title));
    const t = q.trim().toLowerCase();
    return t ? list.filter((i) => i.title.toLowerCase().includes(t)) : list;
  }, [items, q]);

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Import into a different {review.media_type === "series" ? "show" : "movie"}</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[11.5px] text-ink-dim">Pick the correct library {review.media_type === "series" ? "series" : "movie"} to import <span className="font-mono">{review.name}</span> into.</p>
        <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter your library…" className="mb-3 w-full rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <div className="thin-scroll max-h-[52vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {items === null ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">Loading…</div>
          ) : filtered.length === 0 ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">No matching library items.</div>
          ) : filtered.map((i) => (
            <button key={i.id} onClick={() => onPicked(i.id, i.title)} className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <span className="truncate font-semibold">{i.title}</span>
              <span className="flex-none font-mono text-[10.5px] text-ink-faint">{i.year || ""}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
