import { useState } from "react";
import { type LinkPreview } from "../lib/api";

function fmtSize(b: number): string {
  if (!b) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, n = b;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${u[i]}`;
}

// PasteLinkModal lets you drop a magnet or .torrent URL for a title, preview what it
// resolves to (name / size / files), then grab it straight into the download client —
// for when you found a release yourself and search didn't surface it.
export function PasteLinkModal({
  what,
  onPreview,
  onGrab,
  onClose,
}: {
  what: string; // e.g. the movie/series title, for the heading
  onPreview: (link: string) => Promise<LinkPreview>;
  onGrab: (link: string, title: string) => Promise<void>;
  onClose: () => void;
}) {
  const [link, setLink] = useState("");
  const [preview, setPreview] = useState<LinkPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [grabbed, setGrabbed] = useState(false);

  const check = async () => {
    setBusy(true); setError(null); setPreview(null);
    try { setPreview(await onPreview(link.trim())); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };
  const grab = async () => {
    if (!preview) return;
    setBusy(true); setError(null);
    try { await onGrab(link.trim(), preview.name); setGrabbed(true); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-16 w-full max-w-[620px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Paste a link — {what}</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">Found a release yourself? Paste a magnet link or a direct .torrent URL. Arrmada previews it so you can check it's right, then grabs it into your library.</p>

        <div className="flex gap-2">
          <input
            autoFocus
            value={link}
            onChange={(e) => { setLink(e.target.value); setPreview(null); setGrabbed(false); }}
            onKeyDown={(e) => { if (e.key === "Enter" && link.trim()) check(); }}
            placeholder="magnet:?xt=urn:btih:…  or  https://…/file.torrent"
            className="min-w-0 flex-1 rounded-lg px-3 py-2 text-[12.5px]"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
          <button onClick={check} disabled={busy || !link.trim()} className="flex-none rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>{busy && !preview ? "Checking…" : "Preview"}</button>
        </div>

        {error && <div className="mt-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}

        {preview && (
          <div className="mt-3 rounded-lg p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            <div className="text-[13px] font-semibold break-words">{preview.name}</div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-3 font-mono text-[10.5px] text-ink-faint">
              {preview.magnet ? <span>magnet{preview.hash ? ` · ${preview.hash.slice(0, 12)}` : ""}</span> : <span>{fmtSize(preview.size_bytes)}</span>}
              {preview.files && preview.files.length > 0 && <span>{preview.files.length} file{preview.files.length === 1 ? "" : "s"}</span>}
            </div>
            {preview.magnet && <div className="mt-1 text-[10.5px] text-ink-faint">Magnet links don't carry size/file info until downloading starts — the name is your check here.</div>}
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
