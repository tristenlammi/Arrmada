import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ApiError, api, type ImportReview, type Job, type ReviewFile, type ReviewImportStarted, type ReviewKind, type ReviewReason, type ReviewTarget } from "../lib/api";
import { formatAgo, formatBytes } from "../lib/format";
import { useLiveRefresh } from "../lib/useLiveQuery";
import { refreshAttention } from "../lib/useAttention";
import { jobDone, jobFailed, jobToast, useJob } from "../lib/useJob";
import { ConfirmDialog, Modal, StatusChip, useConfirm, useToast, type Tone } from "../ui";
import { MapFilesModal } from "./reviews/MapFilesModal";
import { MatchBreakdown } from "./reviews/MatchBreakdown";
import { guessLabel } from "./reviews/episodes";
import { musicApi } from "../lib/musicApi";

// What the user calls a library item of each review kind.
const KIND_LABEL: Record<ReviewKind, string> = { series: "show", movie: "movie", book: "book", music: "album" };

// Each reason's chip, and the one-line hint of what to do about it.
const REASON: Record<ReviewReason, { label: string; tone: Tone }> = {
  mismatch: { label: "Doesn't match", tone: "avoid" },
  same_name: { label: "Which show?", tone: "avoid" },
  unmatched: { label: "Not linked", tone: "faint" },
  numbering: { label: "Numbering", tone: "accent" },
  title_conflict: { label: "Titles disagree", tone: "accent" },
  import_failed: { label: "Import failing", tone: "reject" },
  no_media: { label: "Nothing to import", tone: "reject" },
  replace_check: { label: "Not as named", tone: "avoid" },
  // The music matcher's (MUS-31): it says in words why underneath.
  low_confidence: { label: "Not sure", tone: "avoid" },
  close_editions: { label: "Two editions", tone: "accent" },
  partial: { label: "Partial album", tone: "avoid" },
  tag_conflict: { label: "Tags disagree", tone: "avoid" },
  fingerprint_conflict: { label: "Sound disagrees", tone: "avoid" },
};

// The music matcher's reasons: each offers importing into the best fit (or one of the
// editions it weighed), another album, or rejecting.
const MATCHER_REASONS = new Set<ReviewReason>(["low_confidence", "close_editions", "partial", "tag_conflict", "fingerprint_conflict"]);

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

