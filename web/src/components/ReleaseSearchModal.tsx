import { useEffect, useMemo, useState } from "react";
import type { RankedRelease, ReleaseList } from "../lib/api";

// --- release metadata parsing (resolution + notable features live in summary/title) ---
type SortKey = "best" | "size" | "bitrate" | "seeders" | "smallest";
const SORTS: { key: SortKey; label: string }[] = [
  { key: "best", label: "Best match" },
  { key: "bitrate", label: "Highest bitrate" },
  { key: "size", label: "Largest" },
  { key: "smallest", label: "Smallest" },
  { key: "seeders", label: "Most seeders" },
];
const RES_ORDER = ["4K", "1080p", "720p", "SD"];
function resOf(rel: RankedRelease): string {
  const s = `${rel.summary} ${rel.title}`;
  if (/2160p|\b4k\b|\buhd\b/i.test(s)) return "4K";
  if (/1080p/i.test(s)) return "1080p";
  if (/720p/i.test(s)) return "720p";
  return "SD";
}
const FEATURES: { key: string; label: string; re: RegExp }[] = [
  { key: "dv", label: "Dolby Vision", re: /dolby ?vision|\bdo?vi\b|\bdv\b/i },
  { key: "hdr", label: "HDR", re: /\bhdr\b|hdr10/i },
  { key: "atmos", label: "Atmos", re: /atmos/i },
  { key: "truehd", label: "TrueHD", re: /true ?hd/i },
  { key: "dts", label: "DTS", re: /\bdts(-? ?(hd|x|es|ma))?\b/i },
  { key: "hevc", label: "x265 / HEVC", re: /x265|hevc|h\.? ?265/i },
];
function featsOf(rel: RankedRelease): Set<string> {
  const s = `${rel.summary} ${rel.title}`;
  return new Set(FEATURES.filter((f) => f.re.test(s)).map((f) => f.key));
}
function sortBitrate(r: RankedRelease): number {
  return r.bitrate_mbps ?? r.size_gb; // bitrate ∝ size for one title; fall back to size when absent
}

