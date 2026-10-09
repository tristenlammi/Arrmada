import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type ImportReview, type ReviewFile, type ReviewKind, type ReviewReason, type ReviewTarget } from "../lib/api";
import { formatAgo, formatBytes } from "../lib/format";
import { useLive } from "../lib/useLive";
import { usePoll } from "../lib/usePoll";
import { refreshAttention } from "../lib/useAttention";
import { ConfirmDialog, Modal, StatusChip, useConfirm, useToast, type Tone } from "../ui";
import { MapFilesModal } from "./reviews/MapFilesModal";
import { guessLabel } from "./reviews/episodes";

// What the user calls a library item of each review kind.
const KIND_LABEL: Record<ReviewKind, string> = { series: "show", movie: "movie", book: "book", music: "album" };

// Each reason's chip, and the one-line hint of what to do about it.
const REASON: Record<ReviewReason, { label: string; tone: Tone }> = {
  mismatch: { label: "Doesn't match", tone: "avoid" },
  unmatched: { label: "Not linked", tone: "faint" },
  numbering: { label: "Numbering", tone: "accent" },
  import_failed: { label: "Import failing", tone: "reject" },
  no_media: { label: "Nothing to import", tone: "reject" },
};

// Where a review's title lives in the app.
function titleLink(r: ImportReview): string | null {
  if (r.expected_id <= 0) return null;
  switch (r.media_type) {
    case "movie": return `/movies/${r.expected_id}`;
    case "series": return `/series/${r.expected_id}`;
    case "book": return `/books/${r.expected_id}`;
    case "music": return `/music/album/${r.expected_id}`;
  }
}

function heldAt(s: string): number {
  const t = Date.parse(s.includes("T") ? s : s.replace(" ", "T") + "Z");
  return Number.isNaN(t) ? 0 : t;
}

