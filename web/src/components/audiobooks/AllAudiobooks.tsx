import { useEffect, useRef, useState } from "react";
import { api, type AudioCard, type AudioLibrarySort } from "../../lib/api";
import { BookCard } from "./BookCard";
import { inputStyle, ghost } from "../../pages/audiobooks/shared";

// AllAudiobooks is the whole audiobook library as a grid, searchable and sortable, a page
// of 60 at a time from the server so a big library stays quick on a phone.

const PAGE = 60;

export function AllAudiobooks({ onOpen }: { onOpen: (key: string) => void }) {
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<AudioLibrarySort>("title");
  const [items, setItems] = useState<AudioCard[] | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Only the newest search's answer is shown: typing fast mustn't flash older results.
  const seq = useRef(0);

  // Search as you type, once typing pauses.
  useEffect(() => {
    const t = setTimeout(() => setQuery(q.trim()), 300);
    return () => clearTimeout(t);
  }, [q]);

  useEffect(() => {
    const n = ++seq.current;
    setBusy(true);
    api.audioLibrary({ q: query || undefined, sort, page: 0, limit: PAGE })
      .then((r) => { if (n !== seq.current) return; setItems(r.items); setTotal(r.total); setPage(0); setError(null); })
      .catch((e) => { if (n === seq.current) setError((e as Error).message); })
      .finally(() => { if (n === seq.current) setBusy(false); });
  }, [query, sort]);

  const more = () => {
    const n = ++seq.current;
    setBusy(true);
    api.audioLibrary({ q: query || undefined, sort, page: page + 1, limit: PAGE })
      .then((r) => { if (n !== seq.current) return; setItems((cur) => [...(cur ?? []), ...r.items]); setTotal(r.total); setPage(page + 1); })
      .catch((e) => { if (n === seq.current) setError((e as Error).message); })
      .finally(() => { if (n === seq.current) setBusy(false); });
  };

  return (
    <section aria-label="All audiobooks">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h2 className="m-0 text-[15px] font-bold">All audiobooks{items && total > 0 ? <span className="font-normal text-ink-faint"> · {total}</span> : null}</h2>
        <div className="flex min-w-0 flex-1 items-center justify-end gap-2">
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search title, author, series"
            aria-label="Search audiobooks"
            className="min-w-0 max-w-[240px] flex-1 rounded-lg px-3 py-1.5 text-[12.5px]"
            style={inputStyle}
          />
          <select value={sort} onChange={(e) => setSort(e.target.value as AudioLibrarySort)} aria-label="Sort audiobooks" className="flex-none rounded-lg px-2 py-1.5 text-[12px]" style={inputStyle}>
            <option value="title">Title</option>
            <option value="author">Author</option>
            <option value="added">Recently added</option>
          </select>
        </div>
      </div>
      {error && <div className="mb-2 text-[12.5px]" style={{ color: "var(--reject-text)" }}>{error}</div>}
      {items === null && !error && <div className="text-[12.5px] text-ink-dim">Loading…</div>}
      {items && items.length === 0 && (
        <div className="rounded-xl p-6 text-center text-[12.5px] text-ink-dim" style={{ border: "1px dashed var(--line)" }}>
          {query ? `Nothing matches “${query}”.` : "No audiobooks in the library yet. Ask for one from Discover."}
        </div>
      )}
      {items && items.length > 0 && (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(140px, 42vw), 1fr))" }}>
          {items.map((c) => <BookCard key={c.key} c={c} onOpen={onOpen} />)}
        </div>
      )}
      {items && items.length < total && (
        <div className="mt-4 flex justify-center">
          <button onClick={more} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={ghost}>
            {busy ? "Loading…" : `Show more (${total - items.length})`}
          </button>
        </div>
      )}
    </section>
  );
}