// ReviewsPanel is the held imports: finished downloads held back from import. Each card
// says why, and offers only the actions that can work for that reason. Activity → Needs
// you shows it as its first section (compact: no introduction, a short empty state).
export function ReviewsPanel({ compact = false }: { compact?: boolean } = {}) {
  const [list, setList] = useState<ImportReview[] | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [reassign, setReassign] = useState<ImportReview | null>(null);
  const [removing, setRemoving] = useState<ImportReview | null>(null);
  const [mapping, setMapping] = useState<ImportReview | null>(null);
  // The import job each importing card follows, and the reason each card's last import
  // was refused or failed ("" once a new one starts).
  const [imports, setImports] = useState<Record<number, { jobId: number; label?: string }>>({});
  const [importErrors, setImportErrors] = useState<Record<number, string>>({});
  const confirm = useConfirm();
  const toast = useToast();

  const refresh = () => api.reviews().then(setList).catch(() => setList((xs) => xs ?? []));
  // The page is left open while downloads finish: re-read it at once when the server
  // announces a newly held import or a finished one, and every 30 s while visible as a
  // backstop.
  useLiveRefresh(refresh, { topics: ["import.held", "download.imported"], fallbackMs: 30_000, immediate: true });
  // A review settled elsewhere (another tab, the download removed) leaves the selection.
  useEffect(() => {
    if (!list) return;
    setSelected((s) => {
      const ids = new Set(list.map((r) => r.id));
      const next = new Set([...s].filter((id) => ids.has(id)));
      return next.size === s.size ? s : next;
    });
  }, [list]);

  // A list read mid-import (a reload, another tab started it) brings each review's running
  // import job with it, and a failed last import its reason: pick both up, without
  // overriding what this page is already following or has just been told.
  useEffect(() => {
    if (!list) return;
    setImports((cur) => {
      let next = cur;
      for (const r of list) {
        const j = r.import_job;
        if (j && !jobDone(j.status) && !cur[r.id]) next = { ...next, [r.id]: { jobId: j.id } };
      }
      return next;
    });
    setImportErrors((cur) => {
      let next = cur;
      for (const r of list) {
        const j = r.import_job;
        if (j && jobFailed(j) && cur[r.id] === undefined) next = { ...next, [r.id]: j.error || "The import failed." };
      }
      return next;
    });
  }, [list]);

  const drop = (id: number) => setList((xs) => (xs ?? []).filter((r) => r.id !== id));
  const act = async (id: number, fn: () => Promise<unknown>, msg: string) => {
    setBusy(id);
    try { await fn(); drop(id); toast(msg); refreshAttention(); }
    catch (e) { toast((e as Error).message, { tone: "error" }); }
    finally { setBusy(null); }
  };

  // Importing runs as a job the card follows. What the server refuses at once (the
  // download is gone, the module is off) comes back here and stays on the card.
  const startImport = async (id: number, fn: () => Promise<ReviewImportStarted>, label?: string) => {
    setBusy(id);
    setImportErrors((cur) => ({ ...cur, [id]: "" }));
    try {
      const res = await fn();
      const jobId = res.job_id;
      if (jobId) setImports((cur) => ({ ...cur, [id]: { jobId, label: label ?? res.title } }));
      else { drop(id); toast(label ? `Imported into ${label}.` : "Imported."); refreshAttention(); }
    } catch (e) {
      // Another import of this download is already running (another tab asked for a
      // different title): say so, and follow that one instead.
      const running = runningImport(e);
      if (running) {
        setImports((cur) => ({ ...cur, [id]: running }));
        toast((e as Error).message, { tone: "error" });
      } else {
        setImportErrors((cur) => ({ ...cur, [id]: (e as Error).message }));
      }
    } finally {
      setBusy(null);
    }
  };
  const importDone = (id: number, job: Job) => {
    const label = imports[id]?.label;
    setImports((cur) => without(cur, id));
    if (job.status === "succeeded") {
      drop(id);
      toast(job.message || (label ? `Imported into ${label}.` : "Imported."));
      refreshAttention();
      return;
    }
    const why = jobToast(job);
    setImportErrors((cur) => ({ ...cur, [id]: why }));
    toast(`Couldn't import: ${why}`, { tone: "error" });
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
      <div className="max-w-[960px]">
        {!compact && <p className="mb-5 max-w-[72ch] text-base text-ink-dim">
          Finished downloads Arrmada held back instead of importing. Each says why: the content doesn't match what it was
          grabbed for, it isn't linked to anything in your library, its files' episode numbering couldn't be read or disagrees
          with their episode titles, its import
          keeps failing (a folder Arrmada can't write to, a full disk), or there's nothing importable inside. Pick what to do —
          only the actions that can work for that reason are offered.
        </p>}

        {list && list.length > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-2 text-sm text-ink-dim">
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={chosen.length === list.length} onChange={(e) => setSelected(e.target.checked ? new Set(list.map((r) => r.id)) : new Set())} />
              {chosen.length > 0 ? `${chosen.length} selected` : "Select all"}
            </label>
            {chosen.length > 0 && (
              <>
                <button onClick={() => bulk("dismiss")} disabled={bulkBusy} className="rounded-lg px-3 py-1 text-sm font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Dismiss selected</button>
                <button onClick={() => bulk("reject")} disabled={bulkBusy} className="rounded-lg px-3 py-1 text-sm font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>Reject selected</button>
                {bulkBusy && <span>Working…</span>}
              </>
            )}
          </div>
        )}

        {list === null ? (
          <div className="p-10 text-center text-base text-ink-dim">Loading…</div>
        ) : list.length === 0 ? (
          <div className={`rounded-xl ${compact ? "p-5" : "p-12"} text-center text-base text-ink-dim`} style={{ border: "1px solid var(--line)" }}>
            {compact ? "Nothing held for review." : "Nothing to review — every finished download went into the library. 🎉"}
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            {list.map((r) => (
              <ReviewCard
                key={r.id}
                r={r}
                busy={busy === r.id || bulkBusy}
                importJob={imports[r.id]?.jobId ?? null}
                importError={importErrors[r.id] || null}
                onImportDone={(job) => importDone(r.id, job)}
                selected={selected.has(r.id)}
                onSelect={() => toggle(r.id)}
                onImport={() => void startImport(r.id, () => api.importReview(r.id), r.expected_title || undefined)}
                onImportEdition={(albumId, releaseId, label) => void startImport(r.id, () => musicApi.importReviewEdition(r.id, albumId, releaseId), label)}
                onReassign={() => setReassign(r)}
                onReject={(findAnother) => reject(r, findAnother)}
                onRetry={() => act(r.id, () => api.retryReview(r.id), "Retrying — the import runs again within a minute.")}
                onRemove={() => setRemoving(r)}
                onMap={() => setMapping(r)}
                onDismiss={() => act(r.id, () => api.dismissReview(r.id), r.source === "library_scan"
                  ? "Dismissed — the folder stays as it is."
                  : "Dismissed — the files stay in the downloads folder.")}
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
            void startImport(id, () => api.importReview(id, targetId, kind), label);
          }}
        />
      )}
      {mapping && (
        <MapFilesModal
          review={mapping}
          onClose={() => setMapping(null)}
          onStarted={(res) => {
            const id = mapping.id;
            setMapping(null);
            void startImport(id, () => Promise.resolve(res));
          }}
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

// A copy onto another drive can run for an hour or more; a card keeps following its import
// for as long as the server would let it run.
export const IMPORT_FOLLOW_MS = 12 * 60 * 60 * 1000;

// runningImport is the import a 409 says is already running for this download — a
// different one than was asked for — so the card can follow it.
export function runningImport(e: unknown): { jobId: number; label?: string } | null {
  if (!(e instanceof ApiError) || e.status !== 409) return null;
  const jobId = Number(e.body?.job_id);
  if (!jobId) return null;
  const title = e.body?.title;
  return { jobId, label: typeof title === "string" && title ? title : undefined };
}

function without<T>(rec: Record<number, T>, id: number): Record<number, T> {
  const next = { ...rec };
  delete next[id];
  return next;
}

interface CardProps {
  r: ImportReview;
  busy: boolean;
  // The import job this card is following, and the reason its last import was refused
  // or failed.
  importJob: number | null;
  importError: string | null;
  onImportDone: (job: Job) => void;
  selected: boolean;
  onSelect: () => void;
  onImport: () => void;
  /** Music: import into one edition of an album the matcher weighed. */
  onImportEdition: (albumId: number, releaseId: number, label: string) => void;
  onReassign: () => void;
  onReject: (findAnother: boolean) => void;
  onRetry: () => void;
  onRemove: () => void;
  onMap: () => void;
  onDismiss: () => void;
}

function ReviewCard({ r, busy: acting, importJob, importError, onImportDone, selected, onSelect, onImport, onImportEdition, onReassign, onReject, onRetry, onRemove, onMap, onDismiss }: CardProps) {
  const [showFiles, setShowFiles] = useState(false);
  const follow = useJob(importJob, { onDone: onImportDone, giveUpMs: IMPORT_FOLLOW_MS });
  // Importing from the moment the job is known until it ends: every action waits for it.
  const importing = !!importJob;
  const busy = acting || importing;
  const reason = REASON[r.reason_code] ?? REASON.mismatch;
  const link = titleLink(r);
  const label = kindLabel(r.media_type);
  const held = heldAt(r.created_at);
  const linked = r.expected_id > 0;
  // A folder the music library scan couldn't place: already in the library, so it is
  // matched where it is, and never rejected (that would delete library files).
  const scanned = r.source === "library_scan";

  const btn = "rounded-lg px-3 py-1.5 text-sm font-semibold";
  const primary = { border: "1px solid var(--line)", color: "var(--ink)" };
  const accent = { border: "1px solid var(--accent-line)", color: "var(--accent-text)" };
  const danger = { border: "1px solid var(--reject)", color: "var(--reject-text)" };

  return (
    <div className="rounded-xl p-4" data-testid="review-card" aria-busy={importing || undefined} style={{ border: `1px solid ${selected ? "var(--accent)" : "var(--avoid)"}`, background: "var(--panel)" }}>
      <div className="flex items-start gap-3">
        <input type="checkbox" className="mt-1" checked={selected} onChange={onSelect} disabled={importing} aria-label={`Select ${r.name}`} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{r.media_type}</span>
            <StatusChip tone={reason.tone}>{reason.label}</StatusChip>
            {scanned && <StatusChip tone="faint">Library scan</StatusChip>}
            <span className="min-w-0 break-all font-mono text-sm">{r.name}</span>
          </div>
          <div className="mt-2 text-base" style={{ color: "var(--avoid-text)" }}>{r.reason}</div>
          {r.match && <MatchBreakdown match={r.match} />}
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-xs text-ink-faint">
            {linked ? (
              <span>{scanned ? "Closest fit" : "Grabbed for"}: {link
                ? <Link to={link} className="font-bold" style={{ color: "var(--accent-text)" }}>{r.expected_title || `this ${label}`}</Link>
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
            <div className="mt-1 flex min-w-0 items-center gap-2 font-mono text-xs text-ink-faint">
              <span className="truncate" title={r.content_path}>{r.content_path}</span>
              <button onClick={() => setShowFiles((v) => !v)} className="flex-none font-semibold" style={{ color: "var(--accent-text)" }} aria-expanded={showFiles}>
                {showFiles ? "Hide files" : "Show files"}
              </button>
            </div>
          )}
          {showFiles && <FileList reviewId={r.id} />}
          {importing && <ImportingLine job={follow.job} />}
          {!importing && importError && (
            <div role="alert" className="mt-2 rounded-lg px-3 py-2 text-base" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>
              <b>Couldn't import:</b> {importError}
            </div>
          )}
        </div>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2 pl-7">
        {scanned && (
          <>
            {r.reason_code === "close_editions"
              ? (r.match?.candidates ?? []).slice(0, 2).map((c) => (
                  <button key={c.release_id} onClick={() => onImportEdition(c.album_id, c.release_id, c.album)} disabled={busy} className={btn} style={primary}>
                    Match as {c.edition}
                  </button>
                ))
              : linked && <button onClick={onImport} disabled={busy} className={btn} style={primary}>Match to {r.expected_title || "this album"}</button>}
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Match…</button>
          </>
        )}
        {!scanned && (r.reason_code === "mismatch" || r.reason_code === "same_name") && (
          <>
            {linked && <button onClick={onImport} disabled={busy} className={btn} style={primary}>Import anyway</button>}
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose different {label}…</button>
            <button onClick={() => onReject(true)} disabled={busy} className={btn} style={danger}>{linked ? "Reject & find another" : "Reject"}</button>
          </>
        )}
        {!scanned && r.reason_code === "unmatched" && (
          <>
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose {label}…</button>
            <button onClick={() => onReject(false)} disabled={busy} className={btn} style={danger} title="Blocks this release for every title">Reject</button>
            {/* Takes it out of the client without blocklisting the release everywhere. */}
            {r.hash && <button onClick={onRemove} disabled={busy} className={btn} style={danger}>Remove download</button>}
          </>
        )}
        {(r.reason_code === "numbering" || r.reason_code === "title_conflict") && (
          <>
            {r.media_type === "series" && <button onClick={onMap} disabled={busy} className={btn} style={primary}>Map files…</button>}
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose different show…</button>
            {/* The release numbers its episodes another way: another release may not. */}
            <button onClick={() => onReject(true)} disabled={busy} className={btn} style={danger}>{linked ? "Reject & find another" : "Reject"}</button>
          </>
        )}
        {r.reason_code === "import_failed" && (
          <button onClick={onRetry} disabled={busy} className={btn} style={primary} title="Fix the cause first — then the import sweep tries again">Retry import</button>
        )}
        {!scanned && MATCHER_REASONS.has(r.reason_code) && (
          <>
            {r.reason_code === "close_editions"
              ? (r.match?.candidates ?? []).slice(0, 2).map((c) => (
                  <button key={c.release_id} onClick={() => onImportEdition(c.album_id, c.release_id, c.album)} disabled={busy} className={btn} style={primary}>
                    Import as {c.edition}
                  </button>
                ))
              : linked && <button onClick={onImport} disabled={busy} className={btn} style={primary}>Import anyway</button>}
            <button onClick={onReassign} disabled={busy} className={btn} style={accent}>Choose different {label}…</button>
            <button onClick={() => onReject(true)} disabled={busy} className={btn} style={danger}>{linked ? "Reject & find another" : "Reject"}</button>
          </>
        )}
        {r.reason_code === "replace_check" && (
          <>
            {/* A smaller release picked to replace a file: the file there is untouched. */}
            {linked && <button onClick={onImport} disabled={busy} className={btn} style={primary}>Import anyway</button>}
            <button onClick={() => onReject(false)} disabled={busy} className={btn} style={danger} title="Remove the download and block the release — your file stays">Reject</button>
          </>
        )}
        {r.reason_code === "no_media" && (
          <button onClick={() => onReject(true)} disabled={busy} className={btn} style={danger}>{linked ? "Reject & find another" : "Reject"}</button>
        )}
        <button onClick={onDismiss} disabled={busy} className="ml-auto rounded-lg px-3 py-1.5 text-sm text-ink-dim hover:text-[var(--ink)]"
          title={scanned ? "Leave the folder as it is; the scan won't hold it again until it changes" : "Leave the files in the downloads folder and handle it yourself"}>Dismiss</button>
      </div>
    </div>
  );
}

// ImportingLine is an importing card's status: a spinner (there's nothing to cancel — a
// half-done copy is cleaned up by the import itself), what the job says it's doing, and
// how far a copy has got.
export function ImportingLine({ job }: { job: Job | null }) {
  const queued = job?.status === "queued";
  const pct = job && job.progress > 0 ? Math.round(job.progress * 100) : 0;
  const msg = queued ? "Waiting for another import to finish…" : job?.message || "Importing…";
  return (
    <div className="mt-2 rounded-lg px-3 py-2" role="status" data-testid="review-importing" style={{ border: "1px solid var(--accent-line)", background: "var(--panel-2)" }}>
      <div className="flex items-center gap-2 text-base" style={{ color: "var(--accent-text)" }}>
        <span aria-hidden className="h-3 w-3 flex-none animate-spin rounded-full" style={{ border: "2px solid var(--line)", borderTopColor: "var(--accent)" }} />
        <span className="min-w-0 flex-1">{msg}</span>
      </div>
      {pct > 0 && (
        <div className="mt-1.5 h-1.5 overflow-hidden rounded-full" style={{ background: "var(--line)" }} role="progressbar" aria-label="Import progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
          <div className="h-full rounded-full" style={{ width: `${pct}%`, background: "var(--accent)" }} />
        </div>
      )}
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
  if (error) return <div className="mt-2 text-sm" style={{ color: "var(--reject-text)" }}>{error}</div>;
  if (!files) return <div className="mt-2 text-sm text-ink-faint">Listing files…</div>;
  if (files.length === 0) return <div className="mt-2 text-sm text-ink-faint">No files in the download.</div>;
  return (
    <div className="thin-scroll mt-2 max-h-[240px] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
      {files.map((f) => (
        <div key={f.rel_path} className="flex items-center gap-3 px-3 py-1.5 font-mono text-xs" style={{ borderTop: "1px solid var(--line-soft)" }}>
          <span className={`min-w-0 flex-1 truncate ${f.video ? "" : "text-ink-faint"}`} title={f.rel_path}>{f.rel_path}</span>
          <span className="flex-none text-ink-faint">{guessLabel(f)}</span>
          <span className="w-16 flex-none text-right text-ink-dim">{formatBytes(f.size)}</span>
        </div>
      ))}
      {truncated && <div className="px-3 py-1.5 text-xs text-ink-faint">Showing the first {files.length} files.</div>}
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
  const label = kindLabel(review.media_type);
  return (
    <TargetPicker
      title={review.source === "library_scan" ? "Match this folder to an album…"
        : review.expected_id > 0 ? `Import into a different ${label}…` : `Import into a ${label}…`}
      name={review.name}
      kind={review.media_type}
      load={(q) => api.reviewTargets(review.id, q)}
      loadKey={String(review.id)}
      onClose={onClose}
      onPicked={onPicked}
    />
  );
}

/**
 * TargetPicker is the "which library item?" dialog behind a review's reassign and Needs
 * you's Import into…: a filter box over the server's list of one kind of item. load is
 * asked again when the filter, kind or loadKey changes; header sits above the filter
 * (Import into… puts its kind switch there).
 */
export function TargetPicker({ title, name, kind, load, loadKey, header, initialQuery = "", onClose, onPicked }: {
  title: string;
  name: string;
  kind: ReviewKind;
  load: (q: string) => Promise<{ targets: ReviewTarget[]; truncated?: boolean }>;
  loadKey: string;
  header?: ReactNode;
  initialQuery?: string;
  onClose: () => void;
  onPicked: (targetId: number, label: string) => void;
}) {
  const [items, setItems] = useState<ReviewTarget[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [q, setQ] = useState(initialQuery);
  const filterRef = useRef<HTMLInputElement>(null);
  const label = kindLabel(kind);
  // The latest load, so a parent's inline function doesn't refetch on every render.
  const loadRef = useRef(load);
  useEffect(() => { loadRef.current = load; });

  // Another kind's list must never stay clickable under this one: an id is only an id
  // within its own kind.
  useEffect(() => { setItems(null); }, [loadKey, kind]);
  // Debounced so typing a title doesn't fire a request per keystroke.
  useEffect(() => {
    let live = true;
    const t = window.setTimeout(() => {
      loadRef.current(q.trim())
        .then((r) => { if (live) { setItems(r.targets); setTruncated(!!r.truncated); setError(null); } })
        .catch((e) => { if (live) { setItems([]); setTruncated(false); setError((e as Error).message); } });
    }, q ? 250 : 0);
    return () => { live = false; window.clearTimeout(t); };
  }, [loadKey, kind, q]);

  return (
    <Modal onClose={onClose} title={title} initialFocus={filterRef}>
      <p className="mb-3 text-sm text-ink-dim">Pick the library {label} to import <span className="break-all font-mono">{name}</span> into.</p>
      {header}
      <input ref={filterRef} value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter your library" placeholder={kind === "book" ? "Filter by title or author…" : kind === "music" ? "Filter by album or artist…" : "Filter your library…"} className="mb-3 w-full rounded-lg px-3 py-2 text-md" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
      <div className="thin-scroll max-h-[52vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
        {error ? (
          <div className="p-6 text-center text-sm" style={{ color: "var(--avoid-text)" }}>{error}</div>
        ) : items === null ? (
          <div className="p-6 text-center text-sm text-ink-faint">Loading…</div>
        ) : items.length === 0 ? (
          <div className="p-6 text-center text-sm text-ink-faint">No matching library items.</div>
        ) : items.map((i) => (
          <button key={i.id} onClick={() => onPicked(i.id, i.title)} className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-base hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
            <span className="min-w-0 truncate">
              <span className="font-semibold">{i.title}{i.year ? ` (${i.year})` : ""}</span>
              {i.subtitle && <span className="ml-2 text-xs text-ink-dim">{i.subtitle}</span>}
            </span>
          </button>
        ))}
      </div>
      {truncated && !error && (
        <p className="mb-0 mt-2 text-xs text-ink-faint">Showing the first {items?.length ?? 0} — type to narrow.</p>
      )}
    </Modal>
  );
}

export function kindLabelOf(kind: ReviewKind): string {
  return kindLabel(kind);
}
