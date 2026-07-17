import { useRef, useState } from "react";
import { type TorrentPreview } from "../lib/api";

function fmtSize(b: number): string {
  if (!b) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, n = b;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${u[i]}`;
}

// Base64-encode an ArrayBuffer in chunks (avoids the arg-count limit of String.fromCharCode
// on large torrents).
function toBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let bin = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(bin);
}

// UploadTorrentModal lets you drop a .torrent file for a title, preview what it resolves
// to (name / size / files), then grab it straight into the download client — for when you
// found a release yourself (e.g. on a private tracker) and search didn't surface it.
export function UploadTorrentModal({
  what,
  onPreview,
  onGrab,
  onClose,
}: {
  what: string; // the movie/series title, for the heading
  onPreview: (torrentB64: string) => Promise<TorrentPreview>;
  onGrab: (torrentB64: string, filename: string, title: string) => Promise<void>;
  onClose: () => void;
}) {
  const [torrent, setTorrent] = useState<string>("");
  const [filename, setFilename] = useState("");
  const [preview, setPreview] = useState<TorrentPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [grabbed, setGrabbed] = useState(false);
  const [drag, setDrag] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const load = async (f: File) => {
    setError(null); setPreview(null); setGrabbed(false);
    if (!f.name.toLowerCase().endsWith(".torrent")) { setError("That's not a .torrent file."); return; }
    setBusy(true); setFilename(f.name);
    try {
      const b64 = toBase64(await f.arrayBuffer());
      setTorrent(b64);
      setPreview(await onPreview(b64));
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };

  const grab = async () => {
    if (!preview || !torrent) return;
    setBusy(true); setError(null);
    try { await onGrab(torrent, filename, preview.name); setGrabbed(true); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-16 w-full max-w-[620px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Upload torrent — {what}</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">Found a release yourself? Download the .torrent from your tracker (while logged in) and drop it here. Arrmada previews it so you can check it's right, then grabs it into your library.</p>

        <input ref={inputRef} type="file" accept=".torrent" className="hidden" onChange={(e) => { const f = e.target.files?.[0]; if (f) load(f); }} />
        <div
          onClick={() => inputRef.current?.click()}
          onDragOver={(e) => { e.preventDefault(); setDrag(true); }}
          onDragLeave={() => setDrag(false)}
          onDrop={(e) => { e.preventDefault(); setDrag(false); const f = e.dataTransfer.files?.[0]; if (f) load(f); }}
          className="cursor-pointer rounded-xl p-6 text-center transition-colors"
          style={{ border: `1.5px dashed ${drag ? "var(--accent)" : "var(--line)"}`, background: drag ? "var(--accent-soft)" : "var(--panel-2)" }}
        >
          <div className="text-[13px] font-semibold">{filename || "Choose a .torrent file"}</div>
          <div className="mt-0.5 text-[11px] text-ink-faint">{busy && !preview ? "Reading…" : "Click to browse, or drag a .torrent here"}</div>
        </div>

        {error && <div className="mt-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}

        {preview && (
          <div className="mt-3 rounded-lg p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            <div className="text-[13px] font-semibold break-words">{preview.name}</div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-3 font-mono text-[10.5px] text-ink-faint">
              <span>{fmtSize(preview.size_bytes)}</span>
              {preview.files && preview.files.length > 0 && <span>{preview.files.length} file{preview.files.length === 1 ? "" : "s"}</span>}
            </div>
            {preview.files && preview.files.length > 0 && (
              <div className="thin-scroll mt-2 max-h-[160px] overflow-y-auto font-mono text-[10.5px] text-ink-dim">
                {preview.files.slice(0, 60).map((f, i) => (
                  <div key={i} className="flex justify-between gap-3 py-0.5"><span className="min-w-0 truncate" title={f.path}>{f.path}</span><span className="flex-none text-ink-faint">{fmtSize(f.size_bytes)}</span></div>
                ))}
                {preview.files.length > 60 && <div className="text-ink-faint">…and {preview.files.length - 60} more</div>}
              </div>
            )}
            <div className="mt-3 flex items-center gap-3">
              {grabbed ? (
                <span className="text-[12.5px] font-semibold" style={{ color: "var(--good)" }}>✓ Grabbed — it'll appear in Downloads.</span>
              ) : (
                <button onClick={grab} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{busy ? "Grabbing…" : "Grab this"}</button>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
