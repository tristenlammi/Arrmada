import type { AudioCard } from "../../lib/api";

// BookCard is one audiobook on the Listen tab: a square cover, title, author and, once
// started, how far through it the person is. Tapping it opens the book sheet.

export function bookProgress(c: AudioCard): number {
  const p = c.progress;
  if (!p) return 0;
  if (p.finished) return 100;
  const dur = p.duration || c.duration;
  return dur > 0 ? Math.min(100, (p.position / dur) * 100) : 0;
}

// Cover is a book's square cover, or its title on a warm tile when it has none (a tile
// too small to read just stays blank: the title is beside it).
export function Cover({ src, title, className = "", small = false }: { src?: string; title: string; className?: string; small?: boolean }) {
  return (
    <div className={`relative aspect-square overflow-hidden rounded-lg ${className}`} style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
      {src ? (
        <img src={src} alt="" className="h-full w-full object-cover" loading="lazy" decoding="async" />
      ) : (
        <div className="flex h-full w-full items-center justify-center p-2 text-center" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>
          {!small && <span aria-hidden className="line-clamp-4 text-[11.5px] font-bold text-white">{title}</span>}
        </div>
      )}
    </div>
  );
}

export function BookCard({ c, onOpen, className = "" }: { c: AudioCard; onOpen: (key: string) => void; className?: string }) {
  const pct = bookProgress(c);
  return (
    <button onClick={() => onOpen(c.key)} className={`flex min-w-0 flex-col text-left ${className}`} aria-label={`${c.title}${c.author ? ` by ${c.author}` : ""}`}>
      <Cover src={c.cover} title={c.title} className="w-full" />
      {c.progress && (
        <div className="mt-1.5 h-[3px] overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
          <div className="h-full rounded-full" style={{ width: `${Math.max(pct, 2)}%`, background: c.progress.finished ? "var(--good)" : "var(--accent)" }} />
        </div>
      )}
      <div className="mt-1.5 truncate text-[12.5px] font-semibold" title={c.title}>{c.title}</div>
      {c.author && <div className="truncate text-[11px] text-ink-dim">{c.author}</div>}
    </button>
  );
}
