import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type LibraryPaths, type BrowseResult } from "../lib/api";

// Library — System page for pointing each library at a folder, with an in-app folder picker.
// Mount your media into the container (see the installer), then browse + select here.
type PathKey = keyof LibraryPaths;
const ROWS: { key: PathKey; label: string; hint: string; scan?: () => Promise<unknown> }[] = [
  { key: "movies", label: "Movies", hint: "folder of movie subfolders", scan: () => api.scanLibrary() },
  { key: "tv", label: "TV Shows", hint: "folder of show subfolders", scan: () => api.scanSeries() },
  { key: "ebooks", label: "Ebooks", hint: "folder of book subfolders", scan: () => api.scanBooks() },
  { key: "audiobooks", label: "Audiobooks", hint: "may share the ebooks folder", scan: () => api.scanBooks() },
  { key: "downloads", label: "Downloads", hint: "where the download client saves completed files" },
];

export function Library() {
  const [paths, setPaths] = useState<LibraryPaths | null>(null);
  const [draft, setDraft] = useState<LibraryPaths | null>(null);
  const [picking, setPicking] = useState<PathKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  useEffect(() => { api.libraryPaths().then((p) => { setPaths(p); setDraft(p); }).catch(() => flash("Could not load library paths")); }, []);
  if (!draft) return <><PageHeader title="Library" crumb="System / Library" /><div className="mx-auto max-w-[820px] px-4 py-6 text-[12.5px] text-ink-dim">Loading…</div></>;

  const dirty = paths && (Object.keys(draft) as PathKey[]).some((k) => draft[k] !== paths[k]);
  const save = async () => {
    setBusy(true);
    try { const p = await api.setLibraryPaths(draft); setPaths(p); setDraft(p); flash("Saved"); }
    catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };
  const scan = async (row: typeof ROWS[number]) => {
    if (!row.scan) return;
    if (dirty) { flash("Save your folders first, then scan."); return; }
    try { await row.scan(); flash(`Scanning ${row.label}… it'll appear in the library shortly.`); }
    catch (e) { flash((e as Error).message); }
  };

  return (
    <>
      <PageHeader title="Library" crumb="System / Library" />
      <div className="mx-auto w-full max-w-[820px] px-4 py-6 sm:px-6">
        <p className="mb-5 max-w-[64ch] text-[12.5px] text-ink-dim">Point each library at a folder inside your mounted media. Use <b>Browse</b> to pick from the folders Arrmada can see, then <b>Scan</b> to catalog what's there. Ebooks and audiobooks can share one folder.</p>

        <div className="flex flex-col gap-2.5">
          {ROWS.map((row) => (
            <div key={row.key} className="rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
              <div className="flex items-center justify-between gap-3">
                <div>
                  <div className="text-[13px] font-semibold">{row.label}</div>
                  <div className="text-[10.5px] text-ink-faint">{row.hint}</div>
                </div>
                {row.scan && <button onClick={() => scan(row)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Scan</button>}
              </div>
              <div className="mt-2 flex gap-2">
                <input
                  value={draft[row.key]}
                  onChange={(e) => setDraft({ ...draft, [row.key]: e.target.value })}
                  placeholder="/storage/media/…"
                  className="flex-1 rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]"
                  style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
                />
                <button onClick={() => setPicking(row.key)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Browse…</button>
              </div>
            </div>
          ))}
        </div>

        <div className="mt-4 flex items-center justify-end gap-3">
          {dirty && <span className="text-[11.5px] text-ink-faint">Unsaved changes</span>}
          <button onClick={save} disabled={!dirty || busy} className="rounded-lg px-4 py-2 text-[13px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save folders"}</button>
        </div>
      </div>

      {picking && (
        <FolderPicker
          initial={draft[picking] || undefined}
          onClose={() => setPicking(null)}
          onSelect={(p) => { setDraft({ ...draft, [picking]: p }); setPicking(null); }}
        />
      )}
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

function FolderPicker({ initial, onClose, onSelect }: { initial?: string; onClose: () => void; onSelect: (path: string) => void }) {
  const [data, setData] = useState<BrowseResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const go = (path?: string) => { setErr(null); api.browseFolders(path).then(setData).catch((e) => setErr((e as Error).message)); };
  useEffect(() => { go(initial); /* eslint-disable-next-line */ }, []);

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Select a folder</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>

        <div className="mb-2 flex items-center gap-2">
          <button onClick={() => data && go(data.parent)} disabled={!data || data.path === "/"} className="rounded-lg px-2.5 py-1.5 text-[12px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} title="Up one level">↑</button>
          <div className="min-w-0 flex-1 truncate rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{data?.path ?? "…"}</div>
        </div>

        {err && <div className="mb-2 rounded-lg p-2.5 text-[11.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{err}</div>}

        <div className="thin-scroll max-h-[46vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {!data ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">Loading…</div>
          ) : data.dirs.length === 0 ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">No sub-folders here.</div>
          ) : data.dirs.map((d) => (
            <button key={d.path} onClick={() => go(d.path)} className="flex w-full items-center gap-2 px-3 py-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <span style={{ color: "var(--accent)" }}>📁</span>
              <span className="truncate">{d.name}</span>
            </button>
          ))}
        </div>

        <div className="mt-3 flex items-center justify-between gap-2">
          <span className="text-[10.5px] text-ink-faint">Navigate into the folder you want, then select it.</span>
          <button onClick={() => data && onSelect(data.path)} disabled={!data} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Select this folder</button>
        </div>
      </div>
    </div>
  );
}
