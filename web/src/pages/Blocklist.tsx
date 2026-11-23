import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type BlocklistRow, type BlockType } from "../lib/api";
import { invalidate, useQuery } from "../lib/query";
import { UsenetChip } from "../components/UsenetChip";
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

// BlocklistPanel is Activity → Blocklist: every release Arrmada won't grab again, for every
// kind of title, including the global entries that block a release for everything. Each
// can be unblocked, or many at once: tick rows, the whole page, or everything the current
// filter matches across every page.
export function BlocklistPanel() {
  const [type, setType] = useState<BlockType | "">("");
  const [input, setInput] = useState("");
  const [q, setQ] = useState("");
  const [page, setPage] = useState(0);
  const [busy, setBusy] = useState<number | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  // Everything the filter matches, on every page, up to the newest entry shown when it was
  // picked (anything blocked after that stays blocked). null when only ticked rows count.
  const [allThrough, setAllThrough] = useState<number | null>(null);
  const [bulkBusy, setBulkBusy] = useState(false);
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
  // A tick is for a row on this page; "all matching" is for this filter.
  useEffect(() => { setSelected(new Set()); }, [key]);
  useEffect(() => { setAllThrough(null); }, [type, q]);

  const all = allThrough !== null;
  const picked = (rows ?? []).filter((r) => selected.has(r.id));
  const pageAll = !!rows && rows.length > 0 && (all || picked.length === rows.length);
  const count = all ? total : picked.length;
  const plural = (n: number) => `${n} release${n === 1 ? "" : "s"}`;

  const toggle = (id: number) => {
    if (all) {
      // Unticking one row while everything is picked keeps the rest of this page picked.
      setAllThrough(null);
      setSelected(new Set((rows ?? []).filter((r) => r.id !== id).map((r) => r.id)));
      return;
    }
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };
  const clear = () => { setAllThrough(null); setSelected(new Set()); };

  const bulkUnblock = async () => {
    if (count === 0) return;
    const globals = all ? (type === "global" ? total : 0) : picked.filter((r) => r.type === "global").length;
    const ok = await confirm({
      title: `Unblock ${all ? "all " : ""}${plural(count)}?`,
      body: (
        <p className="m-0">
          {all
            ? `Every entry ${q || type ? "matching this filter" : "on the blocklist"}, on every page, is unblocked. `
            : ""}
          The next search for their titles may grab them again.
          {globals > 0 && <> <b>{globals === 1 ? "One is" : `${globals} are`} blocked for every title, so any search may grab {globals === 1 ? "it" : "them"} again.</b></>}
          {all && type === "" && <> <b>Global entries among them become grabbable by any search.</b></>}
        </p>
      ),
      confirmLabel: all ? `Unblock all ${count}` : `Unblock ${count}`,
      tone: "danger",
    });
    if (!ok) return;
    setBulkBusy(true);
    try {
      const res = all
        ? await api.bulkUnblock({ all: true, type, q, through_id: allThrough })
        : await api.bulkUnblock({ ids: picked.map((r) => r.id) });
      toast(`Unblocked ${plural(res.removed)}.`);
      // Emptied this page: step back to one that still has entries.
      if (all) setPage(0);
      else if (rows && picked.length === rows.length && page > 0) setPage((p) => p - 1);
      clear();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBulkBusy(false);
      invalidate("blocklist:");
    }
  };

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
      <div className="max-w-[1100px]">
        <p className="mb-5 max-w-[72ch] text-base text-ink-dim">
          Releases Arrmada won't grab again: ones you blocked or rejected, downloads that stalled, and fakes it caught.
          Most are blocked for one title. Global entries block a release for every title — usually junk rejected from
          Needs you that wasn't tied to anything. Unblock an entry to make that release grabbable again on the next search.
        </p>

        <div className="mb-4 flex flex-wrap items-center gap-2">
          {TYPES.map((t) => {
            const on = type === t.key;
            return (
              <button key={t.key || "all"} onClick={() => { setType(t.key); setPage(0); }} aria-pressed={on}
                className="rounded-full px-3 py-1 text-sm font-semibold"
                style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent-text)" : "var(--ink-faint)" }}>
                {t.label}
              </button>
            );
          })}
          <input value={input} onChange={(e) => setInput(e.target.value)} placeholder="Search releases or titles…" aria-label="Search the blocklist"
            className="ml-auto w-full rounded-lg px-3 py-1.5 text-base sm:w-[260px]"
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
              <>
                <div className="mb-3 flex flex-wrap items-center gap-2 text-sm text-ink-dim" role="region" aria-label="Select entries">
                  <label className="flex items-center gap-2">
                    <input type="checkbox" className="h-4 w-4 accent-[var(--accent)]" checked={pageAll} disabled={bulkBusy} aria-label="Select every entry on this page"
                      onChange={(e) => { setAllThrough(null); setSelected(e.target.checked ? new Set(rows.map((r) => r.id)) : new Set()); }} />
                    <span aria-live="polite">{count > 0 ? `${count} selected` : "Select all on this page"}</span>
                  </label>
                  {pageAll && total > rows.length && (all ? (
                    <span>Every entry {q || type ? "matching this filter" : "on the blocklist"} is selected.</span>
                  ) : (
                    <button type="button" onClick={() => setAllThrough(data?.newest_id ?? 0)} disabled={bulkBusy || !data?.newest_id}
                      className="font-semibold" style={{ color: "var(--accent-text)" }}>
                      Select all {total} {q || type ? "matching this filter" : "entries"}
                    </button>
                  ))}
                  {count > 0 && (
                    <span className="ml-auto flex gap-2">
                      <Button size="sm" variant="ghost" onClick={clear} disabled={bulkBusy}>Clear</Button>
                      <Button size="sm" variant="danger" busy={bulkBusy} busyLabel="Unblocking…" disabled={busy !== null} onClick={bulkUnblock}>
                        {all ? `Unblock all ${count}` : `Unblock selected (${count})`}
                      </Button>
                    </span>
                  )}
                </div>
                <div className="flex flex-col gap-2">
                  {rows.map((r) => {
                    const link = itemLink(r);
                    const on = all || selected.has(r.id);
                    return (
                      <div key={r.id} className="rounded-xl p-3.5" data-testid="blocklist-row"
                        style={{ background: "var(--panel)", border: `1px solid ${on ? "var(--accent)" : r.type === "global" ? "var(--avoid)" : "var(--line)"}` }}>
                        <div className="flex items-start gap-3">
                          <input type="checkbox" className="mt-1 h-4 w-4 shrink-0 accent-[var(--accent)]" checked={on} disabled={bulkBusy}
                            onChange={() => toggle(r.id)} aria-label={`Select ${r.title}`} />
                          <div className="min-w-0 flex-1">
                            <div className="flex flex-wrap items-center gap-2">
                              <StatusChip tone={r.type === "global" ? "avoid" : "faint"}>{r.type === "music" && r.discography ? "discography" : KIND_LABEL[r.type]}</StatusChip>
                              {r.network === "usenet" && <UsenetChip />}
                              <span className="min-w-0 break-all font-mono text-sm">{r.title}</span>
                            </div>
                            <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-sm text-ink-dim">
                              {r.type === "global" ? (
                                <span style={{ color: "var(--avoid-text)" }}>Blocks this release for every title</span>
                              ) : link && r.item_title ? (
                                <span>For <Link to={link} className="font-semibold" style={{ color: "var(--accent-text)" }}>{r.item_title}</Link></span>
                              ) : (
                                <span className="text-ink-faint">For a {KIND_LABEL[r.type]} no longer in the library</span>
                              )}
                              {r.reason && <span>{r.reason}</span>}
                              {r.indexer && <span className="font-mono text-xs text-ink-faint">{r.indexer}</span>}
                              <span className="font-mono text-xs text-ink-faint">{when(r.created_at)}</span>
                            </div>
                          </div>
                          <Button size="sm" variant="ghost" busy={busy === r.id} busyLabel="Unblocking…" disabled={busy !== null || bulkBusy} onClick={() => unblock(r)}>Unblock</Button>
                        </div>
                      </div>
                    );
                  })}
                </div>
              </>
            )}
            {total > PAGE_SIZE && (
              <div className="mt-4 flex items-center justify-between text-sm text-ink-dim">
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