// Reviews — finished downloads held back from import. Each card says why, and offers only
// the actions that can work for that reason.
export function Reviews() {
  const [list, setList] = useState<ImportReview[] | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [reassign, setReassign] = useState<ImportReview | null>(null);
  const [removing, setRemoving] = useState<ImportReview | null>(null);
  const [mapping, setMapping] = useState<ImportReview | null>(null);
  const confirm = useConfirm();
  const toast = useToast();

  const refresh = () => api.reviews().then(setList).catch(() => setList((xs) => xs ?? []));
  // The page is left open while downloads finish: re-read it every 30 s while visible, and at
  // once when the server announces a newly held import.
  usePoll(refresh, 30_000);
  const { last } = useLive();
  useEffect(() => { if (last?.topic === "import.held") refresh(); }, [last]);
  // A review settled elsewhere (another tab, the download removed) leaves the selection.
  useEffect(() => {
    if (!list) return;
    setSelected((s) => {
      const ids = new Set(list.map((r) => r.id));
      const next = new Set([...s].filter((id) => ids.has(id)));
      return next.size === s.size ? s : next;
    });
  }, [list]);

  const drop = (id: number) => setList((xs) => (xs ?? []).filter((r) => r.id !== id));
  const act = async (id: number, fn: () => Promise<unknown>, msg: string) => {
    setBusy(id);
    try { await fn(); drop(id); toast(msg); refreshAttention(); }
    catch (e) { toast((e as Error).message, { tone: "error" }); }
    finally { setBusy(null); }
  };

  // Reject deletes the download's files, so it always asks. One tied to nothing is blocked
  // for every title, which the question says.
  const reject = async (r: ImportReview, findAnother: boolean) => {
    const global = r.expected_id <= 0;
    const ok = await confirm({
      title: findAnother ? "Reject and find another release?" : "Reject this download?",
      body: (
        <>
          <span className="break-all font-mono">{r.name}</span>
          <p className="mb-0 mt-2">
            Removes it and deletes its files{r.size_bytes > 0 ? ` (${formatBytes(r.size_bytes)})` : ""}, and blocklists the release
            {global ? <b> for every title</b> : ` for ${r.expected_title || `this ${kindLabel(r.media_type)}`}`}.
            {findAnother && !global ? ` Then searches for another release of ${r.expected_title || "it"}.` : ""}
          </p>
        </>
      ),
      confirmLabel: findAnother && !global ? "Reject & find another" : "Reject",
      tone: "danger",
    });
    if (!ok) return;
    await act(r.id, async () => {
      const res = await api.rejectReview(r.id, findAnother && !global);
      if (res.search_error) throw new Error(`Rejected, but ${res.search_error}`);
    }, findAnother && !global ? "Rejected and blocklisted — searching for another release." : "Rejected and blocklisted.");
  };

  const toggle = (id: number) => setSelected((s) => {
    const next = new Set(s);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const chosen = (list ?? []).filter((r) => selected.has(r.id));

  const bulk = async (action: "dismiss" | "reject") => {
    if (chosen.length === 0) return;
    if (action === "reject") {
      const globals = chosen.filter((r) => r.expected_id <= 0).length;
      const ok = await confirm({
        title: `Reject ${chosen.length} download${chosen.length === 1 ? "" : "s"}?`,
        body: (
          <p className="m-0">
            Each is removed with its files and its release blocklisted.
            {globals > 0 && <> <b>{globals} {globals === 1 ? "isn't" : "aren't"} tied to a title, so {globals === 1 ? "its release is" : "their releases are"} blocked for every title.</b></>}
          </p>
        ),
        confirmLabel: "Reject all",
        tone: "danger",
      });
      if (!ok) return;
    }
    setBulkBusy(true);
    try {
      const res = await api.bulkReviews(chosen.map((r) => r.id), action);
      refreshAttention();
      const failed = new Set(res.failed.map((f) => f.id));
      setList((xs) => (xs ?? []).filter((r) => !selected.has(r.id) || failed.has(r.id)));
      setSelected(failed);
      const verb = action === "dismiss" ? "Dismissed" : "Rejected";
      toast(res.failed.length > 0
        ? `${verb} ${res.done}; ${res.failed.length} couldn't be: ${res.failed[0].error}`
        : `${verb} ${res.done}.`, { tone: res.failed.length > 0 ? "error" : undefined });
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBulkBusy(false);
    }
  };

  return (
    <>
      <PageHeader title="Review" />
      <div className="mx-auto w-full max-w-[960px] px-4 py-6 sm:px-6">
        <p className="mb-5 max-w-[72ch] text-[12.5px] text-ink-dim">
          Finished downloads Arrmada held back instead of importing. Each says why: the content doesn't match what it was
          grabbed for, it isn't linked to anything in your library, its files' episode numbering couldn't be read, its import
          keeps failing (a folder Arrmada can't write to, a full disk), or there's nothing importable inside. Pick what to do —
          only the actions that can work for that reason are offered.
        </p>

        {list && list.length > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-2 text-[12px] text-ink-dim">
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={chosen.length === list.length} onChange={(e) => setSelected(e.target.checked ? new Set(list.map((r) => r.id)) : new Set())} />
              {chosen.length > 0 ? `${chosen.length} selected` : "Select all"}
            </label>
            {chosen.length > 0 && (
              <>
                <button onClick={() => bulk("dismiss")} disabled={bulkBusy} className="rounded-lg px-3 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Dismiss selected</button>
                <button onClick={() => bulk("reject")} disabled={bulkBusy} className="rounded-lg px-3 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Reject selected</button>
                {bulkBusy && <span>Working…</span>}
              </>
            )}
          </div>
        )}

        {list === null ? (
          <div className="p-10 text-center text-[12.5px] text-ink-dim">Loading…</div>
        ) : list.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            Nothing to review — every finished download went into the library. 🎉
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            {list.map((r) => (
              <ReviewCard
                key={r.id}
                r={r}
                busy={busy === r.id || bulkBusy}
                selected={selected.has(r.id)}
                onSelect={() => toggle(r.id)}
                onImport={() => act(r.id, () => api.importReview(r.id), `Imported into ${r.expected_title}.`)}
                onReassign={() => setReassign(r)}
                onReject={(findAnother) => reject(r, findAnother)}
                onRetry={() => act(r.id, () => api.retryReview(r.id), "Retrying — the import runs again within a minute.")}
                onRemove={() => setRemoving(r)}
                onMap={() => setMapping(r)}
                onDismiss={() => act(r.id, () => api.dismissReview(r.id), "Dismissed — the files stay in the downloads folder.")}
              />
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
      {mapping && (
        <MapFilesModal
          review={mapping}
          onClose={() => setMapping(null)}
          onDone={(msg) => { const id = mapping.id; setMapping(null); drop(id); toast(msg); }}
        />
      )}
      {removing && (
        <RemoveHeldDialog
          review={removing}
          onClose={() => setRemoving(null)}
          onRemoved={(msg) => { const id = removing.id; setRemoving(null); drop(id); toast(msg); }}
        />
      )}
    </>
  );
}

interface CardProps {
  r: ImportReview;
  busy: boolean;
  selected: boolean;
  onSelect: () => void;
  onImport: () => void;
  onReassign: () => void;
  onReject: (findAnother: boolean) => void;
  onRetry: () => void;
  onRemove: () => void;
  onMap: () => void;
  onDismiss: () => void;
}

function ReviewCard({ r, busy, selected, onSelect, onImport, onReassign, onReject, onRetry, onRemove, onMap, onDismiss }: CardProps) {
  const [showFiles, setShowFiles] = useState(false);
  const reason = REASON[r.reason_code] ?? REASON.mismatch;
  const link = titleLink(r);
  const label = kindLabel(r.media_type);
  const held = heldAt(r.created_at);
  const linked = r.expected_id > 0;

  const btn = "rounded-lg px-3 py-1.5 text-[11.5px] font-semibold";
  const primary = { border: "1px solid var(--line)", color: "var(--ink)" };
  const accent = { border: "1px solid var(--accent-line)", color: "var(--accent)" };
  const danger = { border: "1px solid var(--reject)", color: "var(--reject)" };

  return (
    <div className="rounded-xl p-4" style={{ border: `1px solid ${selected ? "var(--accent)" : "var(--avoid)"}`, background: "var(--panel)" }}>
      <div className="flex items-start gap-3">
        <input type="checkbox" className="mt-1" checked={selected} onChange={onSelect} aria-label={`Select ${r.name}`} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{r.media_type}</span>
            <StatusChip tone={reason.tone}>{reason.label}</StatusChip>
            <span className="min-w-0 break-all font-mono text-[12px]">{r.name}</span>
          </div>
          <div className="mt-2 text-[12.5px]" style={{ color: "var(--avoid)" }}>{r.reason}</div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[10.5px] text-ink-faint">
            {linked ? (
              <span>Grabbed for: {link
                ? <Link to={link} className="font-bold" style={{ color: "var(--accent)" }}>{r.expected_title || `this ${label}`}</Link>
                : <b className="text-ink-dim">{r.expected_title}</b>}</span>
            ) : (
              <span>Not linked to a {label} in your library</span>
            )}
            {r.parsed_title && <span>Looks like: <b className="text-ink-dim">{r.parsed_title}</b></span>}
            {r.size_bytes > 0 && <span>{formatBytes(r.size_bytes)}</span>}
            {r.indexer && <span>{r.indexer}</span>}
            {held > 0 && <span title={new Date(held).toLocaleString()}>Held {formatAgo(held)}</span>}
          </div>
          {r.content_path && (
            <div className="mt-1 flex min-w-0 items-center gap-2 font-mono text-[10.5px] text-ink-faint">
              <span className="truncate" title={r.content_path}>{r.content_path}</span>
              <button onClick={() => setShowFiles((v) => !v)} className="flex-none font-semibold" style={{ color: "var(--accent)" }} aria-expanded={showFiles}>
                {showFiles ? "Hide files" : "Show files"}
              </button>
            </div>
          )}
          {showFiles && <FileList reviewId={r.id} />}
        </div>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2 pl-7">
        {r.reason_code === "mismatch" && (
          <>
            {linked && <button onClick={onImport} disabled={busy} className={btn} style={primary}>Import anyway</button>}
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose different {label}…</button>
            <button onClick={() => onReject(true)} disabled={busy} className={btn} style={danger}>{linked ? "Reject & find another" : "Reject"}</button>
          </>
        )}
        {r.reason_code === "unmatched" && (
          <>
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose {label}…</button>
            <button onClick={() => onReject(false)} disabled={busy} className={btn} style={danger} title="Blocks this release for every title">Reject</button>
            {/* Takes it out of the client without blocklisting the release everywhere. */}
            {r.hash && <button onClick={onRemove} disabled={busy} className={btn} style={danger}>Remove download</button>}
          </>
        )}
        {r.reason_code === "numbering" && (
          <>
            {r.media_type === "series" && <button onClick={onMap} disabled={busy} className={btn} style={primary}>Map files…</button>}
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose different show…</button>
          </>
        )}
        {r.reason_code === "import_failed" && (
          <button onClick={onRetry} disabled={busy} className={btn} style={primary} title="Fix the cause first — then the import sweep tries again">Retry import</button>
        )}
        {r.reason_code === "no_media" && (
          <button onClick={() => onReject(true)} disabled={busy} className={btn} style={danger}>{linked ? "Reject & find another" : "Reject"}</button>
        )}
        <button onClick={onDismiss} disabled={busy} className="ml-auto rounded-lg px-3 py-1.5 text-[11.5px] text-ink-dim hover:text-[var(--ink)]" title="Leave the files in the downloads folder and handle it yourself">Dismiss</button>
      </div>
    </div>
  );
}

// FileList shows what's inside a held download, with sizes and what each name says
// about its episode.
function FileList({ reviewId }: { reviewId: number }) {
  const [files, setFiles] = useState<ReviewFile[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    api.reviewFiles(reviewId)
      .then((r) => { if (live) { setFiles(r.files); setTruncated(r.truncated); } })
      .catch((e) => { if (live) setError((e as Error).message); });
    return () => { live = false; };
  }, [reviewId]);
  if (error) return <div className="mt-2 text-[11.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  if (!files) return <div className="mt-2 text-[11.5px] text-ink-faint">Listing files…</div>;
  if (files.length === 0) return <div className="mt-2 text-[11.5px] text-ink-faint">No files in the download.</div>;
  return (
    <div className="thin-scroll mt-2 max-h-[240px] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
      {files.map((f) => (
        <div key={f.rel_path} className="flex items-center gap-3 px-3 py-1.5 font-mono text-[10.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
          <span className={`min-w-0 flex-1 truncate ${f.video ? "" : "text-ink-faint"}`} title={f.rel_path}>{f.rel_path}</span>
          <span className="flex-none text-ink-faint">{guessLabel(f)}</span>
          <span className="w-16 flex-none text-right text-ink-dim">{formatBytes(f.size)}</span>
        </div>
      ))}
      {truncated && <div className="px-3 py-1.5 text-[10.5px] text-ink-faint">Showing the first {files.length} files.</div>}
    </div>
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
        { value: "delete_files", label: "Remove and delete the files", hint: review.size_bytes > 0 ? `Deletes ${formatBytes(review.size_bytes)} from the downloads folder.` : undefined, danger: true },
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
  const filterRef = useRef<HTMLInputElement>(null);
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
    <Modal onClose={onClose} title={review.expected_id > 0 ? `Import into a different ${label}…` : `Import into a ${label}…`} initialFocus={filterRef}>
      <p className="mb-3 text-[11.5px] text-ink-dim">Pick the correct library {label} to import <span className="font-mono">{review.name}</span> into.</p>
      <input ref={filterRef} value={q} onChange={(e) => setQ(e.target.value)} placeholder={review.media_type === "book" ? "Filter by title or author…" : review.media_type === "music" ? "Filter by album or artist…" : "Filter your library…"} className="mb-3 w-full rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
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
    </Modal>
  );
}