// ReleaseSearchModal is the shared "search indexers" experience for both movies and
// series. The caller supplies how to fetch releases (scoped however it likes) and how
// to grab one; an optional onBlock enables the blocklist-and-retry action (movies).
export function ReleaseSearchModal({
  title,
  subtitle,
  fetchReleases,
  onGrab,
  onBlock,
  onClose,
}: {
  title: string;
  subtitle?: string;
  fetchReleases: () => Promise<ReleaseList>;
  onGrab: (rel: RankedRelease) => Promise<void>;
  onBlock?: (rel: RankedRelease) => Promise<void>;
  onClose: () => void;
}) {
  const [list, setList] = useState<ReleaseList | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [grabbed, setGrabbed] = useState<Set<string>>(new Set());
  const [sort, setSort] = useState<SortKey>("best");
  const [resFilter, setResFilter] = useState<Set<string>>(new Set());
  const [featFilter, setFeatFilter] = useState<Set<string>>(new Set());

  // Decorate releases with parsed resolution + feature tags once, keeping ranked order as the index.
  const decorated = useMemo(
    () => (list?.releases ?? []).map((rel, i) => ({ rel, i, res: resOf(rel), feats: featsOf(rel) })),
    [list],
  );
  const resAvailable = useMemo(() => RES_ORDER.filter((r) => decorated.some((d) => d.res === r)), [decorated]);
  const featAvailable = useMemo(() => FEATURES.filter((f) => decorated.some((d) => d.feats.has(f.key))), [decorated]);
  const view = useMemo(() => {
    const rows = decorated.filter(
      (d) =>
        (resFilter.size === 0 || resFilter.has(d.res)) &&
        (featFilter.size === 0 || [...featFilter].every((k) => d.feats.has(k))),
    );
    const cmp: Record<SortKey, (a: typeof rows[number], b: typeof rows[number]) => number> = {
      best: (a, b) => a.i - b.i,
      size: (a, b) => b.rel.size_gb - a.rel.size_gb,
      smallest: (a, b) => a.rel.size_gb - b.rel.size_gb,
      bitrate: (a, b) => sortBitrate(b.rel) - sortBitrate(a.rel),
      seeders: (a, b) => b.rel.seeders - a.rel.seeders,
    };
    return [...rows].sort(cmp[sort]);
  }, [decorated, resFilter, featFilter, sort]);
  const toggle = (set: React.Dispatch<React.SetStateAction<Set<string>>>, key: string) =>
    set((cur) => { const n = new Set(cur); n.has(key) ? n.delete(key) : n.add(key); return n; });

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

  const block = onBlock
    ? async (rel: RankedRelease) => {
        setBusy(rel.title);
        try {
          await onBlock(rel);
          await run();
        } catch (e) {
          setError((e as Error).message);
        } finally {
          setBusy(null);
        }
      }
    : undefined;

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-10 w-full max-w-[760px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="m-0 truncate text-[15px] font-bold">{title}</h2>
            {subtitle && <p className="m-0 mt-0.5 text-[11.5px] text-ink-dim">{subtitle}</p>}
          </div>
          <div className="flex flex-none items-center gap-2">
            <button onClick={run} disabled={loading} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{loading ? "Searching…" : "Search again"}</button>
            <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
          </div>
        </div>
        {error && <div className="mb-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}
        {!loading && list && list.releases.length > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-2 border-b pb-3" style={{ borderColor: "var(--line)" }}>
            {resAvailable.length > 1 && (
              <FilterGroup label="Resolution">
                {resAvailable.map((r) => (
                  <Chip key={r} active={resFilter.has(r)} onClick={() => toggle(setResFilter, r)}>{r}</Chip>
                ))}
              </FilterGroup>
            )}
            {featAvailable.length > 0 && (
              <FilterGroup label="Must have">
                {featAvailable.map((f) => (
                  <Chip key={f.key} active={featFilter.has(f.key)} onClick={() => toggle(setFeatFilter, f.key)}>{f.label}</Chip>
                ))}
              </FilterGroup>
            )}
            <label className="ml-auto flex items-center gap-1.5 text-[10.5px] text-ink-faint">
              Sort
              <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)} className="rounded-lg px-2 py-1 text-[11px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                {SORTS.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
              </select>
            </label>
          </div>
        )}
        <div className="thin-scroll max-h-[58vh] overflow-y-auto">
          {loading ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">Searching your indexers…</div>
          ) : !list || list.releases.length === 0 ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">No releases found on your indexers.</div>
          ) : view.length === 0 ? (
            <div className="p-8 text-center text-[12.5px] text-ink-dim">
              No releases match these filters.
              <button onClick={() => { setResFilter(new Set()); setFeatFilter(new Set()); }} className="ml-1.5 font-semibold" style={{ color: "var(--accent)" }}>Clear filters</button>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              {view.map(({ rel }) => (
                <ReleaseRow
                  key={rel.title}
                  rel={rel}
                  why={rel.recommended ? list.why : undefined}
                  busy={busy === rel.title}
                  grabbed={grabbed.has(rel.title)}
                  disabled={busy !== null}
                  onGrab={() => grab(rel)}
                  onBlock={block ? () => block(rel) : undefined}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function FilterGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="font-mono text-[9px] font-bold uppercase tracking-[0.09em] text-ink-faint">{label}</span>
      <div className="flex flex-wrap gap-1">{children}</div>
    </div>
  );
}

function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className="rounded-full px-2.5 py-1 text-[10.5px] font-semibold transition-colors"
      style={{
        border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`,
        background: active ? "var(--accent-soft)" : "var(--panel-2)",
        color: active ? "var(--accent)" : "var(--ink-dim)",
      }}
    >
      {children}
    </button>
  );
}

function ReleaseRow({ rel, why, busy, grabbed, disabled, onGrab, onBlock }: { rel: RankedRelease; why?: string[]; busy: boolean; grabbed: boolean; disabled: boolean; onGrab: () => void; onBlock?: () => void }) {
  const blocked = rel.blocklisted;
  const highlight = rel.recommended && !blocked;
  return (
    <div className="rounded-xl p-3.5" style={{ border: highlight ? "1px solid var(--accent)" : "1px solid var(--line)", background: highlight ? "var(--accent-soft)" : "var(--panel)", opacity: blocked ? 0.55 : 1 }}>
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            {highlight && <span className="rounded-full px-2 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Recommended</span>}
            {blocked && <span className="rounded-full px-2 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>Blocklisted</span>}
            <span className="text-[13px] font-semibold">{rel.summary}</span>
          </div>
          <div className="mt-1 truncate font-mono text-[11px] text-ink-faint" title={rel.title}>{rel.title}</div>
          <div className="mt-1.5 flex flex-wrap items-center gap-3 font-mono text-[10.5px] text-ink-dim">
            <span>{rel.size_gb.toFixed(1)} GB</span>
            {rel.bitrate_mbps ? <span title="Average bitrate (size ÷ runtime)" style={{ color: "var(--accent)" }}>{rel.bitrate_mbps.toFixed(1)} Mb/s</span> : null}
            <span>{rel.seeders} seeders</span>
            <span>{rel.indexer}</span>
          </div>
          {!rel.eligible && rel.reject_reason && <div className="mt-1.5 text-[11px]" style={{ color: "var(--avoid)" }}>{rel.reject_reason}</div>}
          {why && why.length > 0 && (
            <ul className="mt-2 list-none space-y-0.5 p-0 text-[11.5px]" style={{ color: "var(--accent)" }}>
              {why.map((w, i) => <li key={i}>✓ {w}</li>)}
            </ul>
          )}
        </div>
        <div className="flex flex-none flex-col items-end gap-1.5">
          <button onClick={onGrab} disabled={disabled || grabbed || blocked} className="rounded-lg px-3.5 py-2 text-[12px] font-semibold" style={{ background: grabbed ? "var(--panel-2)" : "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: grabbed ? "var(--ink-dim)" : "var(--accent-ink)", opacity: blocked ? 0.5 : 1 }}>
            {grabbed ? "Grabbed ✓" : busy ? "…" : "Grab"}
          </button>
          {onBlock && !blocked && !grabbed && (
            <button onClick={onBlock} disabled={disabled} title="Blocklist and search for an alternate" className="rounded-lg px-2.5 py-1 text-[10.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-faint)" }}>⊘ Block</button>
          )}
        </div>
      </div>
    </div>
  );
}
