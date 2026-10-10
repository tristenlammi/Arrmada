import type { AudioShelf } from "../../lib/api";
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
  if (shelf.items.length === 0) return null;
  const label = shelfLabel(shelf);
  return (
    <section aria-label={label}>
      <h2 className="m-0 mb-2 text-[15px] font-bold">{label}</h2>
      <div className="thin-scroll -mx-4 flex gap-3 overflow-x-auto px-4 pb-2 sm:mx-0 sm:px-0" style={{ scrollSnapType: "x proximity" }}>
        {shelf.items.map((c) => (
          <BookCard key={c.key} c={c} onOpen={onOpen} className="w-[128px] flex-none sm:w-[140px]" />
        ))}
      </div>
    </section>
  );
}
