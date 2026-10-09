// Placeholders shaped like the real content, shown while a list loads for the
// first time. They stand in for the grid or list so the page never shows its
// "nothing here yet" text before the server has answered.

const block = { background: "var(--panel-2)" };

// One poster card: the same 2:3 art and two caption lines as the library grids.
export function CardSkeleton({ full = true }: { full?: boolean }) {
  return (
    <div className={full ? "w-full" : "w-[150px] flex-none"}>
      <div className="rounded-xl" style={{ aspectRatio: "2/3", ...block }} />
      <div className="mt-2 h-3 w-4/5 rounded" style={block} />
      <div className="mt-1.5 h-2.5 w-2/5 rounded" style={block} />
    </div>
  );
}

export interface SkeletonProps {
  variant?: "grid" | "list" | "table";
  /** How many cards or rows. */
  count?: number;
  /** Grid column minimum, matching the page's own grid. */
  minWidth?: number;
}

export function Skeleton({ variant = "grid", count, minWidth = 150 }: SkeletonProps) {
  if (variant === "grid") {
    return (
      <div aria-busy="true" aria-label="Loading" className="grid animate-pulse gap-4" style={{ gridTemplateColumns: `repeat(auto-fill, minmax(${minWidth}px, 1fr))` }}>
        {Array.from({ length: count ?? 12 }).map((_, i) => <CardSkeleton key={i} />)}
      </div>
    );
  }
  if (variant === "list") {
    return (
      <div aria-busy="true" aria-label="Loading" className="flex animate-pulse flex-col gap-2.5">
        {Array.from({ length: count ?? 4 }).map((_, i) => (
          <div key={i} className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
            <div className="h-3.5 w-1/3 rounded" style={block} />
            <div className="mt-2 h-2.5 w-2/3 rounded" style={block} />
          </div>
        ))}
      </div>
    );
  }
  return (
    <div aria-busy="true" aria-label="Loading" className="animate-pulse overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
      {Array.from({ length: count ?? 8 }).map((_, i) => (
        <div key={i} className="flex items-center gap-4 px-4 py-3" style={{ borderTop: i ? "1px solid var(--line)" : undefined }}>
          <div className="h-3 w-2/5 rounded" style={block} />
          <div className="h-3 w-1/6 rounded" style={block} />
          <div className="ml-auto h-3 w-1/12 rounded" style={block} />
        </div>
      ))}
    </div>
  );
}
