import { useEffect, useMemo, useState } from "react";
import { api, type MyBook, type MyRequest } from "../lib/api";
import { posterThumb } from "../lib/img";

// MyBooks is the requester's view of the book library: every book that has a file,
// a download for each ebook, and their own requests still on the way. Deliberately
// small — no editing, no indexer search, no library management — because its readers
// aren't running the library, they're looking for something to read. Audiobooks are
// pointed at Audiobookshelf rather than downloaded from here.

function fmtSize(b: number): string {
  if (!b) return "";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  let v = b;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${u[i]}`;
}

type Sort = "newest" | "title" | "author";

export function MyBooks() {
  const [books, setBooks] = useState<MyBook[] | null>(null);
  const [requests, setRequests] = useState<MyRequest[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [sort, setSort] = useState<Sort>("newest");
  const [onlyEbooks, setOnlyEbooks] = useState(false);

  useEffect(() => {
    let alive = true;
    const load = () => api.myBooks()
      .then((r) => { if (alive) { setBooks(r.books); setRequests(r.requests); setError(null); } })
      .catch((e) => { if (alive) setError((e as Error).message); });
    load();
    const t = setInterval(load, 15000); // a book that arrives while the page is open shows up
    return () => { alive = false; clearInterval(t); };
  }, []);

  const shown = useMemo(() => {
    if (!books) return [];
    const needle = q.trim().toLowerCase();
    const list = books.filter((b) => {
      if (onlyEbooks && !b.ebook) return false;
      if (!needle) return true;
      return [b.title, b.author, b.series].some((s) => s && s.toLowerCase().includes(needle));
    });
    const by = (a: string | undefined, b: string | undefined) => (a || "").localeCompare(b || "", undefined, { sensitivity: "base" });
    if (sort === "title") list.sort((a, b) => by(a.title, b.title));
    else if (sort === "author") list.sort((a, b) => by(a.author, b.author) || by(a.title, b.title));
    // "newest" is the server's order (added_at desc)
    return list;
  }, [books, q, sort, onlyEbooks]);

  const ebooks = books ? books.filter((b) => b.ebook).length : 0;

  return (
    <div className="mx-auto w-full max-w-[1200px] px-4 py-5 sm:px-6">
      <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="m-0 text-[20px] font-bold">Books</h1>
          <p className="m-0 mt-1 text-[12.5px] text-ink-dim">
            Download any ebook in the library. Audiobooks are in Audiobookshelf.
            {books && <span className="text-ink-faint"> · {books.length} book{books.length === 1 ? "" : "s"}, {ebooks} ebook{ebooks === 1 ? "" : "s"}</span>}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search title, author, series"
            className="w-[220px] max-w-full rounded-lg px-3 py-1.5 text-[12.5px]"
            style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
          <select value={sort} onChange={(e) => setSort(e.target.value as Sort)} className="rounded-lg px-2 py-1.5 text-[12px]" style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}>
            <option value="newest">Newest first</option>
            <option value="title">Title A–Z</option>
            <option value="author">Author A–Z</option>
          </select>
          <label className="flex items-center gap-1.5 text-[12px] text-ink-dim">
            <input type="checkbox" checked={onlyEbooks} onChange={(e) => setOnlyEbooks(e.target.checked)} />
            Ebooks only
          </label>
        </div>
      </div>

      {error && <div className="mb-3 text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>}

      {requests.length > 0 && (
        <div className="mb-5">
          <h2 className="m-0 mb-2 text-[14px] font-bold">Your requests</h2>
          <div className="thin-scroll flex gap-3 overflow-x-auto pb-2">
            {requests.map((r, i) => <RequestCard key={`${r.title}-${i}`} r={r} />)}
          </div>
        </div>
      )}

      {books === null && !error && <div className="text-[13px] text-ink-dim">Loading…</div>}
      {books && books.length === 0 && (
        <div className="rounded-xl p-6 text-center text-[13px] text-ink-dim" style={{ border: "1px dashed var(--line)" }}>
          The library is empty so far. Request a book from Discover and it will show up here once it arrives.
        </div>
      )}
      {books && books.length > 0 && shown.length === 0 && (
        <div className="rounded-xl p-6 text-center text-[13px] text-ink-dim" style={{ border: "1px dashed var(--line)" }}>
          Nothing matches “{q}”. Can't find it? Request it from Discover.
        </div>
      )}

      {shown.length > 0 && (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {shown.map((b) => <BookCard key={b.book_id} b={b} />)}
        </div>
      )}
    </div>
  );
}

function CoverBox({ url, title, children }: { url?: string; title: string; children?: React.ReactNode }) {
  return (
    <div className="relative" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
      {url ? (
        <img src={posterThumb(url)} alt={title} className="h-full w-full object-cover" loading="lazy" decoding="async" />
      ) : (
        <div className="flex h-full w-full items-center justify-center p-3 text-center" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>
          <span className="text-[12px] font-bold text-white">{title}</span>
        </div>
      )}
      {children}
    </div>
  );
}

function Badge({ label, tone }: { label: string; tone: string }) {
  return <span className="absolute right-1.5 top-1.5 rounded-full px-2 py-0.5 text-[9px] font-bold uppercase tracking-wide" style={{ background: "rgba(20,12,7,.72)", color: tone, border: `1px solid ${tone}` }}>{label}</span>;
}

function BookCard({ b }: { b: MyBook }) {
  return (
    <div className="flex flex-col overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <CoverBox url={b.cover_url} title={b.title}>
        {b.mine && <Badge label="Yours" tone="var(--accent)" />}
      </CoverBox>
      <div className="flex flex-1 flex-col gap-0.5 p-2.5">
        <div className="truncate text-[12.5px] font-semibold" title={b.title}>{b.title}</div>
        {b.author && <div className="truncate text-[11px] text-ink-dim" title={b.author}>{b.author}</div>}
        {b.series && <div className="truncate text-[10.5px] text-ink-faint" title={b.series}>{b.series}</div>}
        <div className="mt-auto pt-2">
          {b.ebook ? (
            <a
              href={api.ebookDownloadURL(b.book_id)}
              download
              className="block rounded-lg px-2 py-1.5 text-center text-[11.5px] font-semibold"
              style={{ background: "var(--accent)", color: "var(--accent-ink)" }}
              title={`Download the ${b.ebook.format.toUpperCase()}${b.ebook.size_bytes ? ` (${fmtSize(b.ebook.size_bytes)})` : ""}`}
            >
              Download {b.ebook.format ? b.ebook.format.toUpperCase() : "ebook"}
              {b.ebook.size_bytes ? <span className="font-normal opacity-80"> · {fmtSize(b.ebook.size_bytes)}</span> : null}
            </a>
          ) : (
            <div className="rounded-lg px-2 py-1.5 text-center text-[11px] text-ink-faint" style={{ border: "1px solid var(--line)" }} title="This one is audiobook only for now">
              Audiobookshelf only
            </div>
          )}
          {b.ebook && b.audiobook && <div className="mt-1 text-center text-[10px] text-ink-faint">Also in Audiobookshelf</div>}
        </div>
      </div>
    </div>
  );
}

function RequestCard({ r }: { r: MyRequest }) {
  const st = r.status === "declined" ? { label: "Declined", tone: "var(--reject)" }
    : r.status === "pending" ? { label: "Pending", tone: "var(--avoid)" }
    : { label: "Searching", tone: "var(--accent)" };
  return (
    <div className="w-[120px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <CoverBox url={r.cover_url} title={r.title}>
        <Badge label={st.label} tone={st.tone} />
      </CoverBox>
      <div className="p-2">
        <div className="truncate text-[11.5px] font-semibold" title={r.title}>{r.title}</div>
        {r.author && <div className="truncate text-[10.5px] text-ink-dim" title={r.author}>{r.author}</div>}
      </div>
    </div>
  );
}
