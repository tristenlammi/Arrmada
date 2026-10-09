import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { api, type ImportReview, type ReviewKind, type ReviewTarget } from "../lib/api";
import { useLive } from "../lib/useLive";
import { useVisiblePoll } from "../lib/useVisiblePoll";

// What the user calls a library item of each review kind.
const KIND_LABEL: Record<ReviewKind, string> = { series: "show", movie: "movie", book: "book", music: "album" };

// Reviews — downloads Arrmada grabbed but held back because their content doesn't
// match what they were grabbed for. The admin reviews each: reject, import anyway,
// import into a different library item, or dismiss.
export function Reviews() {
  const [list, setList] = useState<ImportReview[] | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [reassign, setReassign] = useState<ImportReview | null>(null);
  const [removing, setRemoving] = useState<ImportReview | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  const refresh = () => api.reviews().then(setList).catch(() => setList((xs) => xs ?? []));
  // The page is left open while downloads finish: re-read it every 30 s while visible, and at
  // once when the server announces a newly held import.
  useVisiblePoll(() => { refresh(); }, 30_000);
  const { last } = useLive();
  useEffect(() => { if (last?.topic === "import.held") refresh(); }, [last]);

  const act = async (id: number, fn: () => Promise<unknown>, msg: string) => {
    setBusy(id);
    try { await fn(); setList((xs) => (xs ?? []).filter((r) => r.id !== id)); flash(msg); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(null); }
  };

  return (
    <>
      <PageHeader title="Review" />
      <div className="mx-auto w-full max-w-[960px] px-4 py-6 sm:px-6">
        <p className="mb-5 max-w-[70ch] text-[12.5px] text-ink-dim">
          Downloads that finished but whose content didn't match what they were grabbed for are held here instead of
          being imported. Reject to remove + blocklist them, import anyway if it's a false alarm, import into a
          different library item, or dismiss to handle it yourself.
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
                      <span>Grabbed for: <b className="text-ink-dim">{r.expected_id > 0 && r.expected_title ? r.expected_title : "not tied to a title"}</b></span>
                      <span>Looks like: <b className="text-ink-dim">{r.parsed_title || "?"}</b></span>
                      {r.size_bytes > 0 && <span>{gb(r.size_bytes)}</span>}
                      {r.indexer && <span>{r.indexer}</span>}
                    </div>
                  </div>
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2">
                  <button onClick={() => act(r.id, () => api.rejectReview(r.id), "Rejected + blocklisted.")} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>Reject</button>
                  {/* With no item to import into, "anyway" has nowhere to go — the reassign picker is the only way in. */}
                  {r.expected_id > 0 && (
                    <button onClick={() => act(r.id, () => api.importReview(r.id), `Imported into ${r.expected_title}.`)} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Import anyway</button>
                  )}
                  <button onClick={() => setReassign(r)} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{r.expected_id > 0 ? `Import into a different ${kindLabel(r.media_type)}…` : `Import into…`}</button>
                  {/* Not tied to a title (e.g. grabbed for a movie since deleted): take it out of
                      the client without blocklisting the release everywhere, as Reject would. */}
                  {r.expected_id === 0 && r.hash && (
                    <button onClick={() => setRemoving(r)} disabled={busy === r.id} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Remove download</button>
                  )}
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
            const kind = reassign.media_type;
            setReassign(null);
            act(id, () => api.importReview(id, targetId, kind), `Imported into ${label}.`);
          }}
        />
      )}
      {removing && (
        <RemoveHeldDialog
          review={removing}
          onClose={() => setRemoving(null)}
          onRemoved={(msg) => { const id = removing.id; setRemoving(null); setList((xs) => (xs ?? []).filter((x) => x.id !== id)); flash(msg); }}
        />
      )}
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

// RemoveHeldDialog takes a held download out of the client, keeping its files by default
// (they may be the only copy). The server settles the review once the torrent is gone.
function RemoveHeldDialog({ review, onClose, onRemoved }: { review: ImportReview; onClose: () => void; onRemoved: (msg: string) => void }) {
  const [mode, setMode] = useState<"keep_files" | "delete_files">("keep_files");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await api.deleteDownload(review.hash, { mode, name: review.name });
      onRemoved(mode === "delete_files" ? "Removed and its files deleted." : "Removed — the files are kept.");
    } catch (e) {
      setErr((e as Error).message);
      setBusy(false);
    }
  };
  return (
    <ConfirmDialog
      title="Remove this download from the client?"
      body={<span className="break-all font-mono">{review.name}</span>}
      choices={[
        { value: "keep_files", label: "Remove from the client, keep the files" },
        { value: "delete_files", label: "Remove and delete the files", hint: review.size_bytes > 0 ? `Deletes ${gb(review.size_bytes)} from the downloads folder.` : undefined, danger: true },
      ]}
      choice={mode}
      onChoice={setMode}
      confirmLabel={mode === "delete_files" ? "Remove and delete files" : "Remove, keep files"}
      busyLabel="Removing…"
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}

function gb(bytes: number): string {
  const g = bytes / 1024 ** 3;
  return g >= 1 ? `${g.toFixed(2)} GB` : `${(bytes / 1024 ** 2).toFixed(0)} MB`;
}

function kindLabel(kind: ReviewKind): string {
  return KIND_LABEL[kind] ?? kind;
}

// ReassignModal lets the admin pick an existing library item (of the review's
// media type) to import the held content into. The server does the listing and the
// filtering, so a book review lists books and an album review lists albums — never
// movies, whose ids would land on an unrelated book or album.
function ReassignModal({ review, onClose, onPicked }: { review: ImportReview; onClose: () => void; onPicked: (targetId: number, label: string) => void }) {
  const [items, setItems] = useState<ReviewTarget[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const label = kindLabel(review.media_type);

  // Debounced so typing a title doesn't fire a request per keystroke.
  useEffect(() => {
    let live = true;
    const t = window.setTimeout(() => {
      api.reviewTargets(review.id, q.trim())
        .then((r) => { if (live) { setItems(r.targets); setTruncated(!!r.truncated); setError(null); } })
        .catch((e) => { if (live) { setItems([]); setTruncated(false); setError((e as Error).message); } });
    }, q ? 250 : 0);
    return () => { live = false; window.clearTimeout(t); };
  }, [review.id, q]);

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Import into a different {label}…</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[11.5px] text-ink-dim">Pick the correct library {label} to import <span className="font-mono">{review.name}</span> into.</p>
        <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={review.media_type === "book" ? "Filter by title or author…" : review.media_type === "music" ? "Filter by album or artist…" : "Filter your library…"} className="mb-3 w-full rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <div className="thin-scroll max-h-[52vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {error ? (
            <div className="p-6 text-center text-[12px]" style={{ color: "var(--avoid)" }}>{error}</div>
          ) : items === null ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">Loading…</div>
          ) : items.length === 0 ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">No matching library items.</div>
          ) : items.map((i) => (
            <button key={i.id} onClick={() => onPicked(i.id, i.title)} className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <span className="min-w-0 truncate">
                <span className="font-semibold">{i.title}{i.year ? ` (${i.year})` : ""}</span>
                {i.subtitle && <span className="ml-2 text-[11px] text-ink-dim">{i.subtitle}</span>}
              </span>
            </button>
          ))}
        </div>
        {truncated && !error && (
          <p className="mb-0 mt-2 text-[11px] text-ink-faint">Showing the first {items?.length ?? 0} — type to narrow.</p>
        )}
      </div>
    </div>
  );
}
