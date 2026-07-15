import { useEffect, useMemo, useState } from "react";
import type { RankedRelease, ReleaseList } from "../lib/api";

// BookReleaseModal is the book-specific interactive search: results split into
// Audiobooks / Ebooks tabs, each showing the raw release title (so the narrator
// and other detail a tracker puts in the name are visible), with the best match
// for the book's quality profile marked ★ Recommended.
export function BookReleaseModal({
  title,
  fetchReleases,
  onGrab,
  onClose,
}: {
  title: string;
  fetchReleases: () => Promise<ReleaseList>;
  onGrab: (rel: RankedRelease) => Promise<void>;
  onClose: () => void;
}) {
  const [list, setList] = useState<ReleaseList | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [grabbed, setGrabbed] = useState<Set<string>>(new Set());
  const [tab, setTab] = useState<"audiobook" | "ebook">("audiobook");

  const run = async () => {
    setLoading(true);
    setError(null);
    try {
      setList(await fetchReleases());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const releases = list?.releases ?? [];
  const ebooks = useMemo(() => releases.filter((r) => r.edition === "ebook"), [releases]);
  const audiobooks = useMemo(() => releases.filter((r) => r.edition === "audiobook"), [releases]);
  // Land on whichever tab actually has results (audiobooks preferred).
  useEffect(() => {
    if (!loading && audiobooks.length === 0 && ebooks.length > 0) setTab("ebook");
  }, [loading, audiobooks.length, ebooks.length]);

  const rows = tab === "ebook" ? ebooks : audiobooks;

  const grab = async (rel: RankedRelease) => {
    setBusy(rel.title);
    setError(null);
    try {
      await onGrab(rel);
      setGrabbed((s) => new Set(s).add(rel.title));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const TabBtn = ({ k, label, count }: { k: "audiobook" | "ebook"; label: string; count: number }) => (
    <button
      onClick={() => setTab(k)}
      className="rounded-lg px-3.5 py-1.5 text-[12px] font-semibold"
      style={{ background: tab === k ? "var(--accent)" : "var(--panel-2)", color: tab === k ? "var(--accent-ink)" : "var(--ink-dim)", border: "1px solid var(--line)" }}
    >
      {label} <span className="font-mono text-[10.5px] opacity-70">{count}</span>
    </button>
  );

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-10 w-full max-w-[760px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="m-0 truncate text-[15px] font-bold">{title}</h2>
            <p className="m-0 mt-0.5 text-[11.5px] text-ink-dim">Best match for your quality profile is marked ★ Recommended.</p>
          </div>
          <div className="flex flex-none items-center gap-2">
            <button onClick={run} disabled={loading} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{loading ? "Searching…" : "Search again"}</button>
            <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
          </div>
        </div>
        {error && <div className="mb-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}
        {!loading && (
          <div className="mb-3 flex items-center gap-2 border-b pb-3" style={{ borderColor: "var(--line)" }}>
            <TabBtn k="audiobook" label="Audiobooks" count={audiobooks.length} />
            <TabBtn k="ebook" label="Ebooks" count={ebooks.length} />
          </div>
        )}
        <div className="thin-scroll max-h-[58vh] overflow-y-auto">
          {loading ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">Searching your indexers…</div>
          ) : rows.length === 0 ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">No {tab === "ebook" ? "ebook" : "audiobook"} releases found on your indexers.</div>
          ) : (
            <div className="flex flex-col gap-2">
              {rows.map((rel) => (
                <BookReleaseRow key={rel.title} rel={rel} busy={busy === rel.title} grabbed={grabbed.has(rel.title)} onGrab={() => grab(rel)} />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function BookReleaseRow({ rel, busy, grabbed, onGrab }: { rel: RankedRelease; busy: boolean; grabbed: boolean; onGrab: () => void }) {
  return (
    <div
      className="rounded-xl p-3"
      style={{
        background: rel.recommended ? "var(--accent-soft)" : "var(--panel-2)",
        border: `1px solid ${rel.recommended ? "var(--accent)" : "var(--line)"}`,
        opacity: rel.eligible ? 1 : 0.6,
      }}
    >
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            {rel.recommended && <span className="rounded px-1.5 py-0.5 text-[9.5px] font-bold uppercase" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>★ Recommended</span>}
            {rel.format && <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--panel)", color: "var(--ink-dim)" }}>{rel.format}</span>}
            {!rel.eligible && <span className="rounded px-1.5 py-0.5 text-[9.5px] uppercase" style={{ background: "var(--panel)", color: "var(--ink-faint)" }}>not in profile</span>}
          </div>
          {/* The raw torrent name — narrator and edition detail live here. */}
          <div className="mt-1 break-words text-[12.5px] font-medium">{rel.title}</div>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[10.5px] text-ink-faint">
            {rel.narrator && <span style={{ color: "var(--accent)" }}>🎙 {rel.narrator}</span>}
            <span>{rel.size_gb.toFixed(2)} GB</span>
            <span>{rel.seeders} seeders</span>
            <span>{rel.indexer}</span>
          </div>
        </div>
        <button
          onClick={onGrab}
          disabled={busy || grabbed}
          className="flex-none rounded-lg px-3.5 py-2 text-[12px] font-semibold disabled:opacity-60"
          style={{ background: grabbed ? "var(--good)" : "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
        >
          {grabbed ? "Grabbed ✓" : busy ? "Grabbing…" : "Grab"}
        </button>
      </div>
    </div>
  );
}
