import { useEffect, useState } from "react";
import { api, type MyBook } from "../lib/api";
import { posterThumb } from "../lib/img";

// MyBooks is the requester's bookshelf: every book they asked for, and a download for
// each ebook that has arrived. Deliberately small — no editing, no search, no library
// management — because its readers aren't running the library, they're waiting for a
// book. Audiobooks are pointed at Audiobookshelf rather than downloaded from here.

function fmtSize(b: number): string {
  if (!b) return "";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  let v = b;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${u[i]}`;
}

function statusOf(b: MyBook): { label: string; tone: string } {
  if (b.ebook) return { label: "Ready", tone: "var(--good)" };
  if (b.status === "declined") return { label: "Declined", tone: "var(--reject)" };
  if (b.status === "pending") return { label: "Pending approval", tone: "var(--avoid)" };
  if (b.audiobook) return { label: "Audiobook only", tone: "var(--accent)" };
  return { label: "Searching", tone: "var(--accent)" };
}

export function MyBooks() {
  const [books, setBooks] = useState<MyBook[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () => api.myBooks().then((r) => { if (alive) setBooks(r.books); }).catch((e) => { if (alive) setError((e as Error).message); });
    load();
    const t = setInterval(load, 15000); // a book that arrives while the page is open shows up
    return () => { alive = false; clearInterval(t); };
  }, []);

  return (
    <div className="mx-auto w-full max-w-[1100px] px-4 py-5 sm:px-6">
      <div className="mb-4">
        <h1 className="m-0 text-[20px] font-bold">Your books</h1>
        <p className="m-0 mt-1 text-[12.5px] text-ink-dim">
          Books you've requested. Once an ebook arrives you can download it here; audiobooks are in Audiobookshelf.
        </p>
      </div>

      {error && <div className="mb-3 text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>}
      {books === null && !error && <div className="text-[13px] text-ink-dim">Loading…</div>}
      {books && books.length === 0 && (
        <div className="rounded-xl p-6 text-center text-[13px] text-ink-dim" style={{ border: "1px dashed var(--line)" }}>
          Nothing yet. Request a book from Discover and it will show up here.
        </div>
      )}

      {books && books.length > 0 && (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {books.map((b, i) => <BookCard key={b.book_id || `r${i}`} b={b} />)}
        </div>
      )}
    </div>
  );
}

function BookCard({ b }: { b: MyBook }) {
  const st = statusOf(b);
  return (
    <div className="flex flex-col overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="relative" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
        {b.cover_url ? (
          <img src={posterThumb(b.cover_url)} alt={b.title} className="h-full w-full object-cover" loading="lazy" decoding="async" />
        ) : (
          <div className="flex h-full w-full items-center justify-center p-3 text-center" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>
            <span className="text-[12px] font-bold text-white">{b.title}</span>
          </div>
        )}
        <span className="absolute right-1.5 top-1.5 rounded-full px-2 py-0.5 text-[9px] font-bold uppercase tracking-wide" style={{ background: "rgba(20,12,7,.72)", color: st.tone, border: `1px solid ${st.tone}` }}>{st.label}</span>
      </div>
      <div className="flex flex-1 flex-col gap-1 p-2.5">
        <div className="truncate text-[12.5px] font-semibold" title={b.title}>{b.title}</div>
        {b.author && <div className="truncate text-[11px] text-ink-dim" title={b.author}>{b.author}</div>}
        <div className="mt-auto pt-1.5">
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
            <div className="rounded-lg px-2 py-1.5 text-center text-[11px] text-ink-faint" style={{ border: "1px solid var(--line)" }}>
              {b.audiobook ? "Listen in Audiobookshelf" : b.status === "declined" ? "Not coming" : "Not here yet"}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
