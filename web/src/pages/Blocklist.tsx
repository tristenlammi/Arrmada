import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type BlocklistRow, type BlockType } from "../lib/api";
import { invalidate, useQuery } from "../lib/query";
import { Button, EmptyState, ErrorState, Skeleton, StaleBanner, StatusChip, useConfirm, useToast } from "../ui";

const PAGE_SIZE = 100;

const TYPES: { key: BlockType | ""; label: string }[] = [
  { key: "", label: "All" },
  { key: "movie", label: "Movies" },
  { key: "series", label: "Series" },
  { key: "book", label: "Books" },
  { key: "music", label: "Music" },
  { key: "global", label: "Global" },
];

const KIND_LABEL: Record<BlockType, string> = { movie: "movie", series: "series", book: "book", music: "album", global: "global" };

// Where a blocklist row's title lives in the app.
function itemLink(r: BlocklistRow): string | null {
  if (r.item_id <= 0) return null;
  switch (r.type) {
    case "movie": return `/movies/${r.item_id}`;
    case "series": return `/series/${r.item_id}`;
    case "book": return `/books/${r.item_id}`;
    case "music": return r.discography ? `/music/${r.item_id}` : `/music/album/${r.item_id}`;
    default: return null;
  }
}

function when(s: string): string {
  const d = new Date(s.includes("T") ? s : s.replace(" ", "T") + "Z");
  if (isNaN(d.getTime())) return s;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// Blocklist — every release Arrmada won't grab again, for every kind of title, including
// the global entries that block a release for everything. Each can be unblocked.
export function Blocklist() {
  const [type, setType] = useState<BlockType | "">("");
  const [input, setInput] = useState("");
  const [q, setQ] = useState("");
  const [page, setPage] = useState(0);
  const [busy, setBusy] = useState<number | null>(null);
  const confirm = useConfirm();
  const toast = useToast();

  // Debounced so typing doesn't fire a request per keystroke.
  useEffect(() => {
    const t = window.setTimeout(() => { setQ(input.trim()); setPage(0); }, 250);
    return () => window.clearTimeout(t);
  }, [input]);

  const key = `blocklist:${type}:${q}:${page}`;
  const { data, error, loading, refetch } = useQuery(key, () => api.blocklistAll({ type, q, limit: PAGE_SIZE, offset: page * PAGE_SIZE }));
  const rows = data?.items;
  const total = data?.total ?? 0;

  const unblock = async (r: BlocklistRow) => {
    const global = r.type === "global";
    const ok = await confirm({
      title: "Unblock this release?",
      body: (
        <>
          <span className="break-all font-mono">{r.title}</span>
          <p className="mb-0 mt-2">
            {global
              ? "It's blocked for every title right now. Once unblocked, any search may grab it again."
              : `The next search for ${r.item_title || `this ${KIND_LABEL[r.type]}`} may grab it again.`}
          </p>
        </>
      ),
      confirmLabel: "Unblock",
      tone: "danger",
    });
    if (!ok) return;
    setBusy(r.id);
    try {
      await api.unblockAny(r.id);
      toast("Unblocked.");
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(null);
      invalidate("blocklist:");
    }
  };

  return (
    <>
      <PageHeader title="Blocklist" />
      <div className="mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">
        <p className="mb-5 max-w-[72ch] text-[12.5px] text-ink-dim">
          Releases Arrmada won't grab again: ones you blocked or rejected, downloads that stalled, and fakes it caught.
          Most are blocked for one title. Global entries block a release for every title — usually junk rejected from
          Review that wasn't tied to anything. Unblock an entry to make that release grabbable again on the next search.
        </p>

        <div className="mb-4 flex flex-wrap items-center gap-2">
          {TYPES.map((t) => {
            const on = type === t.key;
            return (
              <button key={t.key || "all"} onClick={() => { setType(t.key); setPage(0); }} aria-pressed={on}
                className="rounded-full px-3 py-1 text-[12px] font-semibold"
                style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
                {t.label}
              </button>
            );
          })}
          <input value={input} onChange={(e) => setInput(e.target.value)} placeholder="Search releases or titles…" aria-label="Search the blocklist"
            className="ml-auto w-full rounded-lg px-3 py-1.5 text-[12.5px] sm:w-[260px]"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        </div>

        {rows === undefined ? (
          error ? <ErrorState what="the blocklist" message={error.message} onRetry={refetch} busy={loading} /> : <Skeleton variant="list" />
        ) : (
          <>
            {error && <StaleBanner message={error.message} onRetry={refetch} />}
            {rows.length === 0 ? (
              <EmptyState title={q || type ? "Nothing matches" : "Nothing is blocklisted"}
                body={q || type ? "No blocklist entries match this filter." : "Releases you block, reject or that stall show up here."} />
            ) : (
              <div className="flex flex-col gap-2">
                {rows.map((r) => {
                  const link = itemLink(r);
                  return (
                    <div key={r.id} className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: `1px solid ${r.type === "global" ? "var(--avoid)" : "var(--line)"}` }}>
                      <div className="flex items-start gap-3">
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2">
                            <StatusChip tone={r.type === "global" ? "avoid" : "faint"}>{r.type === "music" && r.discography ? "discography" : KIND_LABEL[r.type]}</StatusChip>
                            <span className="min-w-0 break-all font-mono text-[12px]">{r.title}</span>
                          </div>
                          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11.5px] text-ink-dim">
                            {r.type === "global" ? (
                              <span style={{ color: "var(--avoid)" }}>Blocks this release for every title</span>
                            ) : link && r.item_title ? (
                              <span>For <Link to={link} className="font-semibold" style={{ color: "var(--accent)" }}>{r.item_title}</Link></span>
                            ) : (
                              <span className="text-ink-faint">For a {KIND_LABEL[r.type]} no longer in the library</span>
                            )}
                            {r.reason && <span>{r.reason}</span>}
                            {r.indexer && <span className="font-mono text-[10.5px] text-ink-faint">{r.indexer}</span>}
                            <span className="font-mono text-[10.5px] text-ink-faint">{when(r.created_at)}</span>
                          </div>
                        </div>
                        <Button size="sm" variant="ghost" busy={busy === r.id} busyLabel="Unblocking…" disabled={busy !== null} onClick={() => unblock(r)}>Unblock</Button>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
            {total > PAGE_SIZE && (
              <div className="mt-4 flex items-center justify-between text-[12px] text-ink-dim">
                <span>{page * PAGE_SIZE + 1}–{Math.min((page + 1) * PAGE_SIZE, total)} of {total}</span>
                <span className="flex gap-2">
                  <Button size="sm" disabled={page === 0} onClick={() => setPage((p) => p - 1)}>Previous</Button>
                  <Button size="sm" disabled={(page + 1) * PAGE_SIZE >= total} onClick={() => setPage((p) => p + 1)}>Next</Button>
                </span>
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}
