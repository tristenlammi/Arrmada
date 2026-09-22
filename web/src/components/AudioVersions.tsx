import { useState } from "react";
import { api, type AudioVersion, type Book } from "../lib/api";
import { BookReleaseModal } from "./BookReleaseModal";

// Extra audiobook versions of a book: a full-cast GraphicAudio production beside the
// standard narration, a second narrator, anything the user names. Each version has
// the words a release must mention to count as it; Arrmada searches for it, files it
// as "<Title> (<Label>)" next to the standard audiobook, and keeps the standard one
// from taking its releases.

function fmtSize(bytes?: number): string {
  if (!bytes) return "";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, n = bytes;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${u[i]}`;
}

// Presets fill the form; everything stays editable. The terms are matched against a
// release's name, narrator, series and description, as whole words.
const PRESETS: { label: string; terms: string[]; hint: string }[] = [
  { label: "Full cast", terms: ["GraphicAudio", "Graphic Audio", "full cast", "full-cast", "dramatized", "dramatised", "dramatization", "dramatisation", "audio drama"], hint: "GraphicAudio and other dramatised productions" },
  { label: "BBC Radio", terms: ["BBC Radio", "BBC radio drama", "radio dramatisation"], hint: "BBC radio adaptations" },
  { label: "Narrator", terms: [], hint: "A specific reading — put the narrator's name in the words" },
];

const parseTerms = (s: string) => s.split(",").map((t) => t.trim()).filter(Boolean);

export function AddAudioVersion({ book, onAdded, flash }: { book: Book; onAdded: () => void; flash: (m: string) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className="flex items-center gap-2 rounded-xl px-4 py-2.5 text-left text-[12px] font-semibold text-ink-dim hover:text-[var(--ink)]"
        style={{ border: "1px dashed var(--line)" }}
        title="Keep another audiobook of this book alongside the standard one — a full-cast production, a different narrator"
      >
        <span className="text-[14px] leading-none">+</span> Add an audiobook version
        <span className="font-normal text-ink-faint">— e.g. a full-cast GraphicAudio production next to the standard narration</span>
      </button>
      {open && (
        <VersionForm
          book={book}
          onClose={() => setOpen(false)}
          onSaved={(v) => {
            setOpen(false);
            onAdded();
            flash(v.monitored && v.terms.length > 0 ? `Added “${v.label}” — searching for it now.` : `Added “${v.label}”.`);
          }}
        />
      )}
    </>
  );
}

function VersionForm({ book, existing, onClose, onSaved }: { book: Book; existing?: AudioVersion; onClose: () => void; onSaved: (v: AudioVersion) => void }) {
  const [label, setLabel] = useState(existing?.label ?? "");
  const [terms, setTerms] = useState((existing?.terms ?? []).join(", "));
  const [monitored, setMonitored] = useState(existing?.monitored ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    setBusy(true); setError(null);
    try {
      const body = { label: label.trim(), terms: parseTerms(terms), monitored };
      const v = existing ? await api.updateAudioVersion(book.id, existing.id, body) : await api.addAudioVersion(book.id, body);
      onSaved(v);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const input = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
  const noTerms = parseTerms(terms).length === 0;

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-16 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">{existing ? `Edit “${existing.label}”` : "Add an audiobook version"}</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">
          Another audiobook of <b>{book.title}</b>, kept next to the standard one. It's filed as <span className="font-mono">{book.title} ({label.trim() || "Name"})</span>, which Audiobookshelf shows as its own book.
        </p>

        {!existing && (
          <div className="mb-3 flex flex-wrap gap-1.5">
            {PRESETS.map((p) => (
              <button key={p.label} onClick={() => { setLabel(p.label); setTerms(p.terms.join(", ")); }} title={p.hint} className="rounded-full px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: label === p.label ? "var(--accent-soft)" : "var(--panel-2)", color: label === p.label ? "var(--accent)" : "var(--ink-dim)" }}>
                {p.label}
              </button>
            ))}
          </div>
        )}

        <label className="mb-2.5 block text-[11.5px] font-semibold text-ink-dim">
          Name
          <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Full cast" maxLength={60} className="mt-1 block w-full rounded-lg px-3 py-2 text-[13px] font-normal" style={input} />
        </label>
        <label className="mb-1 block text-[11.5px] font-semibold text-ink-dim">
          Words a release must mention <span className="font-normal text-ink-faint">(comma-separated, any one is enough)</span>
          <textarea value={terms} onChange={(e) => setTerms(e.target.value)} rows={2} placeholder="GraphicAudio, full cast, dramatized" className="mt-1 block w-full rounded-lg px-3 py-2 text-[12.5px] font-normal" style={input} />
        </label>
        <p className="mb-3 text-[11px] text-ink-faint">
          Matched as whole words against the release name, narrator, series and description. While this version exists, the standard audiobook won't take releases that match these words.
          {noTerms && <span style={{ color: "var(--avoid)" }}> With no words, this version is only filled by hand — pick it when you grab, upload or import.</span>}
        </p>
        <label className="mb-4 flex items-center gap-2 text-[12.5px]">
          <input type="checkbox" checked={monitored} onChange={(e) => setMonitored(e.target.checked)} />
          Search for it automatically
        </label>
        {existing && label.trim() !== existing.label && existing.file && (
          <p className="mb-3 text-[11px]" style={{ color: "var(--avoid)" }}>Renaming doesn't move the files already on disk.</p>
        )}
        {error && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="flex justify-end gap-2">
          <button onClick={onClose} className="rounded-lg px-3.5 py-2 text-[12.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={save} disabled={busy || !label.trim()} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{busy ? "Saving…" : existing ? "Save" : "Add version"}</button>
        </div>
      </div>
    </div>
  );
}

export function AudioVersionPanel({ book, v, onChange, flash }: { book: Book; v: AudioVersion; onChange: () => void; flash: (m: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const [confirm, setConfirm] = useState<null | "file" | "remove">(null);
  const [busy, setBusy] = useState<string | null>(null);

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try { await fn(); } catch (e) { flash((e as Error).message); } finally { setBusy(null); setConfirm(null); }
  };
  const search = () => run("search", async () => {
    const r = await api.searchAudioVersion(book.id, v.id);
    flash(r.grabbed ? `Grabbed a release for “${v.label}”.` : `Nothing found for “${v.label}” right now.`);
    onChange();
  });
  const toggleMonitor = () => run("monitor", async () => { await api.updateAudioVersion(book.id, v.id, { monitored: !v.monitored }); onChange(); });

  const has = !!v.file;
  const tone = has ? "var(--good)" : v.monitored ? "var(--avoid)" : "var(--ink-faint)";
  const soft = has ? "var(--good-soft, rgba(90,140,90,.14))" : v.monitored ? "var(--avoid-soft)" : "var(--panel-2)";
  const small = "rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50";
  const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

  return (
    <div className="rounded-xl p-4" style={{ border: `1px solid ${has ? "var(--good)" : "var(--line)"}`, background: "var(--panel)" }}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: soft, color: tone }}>Audiobook · {v.label}</span>
            {has && v.file && (
              <>
                <span className="rounded px-2 py-0.5 text-[11px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{v.file.format}</span>
                {v.file.file_count > 1 && <span className="rounded px-2 py-0.5 text-[11px] font-semibold" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{v.file.file_count} files</span>}
                <span className="font-mono text-[12px] text-ink-dim">{fmtSize(v.file.size_bytes)}</span>
              </>
            )}
          </div>
          {has && v.file ? (
            <div className="mt-1.5 break-all font-mono text-[11.5px] text-ink-faint">{v.file.path}</div>
          ) : (
            <div className="mt-1.5 text-[12px]" style={{ color: tone }}>
              {v.terms.length === 0 ? "Not searched automatically without search words — use Search indexers to pick a release for it." : v.monitored ? "Wanted — Arrmada is searching for it." : "Not searched automatically."}
            </div>
          )}
          {v.terms.length > 0 && (
            <div className="mt-2 flex flex-wrap items-center gap-1">
              <span className="text-[10.5px] text-ink-faint">Matches</span>
              {v.terms.map((t) => <span key={t} className="rounded-full px-2 py-0.5 text-[10.5px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{t}</span>)}
            </div>
          )}
        </div>
        <div className="flex flex-none flex-wrap items-center justify-end gap-1.5">
          {confirm ? (
            <>
              <span className="text-[11.5px] text-ink-dim">{confirm === "file" ? "Delete this version's files?" : has ? "Remove the version and its files?" : "Remove this version?"}</span>
              <button
                disabled={busy !== null}
                onClick={() => run("delete", async () => {
                  if (confirm === "file") await api.deleteAudioVersionFile(book.id, v.id);
                  else await api.deleteAudioVersion(book.id, v.id, true);
                  onChange();
                })}
                className={small}
                style={{ background: "var(--reject)", color: "#fff" }}
              >
                {busy === "delete" ? "Deleting…" : confirm === "file" ? "Delete files" : "Remove"}
              </button>
              <button onClick={() => setConfirm(null)} className={small} style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
            </>
          ) : (
            <>
              <button disabled={busy !== null} onClick={() => setBrowsing(true)} className={small} style={ghost} title="Browse audiobook releases on your indexers and pick one for this version">Search indexers</button>
              {!has && v.terms.length > 0 && <button disabled={busy !== null} onClick={search} className={small} style={ghost} title="Let Arrmada pick and grab the best release matching this version's words">{busy === "search" ? "Searching…" : "Auto search"}</button>}
              <button disabled={busy !== null} onClick={toggleMonitor} className={small} style={ghost} title="Whether the automatic searches look for this version">{v.monitored ? "Monitored" : "Unmonitored"}</button>
              <button disabled={busy !== null} onClick={() => setEditing(true)} className={small} style={ghost}>Edit</button>
              {has && <button disabled={busy !== null} onClick={() => setConfirm("file")} className={small} style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete files</button>}
              <button disabled={busy !== null} onClick={() => setConfirm("remove")} className={small} style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Remove</button>
            </>
          )}
        </div>
      </div>
      {browsing && (
        <BookReleaseModal
          title={`Search indexers — ${book.title} (${v.label})`}
          fetchReleases={() => api.bookReleases(book.id)}
          targets={(book.audio_versions ?? []).map((x) => ({ id: x.id, label: x.label }))}
          defaultTarget={v.id}
          audioOnly
          onGrab={async (rel, versionId) => { await api.grabBook(book.id, { indexer: rel.indexer, download_url: rel.download_url, title: rel.title, version_id: versionId }); onChange(); }}
          onClose={() => setBrowsing(false)}
        />
      )}
      {editing && <VersionForm book={book} existing={v} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); onChange(); flash("Version updated."); }} />}
    </div>
  );
}
