import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { ReleaseSearchModal } from "../components/ReleaseSearchModal";
import { api, type Book, type BookFile, type BookFileEntry, type BookImportCandidate } from "../lib/api";

function fmtSize(bytes?: number): string {
  if (!bytes || bytes <= 0) return "";
  const mb = bytes / 1024 ** 2;
  return mb >= 1000 ? `${(mb / 1024).toFixed(2)} GB` : mb >= 1 ? `${mb.toFixed(1)} MB` : `${(bytes / 1024).toFixed(0)} KB`;
}

export function BookDetail() {
  const { id } = useParams();
  const bid = Number(id);
  const [b, setB] = useState<Book | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);

  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };
  const load = useCallback(() => {
    api.bookDetail(bid).then(setB).catch((e: Error) => {
      if (e.message.toLowerCase().includes("not found")) setNotFound(true);
      else setError(e.message);
    });
  }, [bid]);
  useEffect(() => { load(); }, [load]);

  if (notFound) return <Shell><div className="py-10 text-center text-[13px] text-ink-dim">That book isn't in your library. <Link to="/books" className="underline" style={{ color: "var(--accent)" }}>Back to Books</Link></div></Shell>;
  if (!b) return <Shell><p className="text-[12.5px] text-ink-dim">{error ?? "Loading…"}</p></Shell>;

  const st = b.has_file ? { label: "Downloaded", tone: "var(--good)", soft: "var(--good-soft, rgba(90,140,90,.14))" } : b.monitored ? { label: "Wanted", tone: "var(--avoid)", soft: "var(--avoid-soft)" } : { label: "Unmonitored", tone: "var(--ink-faint)", soft: "var(--panel-2)" };

  return (
    <>
      <PageHeader title={b.title} crumb="Library / Books" />
      <div className="relative">
        {b.cover_url && (
          <>
            <div className="pointer-events-none absolute inset-0 bg-cover bg-center opacity-[0.08] blur-2xl" style={{ backgroundImage: `url(${b.cover_url})` }} />
            {/* Scrim pulls every cover toward the neutral bg so a bright cover (e.g. an orange
                one) doesn't wash the whole hero — keeps all book pages equally clean. */}
            <div className="pointer-events-none absolute inset-0" style={{ background: "linear-gradient(180deg, transparent 0%, var(--bg) 65%)" }} />
          </>
        )}
        <div className="relative mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">
          <Link to="/books" className="mb-4 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
            All books
          </Link>
          <div className="flex flex-col gap-6 sm:flex-row">
            <Poster book={b} onChange={load} flash={flash} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2.5">
                <span className="rounded-full px-2.5 py-1 font-mono text-[10.5px] font-semibold uppercase" style={{ background: st.soft, color: st.tone }}>{st.label}</span>
                {b.year > 0 && <span className="font-mono text-[11px] text-ink-faint">{b.year}</span>}
                <a href={`https://openlibrary.org/works/${b.ol_key}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: "#0a3d62", color: "#fff" }}>Open Library</a>
              </div>
              <div className="mt-1.5 text-[14px] font-semibold text-ink-dim">{b.author || "Unknown author"}</div>
              {b.description && <p className="mt-3 max-h-[180px] overflow-y-auto text-[13px] leading-relaxed text-ink-dim">{b.description}</p>}
              {b.subjects && b.subjects.length > 0 && (
                <div className="mt-3 flex flex-wrap gap-1.5">{b.subjects.slice(0, 8).map((s) => <span key={s} className="rounded-full px-2 py-0.5 text-[11px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{s}</span>)}</div>
              )}
              <div className="mt-4 flex flex-wrap items-center gap-4">
                <ProfileSelector book={b} onChange={load} />
              </div>
              <Toolbar book={b} onChange={load} flash={flash} />
            </div>
          </div>
        </div>
      </div>

      <div className="mx-auto w-full max-w-[1100px] px-4 pb-10 sm:px-6">
        <h2 className="m-0 mb-3 text-[14px] font-bold">Editions</h2>
        <div className="flex flex-col gap-2.5">
          <EditionPanel label="Ebook" file={b.ebook} wanted={b.want_ebook} bookId={b.id} kind="ebook" onChange={load} flash={flash} />
          <EditionPanel label="Audiobook" file={b.audiobook} wanted={b.want_audiobook} bookId={b.id} kind="audiobook" onChange={load} flash={flash} />
        </div>
      </div>

      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

// Poster shows the cover with a hover "Edit cover" affordance that opens the picker.
function Poster({ book, onChange, flash }: { book: Book; onChange: () => void; flash: (m: string) => void }) {
  const [picking, setPicking] = useState(false);
  return (
    <div className="flex-none">
      <div className="group relative w-[180px] overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", aspectRatio: "2/3", background: "var(--panel-2)" }}>
        {book.cover_url ? (
          <img src={book.cover_url} alt={book.title} className="h-full w-full object-cover" />
        ) : (
          <div className="flex h-full items-center justify-center p-3 text-center text-[13px] font-bold text-white" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>{book.title}</div>
        )}
        <button onClick={() => setPicking(true)} className="absolute inset-0 flex items-end justify-center opacity-0 transition-opacity duration-150 group-hover:opacity-100" style={{ background: "linear-gradient(180deg, transparent 45%, rgba(0,0,0,.68))" }}>
          <span className="mb-3 inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-semibold text-white" style={{ background: "rgba(0,0,0,.55)", border: "1px solid rgba(255,255,255,.28)" }}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none"><path d="M4 20h4l10-10-4-4L4 16v4z" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
            Edit cover
          </span>
        </button>
      </div>
      {picking && <CoverPickerModal book={book} onClose={() => setPicking(false)} onChange={onChange} flash={flash} />}
    </div>
  );
}

// CoverPickerModal lets the user pick from searched covers (Open Library editions + Google
// Books) or upload a custom image.
function CoverPickerModal({ book, onClose, onChange, flash }: { book: Book; onClose: () => void; onChange: () => void; flash: (m: string) => void }) {
  const [covers, setCovers] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    api.bookCovers(book.id).then(setCovers).catch((e: Error) => { setError(e.message); setCovers([]); });
  }, [book.id]);

  const choose = async (url: string) => {
    setSaving(url); setError(null);
    try { await api.setBookCover(book.id, url); onChange(); onClose(); flash("Cover updated."); }
    catch (e) { setError((e as Error).message); setSaving(null); }
  };
  const upload = async (file: File) => {
    setSaving("upload"); setError(null);
    try { await api.uploadBookCover(book.id, file); onChange(); onClose(); flash("Custom cover uploaded."); }
    catch (e) { setError((e as Error).message); setSaving(null); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-10 w-full max-w-[760px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Choose a cover</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">Covers from Open Library editions and Google Books — or upload your own.</p>
        {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="mb-3">
          <input ref={fileRef} type="file" accept="image/jpeg,image/png,image/webp,image/gif" className="hidden" onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(f); e.target.value = ""; }} />
          <button onClick={() => fileRef.current?.click()} disabled={saving !== null} className="inline-flex items-center gap-1.5 rounded-lg px-3 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
            {saving === "upload" ? "Uploading…" : "⭱ Upload custom cover"}
          </button>
        </div>
        <div className="thin-scroll max-h-[58vh] overflow-y-auto">
          {covers === null ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">Finding covers…</div>
          ) : covers.length === 0 ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">No alternate covers found — you can still upload your own above.</div>
          ) : (
            <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5">
              {covers.map((url) => {
                const current = url === book.cover_url;
                return (
                  <button key={url} onClick={() => choose(url)} disabled={saving !== null} className="relative overflow-hidden rounded-lg disabled:opacity-60" style={{ border: current ? "2px solid var(--accent)" : "1px solid var(--line)", aspectRatio: "2/3", background: "var(--panel-2)" }}>
                    <img src={url} alt="" loading="lazy" className="h-full w-full object-cover" onError={(e) => { const btn = e.currentTarget.closest("button"); if (btn) (btn as HTMLElement).style.display = "none"; }} />
                    {saving === url && <div className="absolute inset-0 grid place-items-center text-[11px] font-semibold text-white" style={{ background: "rgba(0,0,0,.5)" }}>Saving…</div>}
                    {current && <span className="absolute right-1 top-1 rounded px-1.5 py-0.5 text-[9px] font-bold text-white" style={{ background: "var(--accent)" }}>Current</span>}
                  </button>
                );
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function EditionPanel({ label, file, wanted, bookId, kind, onChange, flash }: { label: string; file?: BookFile; wanted: boolean; bookId: number; kind: "ebook" | "audiobook"; onChange: () => void; flash: (m: string) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!file && !wanted) {
    return (
      <div className="flex items-center gap-3 rounded-xl px-4 py-3 opacity-60" style={{ border: "1px dashed var(--line)" }}>
        <span className="font-mono text-[10px] uppercase text-ink-faint">{label}</span>
        <span className="text-[12px] text-ink-faint">Not included in this book's format profile.</span>
      </div>
    );
  }
  if (!file) {
    return (
      <div className="flex items-center gap-3 rounded-xl px-4 py-3" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
        <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }}>{label}</span>
        <span className="text-[12px]" style={{ color: "var(--avoid)" }}>Wanted — no file yet. Arrmada is searching for the {label.toLowerCase()}.</span>
      </div>
    );
  }
  const del = async () => {
    setBusy(true);
    try { await api.deleteBookFile(bookId, kind); onChange(); } catch (e) { flash((e as Error).message); } finally { setBusy(false); setConfirming(false); }
  };
  const multi = file.file_count > 1;
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--good)", background: "var(--panel)" }}>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--good-soft, rgba(90,140,90,.14))", color: "var(--good)" }}>{label}</span>
            <span className="rounded px-2 py-0.5 text-[11px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{file.format}</span>
            {multi && <span className="rounded px-2 py-0.5 text-[11px] font-semibold" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{file.file_count} files</span>}
            <span className="font-mono text-[12px] text-ink-dim">{fmtSize(file.size_bytes)}</span>
          </div>
          <div className="mt-1.5 break-all font-mono text-[11.5px] text-ink-faint">{file.path}</div>
          {multi && <FileList bookId={bookId} kind={kind} count={file.file_count} />}
        </div>
        <div className="flex flex-none flex-col items-end gap-1.5">
          {multi && kind === "audiobook" && <MergeButton bookId={bookId} onDone={onChange} flash={flash} />}
          {confirming ? (
            <div className="flex items-center gap-2">
              <button onClick={del} disabled={busy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>{busy ? "Deleting…" : "Delete"}</button>
              <button onClick={() => setConfirming(false)} className="rounded-lg px-3 py-1.5 text-[11.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
            </div>
          ) : (
            <button onClick={() => setConfirming(true)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{multi ? "Delete all" : "Delete file"}</button>
          )}
        </div>
      </div>
    </div>
  );
}

function FileList({ bookId, kind, count }: { bookId: number; kind: "ebook" | "audiobook"; count: number }) {
  const [open, setOpen] = useState(false);
  const [files, setFiles] = useState<BookFileEntry[] | null>(null);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next && files === null) api.bookEditionFiles(bookId, kind).then(setFiles).catch(() => setFiles([]));
  };
  return (
    <div className="mt-2">
      <button onClick={toggle} className="flex items-center gap-1.5 text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" style={{ transform: open ? "rotate(90deg)" : "none", transition: "transform .15s" }}><path d="M9 6l6 6-6 6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        {open ? "Hide files" : `Show all ${count} files`}
      </button>
      {open && (
        <div className="thin-scroll mt-2 max-h-[240px] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {files === null ? (
            <div className="p-3 text-[11.5px] text-ink-dim">Loading…</div>
          ) : (
            files.map((f, i) => (
              <div key={i} className="flex items-center justify-between gap-3 px-3 py-1.5 text-[11.5px]" style={{ borderBottom: i < files.length - 1 ? "1px solid var(--line-soft)" : "none" }}>
                <span className="min-w-0 flex-1 truncate font-mono text-ink-dim" title={f.name}>{f.name}</span>
                <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtSize(f.size_bytes)}</span>
              </div>
            ))
          )}
        </div>
      )}
    </div>
  );
}

function MergeButton({ bookId, onDone, flash }: { bookId: number; onDone: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState(false);
  const merge = async () => {
    setBusy(true);
    try {
      await api.mergeAudiobook(bookId);
      flash("Combining into a single chapterized .m4b — this runs in the background and may take a while.");
      // Poll for the merge to finish (edition collapses to 1 file).
      let ticks = 0;
      const t = setInterval(() => { onDone(); if (++ticks >= 40) clearInterval(t); }, 5000);
    } catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <button onClick={merge} disabled={busy} title="Merge all chapter files into one chapterized .m4b (needs ffmpeg)" className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
      {busy ? "Merging…" : "⧉ Combine into one file"}
    </button>
  );
}

function Toolbar({ book, onChange, flash }: { book: Book; onChange: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [showSearch, setShowSearch] = useState(false);
  const [showImport, setShowImport] = useState(false);
  const [showEdit, setShowEdit] = useState(false);

  const run = async (key: string, fn: () => Promise<void>) => { setBusy(key); try { await fn(); } catch (e) { flash((e as Error).message); } finally { setBusy(null); } };
  const btn = "rounded-lg px-3 py-2 text-[12.5px] font-semibold disabled:opacity-50";
  const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

  const rename = async () => {
    const res = await api.renameBook(book.id);
    flash(res.renamed > 0 ? `Renamed ${res.renamed} file${res.renamed === 1 ? "" : "s"}.` : "Files already named correctly.");
    onChange();
  };

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <button role="switch" aria-checked={book.monitored} disabled={busy !== null} onClick={() => run("monitor", async () => { await api.setBookMonitored(book.id, !book.monitored); onChange(); })} className="inline-flex items-center gap-2 text-[12.5px] font-semibold disabled:opacity-50">
          <span className="relative inline-block h-[22px] w-[38px] rounded-full transition-colors" style={{ background: book.monitored ? "var(--accent)" : "var(--line)" }}>
            <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: book.monitored ? "19px" : "3px" }} />
          </span>
          <span style={{ color: book.monitored ? "var(--ink)" : "var(--ink-dim)" }}>{book.monitored ? "Monitored" : "Monitor"}</span>
        </button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => run("refresh", async () => { await api.refreshBook(book.id); onChange(); flash("Refreshed metadata and rescanned disk."); })}>{busy === "refresh" ? "Refreshing…" : "Refresh & rescan"}</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowSearch(true)}>Search indexers</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowImport(true)}>Manual import</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => run("rename", rename)}>{busy === "rename" ? "Renaming…" : "Rename"}</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowEdit(true)} title="Manually fix title/author/year when the metadata providers got it wrong">Edit metadata</button>
        <DeleteButton book={book} />
      </div>
      {showEdit && <EditMetadataModal book={book} onClose={() => setShowEdit(false)} onSaved={() => { onChange(); flash("Metadata updated."); }} />}
      {showSearch && (
        <ReleaseSearchModal
          title={`Search indexers — ${book.title}`}
          subtitle="Ebook and audiobook releases. Pick one to grab."
          fetchReleases={() => api.bookReleases(book.id)}
          onGrab={async (rel) => { await api.grabBook(book.id, { indexer: rel.indexer, download_url: rel.download_url, title: rel.title }); onChange(); }}
          onClose={() => setShowSearch(false)}
        />
      )}
      {showImport && <ManualImportModal book={book} onClose={() => setShowImport(false)} onImported={() => { onChange(); flash("Imported."); }} />}
    </>
  );
}

function ManualImportModal({ book, onClose, onImported }: { book: Book; onClose: () => void; onImported: () => void }) {
  const [cands, setCands] = useState<BookImportCandidate[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState<string | null>(null);

  const load = () => api.bookManualImportList(book.id).then((r) => setCands(r.candidates)).catch((e: Error) => setError(e.message));
  useEffect(() => { load(); }, [book.id]);

  const doImport = async (path: string) => {
    setImporting(path); setError(null);
    try { await api.bookManualImport(book.id, path); onImported(); load(); } catch (e) { setError((e as Error).message); } finally { setImporting(null); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[680px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Manual import</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">Pick a book file on disk to import as <b>{book.title}</b>. Ebook and audiobook files are assigned to the right edition automatically.</p>
        {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="thin-scroll max-h-[52vh] overflow-y-auto">
          {cands === null ? (
            <div className="p-6 text-center text-[12.5px] text-ink-dim">Scanning…</div>
          ) : cands.length === 0 ? (
            <div className="p-6 text-center text-[12.5px] text-ink-dim">No ebook or audiobook files found in the downloads folder.</div>
          ) : (
            cands.map((c) => (
              <button key={c.path} onClick={() => doImport(c.path)} disabled={importing !== null} className="flex w-full items-center gap-3 rounded-lg p-2.5 text-left transition-colors hover:bg-[var(--panel-2)]">
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[12.5px] font-medium" title={c.filename}>{c.filename}</div>
                  <div className="mt-0.5 flex items-center gap-3 font-mono text-[10.5px] text-ink-faint">
                    <span className="uppercase" style={{ color: c.edition === "audiobook" ? "var(--accent)" : "var(--good)" }}>{c.edition}</span>
                    <span>{c.format}</span>
                    <span>{fmtSize(c.size_bytes)}</span>
                  </div>
                </div>
                <span className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{importing === c.path ? "Importing…" : "Import"}</span>
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  );
}

function ProfileSelector({ book, onChange }: { book: Book; onChange: () => void }) {
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  useEffect(() => { api.qualityProfiles("book").then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })))).catch(() => {}); }, []);
  const change = async (p: string) => { if (p === book.quality_profile) return; await api.setBookProfile(book.id, p); onChange(); };
  const opts = [...profiles];
  if (book.quality_profile && !opts.some((o) => o.key === book.quality_profile)) opts.unshift({ key: book.quality_profile, name: book.quality_profile === "n/a" ? "Not set" : book.quality_profile });
  return (
    <div className="flex items-center gap-2">
      <span className="font-mono text-[10.5px] uppercase text-ink-faint">Format profile</span>
      <select value={book.quality_profile} onChange={(e) => change(e.target.value)} className="rounded-lg px-2.5 py-1.5 text-[12px] font-medium" style={{ background: "var(--accent-soft)", border: "1px solid var(--line)", color: "var(--accent)" }}>
        {opts.map((p) => <option key={p.key} value={p.key} style={{ background: "var(--panel)", color: "var(--ink)" }}>{p.name}</option>)}
      </select>
    </div>
  );
}

function DeleteButton({ book }: { book: Book }) {
  const [open, setOpen] = useState(false);
  const [deleteFiles, setDeleteFiles] = useState(true);
  const [busy, setBusy] = useState(false);
  const remove = async () => {
    setBusy(true);
    try { await api.deleteBook(book.id, deleteFiles); window.location.href = "/books"; } finally { setBusy(false); }
  };
  return (
    <>
      <button onClick={() => setOpen(true)} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete</button>
      {open && (
        <div className="fixed inset-0 z-50 grid place-items-center p-6" style={{ background: "rgba(0,0,0,.6)" }} onClick={() => setOpen(false)}>
          <div className="w-full max-w-[440px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
            <h2 className="m-0 text-[15px] font-bold">Remove “{book.title}”?</h2>
            <p className="mt-1 text-[12px] text-ink-dim">It'll be removed from your library and Arrmada will stop monitoring it.</p>
            <label className="mt-4 flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: book.has_file ? "pointer" : "default", opacity: book.has_file ? 1 : 0.6 }}>
              <input type="checkbox" checked={deleteFiles} disabled={!book.has_file} onChange={(e) => setDeleteFiles(e.target.checked)} className="mt-0.5" />
              <span>
                <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
                <span className="mt-0.5 block text-[11px] text-ink-faint">{book.has_file ? "Moves the ebook and/or audiobook file(s) to the recycle bin." : "This book has no files on disk."}</span>
              </span>
            </label>
            <div className="mt-4 flex justify-end gap-2.5">
              <button onClick={() => setOpen(false)} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
              <button onClick={remove} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>{busy ? "Removing…" : deleteFiles ? "Remove + delete files" : "Remove"}</button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return <><PageHeader title="Books" crumb="Library / Books" /><div className="mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">{children}</div></>;
}

// EditMetadataModal lets a user manually correct a book's title/author/year/cover/overview when
// the metadata providers get it wrong (the Books "manual override" escape hatch).
function EditMetadataModal({ book, onClose, onSaved }: { book: Book; onClose: () => void; onSaved: () => void }) {
  const [title, setTitle] = useState(book.title);
  const [author, setAuthor] = useState(book.author);
  const [year, setYear] = useState(String(book.year || ""));
  const [cover, setCover] = useState(book.cover_url ?? "");
  const [overview, setOverview] = useState(book.description ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const inp = "w-full rounded-lg px-2.5 py-1.5 text-[12.5px]";
  const inpStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
  const lbl = "mb-1 block text-[10.5px] font-semibold uppercase tracking-wide text-ink-faint";

  const save = async () => {
    if (!title.trim() || !author.trim()) { setErr("Title and author are required."); return; }
    setBusy(true);
    try {
      await api.overrideBookMetadata(book.id, { title: title.trim(), author: author.trim(), year: Number(year) || 0, overview, cover_url: cover.trim() });
      onSaved(); onClose();
    } catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-6" style={{ background: "rgba(0,0,0,.6)" }} onClick={onClose}>
      <div className="w-full max-w-[480px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <h2 className="m-0 text-[15px] font-bold">Edit metadata</h2>
        <p className="mt-1 mb-3 text-[11.5px] text-ink-dim">Manually fix the details when a provider got them wrong. This overrides what was fetched.</p>
        <div className="flex flex-col gap-3">
          <div><span className={lbl}>Title</span><input value={title} onChange={(e) => setTitle(e.target.value)} className={inp} style={inpStyle} /></div>
          <div className="flex gap-3">
            <div className="flex-1"><span className={lbl}>Author</span><input value={author} onChange={(e) => setAuthor(e.target.value)} className={inp} style={inpStyle} /></div>
            <div className="w-[88px]"><span className={lbl}>Year</span><input type="number" value={year} onChange={(e) => setYear(e.target.value)} className={inp} style={inpStyle} /></div>
          </div>
          <div><span className={lbl}>Cover URL</span><input value={cover} onChange={(e) => setCover(e.target.value)} placeholder="https://…" className={inp} style={inpStyle} /></div>
          <div><span className={lbl}>Overview</span><textarea value={overview} onChange={(e) => setOverview(e.target.value)} rows={4} className={inp} style={inpStyle} /></div>
        </div>
        {err && <p className="mt-2 text-[11.5px]" style={{ color: "var(--reject)" }}>{err}</p>}
        <div className="mt-4 flex justify-end gap-2.5">
          <button onClick={onClose} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={save} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save"}</button>
        </div>
      </div>
    </div>
  );
}
