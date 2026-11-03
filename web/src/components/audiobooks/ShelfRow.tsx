import { useState } from "react";
import { api, type AudioCard, type AudioShelf } from "../../lib/api";
import { useToast } from "../../ui";
import { BookCard } from "./BookCard";

// ShelfRow is one of the Listen tab's rows (Continue listening, Continue series,
// Recently added, Finished): a horizontal strip of square covers, like Discover's rows.

// The server names shelves the way the listening apps do; the page says them in
// sentence case, and "Listen Again" is simply the books you've finished.
const LABEL: Record<string, string> = {
  "continue-listening": "Continue listening",
  "continue-series": "Continue series",
  "recently-added": "Recently added",
  "listen-again": "Finished",
};

export function shelfLabel(s: AudioShelf): string {
  return LABEL[s.id] ?? s.label;
}

export function ShelfRow({ shelf, onOpen }: { shelf: AudioShelf; onOpen: (key: string) => void }) {
  const toast = useToast();
  // Continue listening's books can be taken off it (the place is kept; listening on puts
  // the book back), with Undo for the last one.
  const removable = shelf.id === "continue-listening";
  const [gone, setGone] = useState<Set<string>>(() => new Set());
  const [undo, setUndo] = useState<AudioCard | null>(null);
  const items = shelf.items.filter((c) => !gone.has(c.key));
  if (items.length === 0 && !undo) return null;
  const label = shelfLabel(shelf);

  const setHidden = (c: AudioCard, hidden: boolean) => {
    setGone((g) => { const n = new Set(g); if (hidden) n.add(c.key); else n.delete(c.key); return n; });
    setUndo(hidden ? c : null);
    api.hideAudio(c.key, hidden).catch((e) => {
      setGone((g) => { const n = new Set(g); if (hidden) n.delete(c.key); else n.add(c.key); return n; });
      setUndo(null);
      toast((e as Error).message, { tone: "error" });
    });
  };

  return (
    <section aria-label={label}>
      <h2 className="m-0 mb-2 text-lg font-bold">{label}</h2>
      {undo && (
        <div className="mb-2 flex flex-wrap items-center gap-2 text-sm text-ink-dim" role="status">
          <span className="min-w-0">Removed “{undo.title}”. Your place is kept.</span>
          <button type="button" onClick={() => setHidden(undo, false)} className="font-semibold" style={{ color: "var(--accent-text)" }}>Undo</button>
        </div>
      )}
      <div className="thin-scroll -mx-4 flex gap-3 overflow-x-auto px-4 pb-2 sm:mx-0 sm:px-0" style={{ scrollSnapType: "x proximity" }}>
        {items.map((c) =>
          removable ? (
            <div key={c.key} className="relative w-[128px] flex-none sm:w-[140px]">
              <BookCard c={c} onOpen={onOpen} className="w-full" />
              <button
                type="button"
                onClick={() => setHidden(c, true)}
                aria-label={`Remove ${c.title} from ${label}`}
                title={`Remove from ${label}`}
                className="absolute right-1.5 top-1.5 flex h-7 w-7 items-center justify-center rounded-full text-base leading-none text-white bg-black/60 hover:bg-black/85"
              >
                ×
              </button>
            </div>
          ) : (
            <BookCard key={c.key} c={c} onOpen={onOpen} className="w-[128px] flex-none sm:w-[140px]" />
          ),
        )}
      </div>
    </section>
  );
}
