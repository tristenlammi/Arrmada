import { BOOK_FORMATS, FORMAT_BADGE, FORMAT_CHOICE, isBookFormats, type BookFormats } from "../lib/bookFormats";

// FormatChoice is a book request's Read / Listen / Both control: a segmented radio group,
// stacked into a short list on a phone where the three don't fit side by side.
export function FormatChoice({ value, onChange, disabled }: { value: BookFormats | null; onChange: (f: BookFormats) => void; disabled?: boolean }) {
  return (
    <div role="radiogroup" aria-label="Format" className="flex w-full max-w-[240px] flex-col overflow-hidden rounded-lg text-left sm:inline-flex sm:w-fit sm:max-w-full sm:flex-row" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      {BOOK_FORMATS.map((f) => {
        const on = value === f;
        return (
          <button
            key={f}
            type="button"
            role="radio"
            aria-checked={on}
            disabled={disabled}
            onClick={() => onChange(f)}
            className="px-3 py-1.5 text-left text-[12px] font-semibold"
            style={{ background: on ? "var(--accent-soft)" : "transparent", color: on ? "var(--accent)" : "var(--ink-dim)" }}
          >
            {FORMAT_CHOICE[f]}
          </button>
        );
      })}
    </div>
  );
}

// BookFormatBadge is the small "Read" / "Listen" / "Both" tag on a book request, so staff
// see what was asked for. Nothing for a request made before the choice existed.
export function BookFormatBadge({ formats, surface }: { formats?: string; surface?: "poster" }) {
  if (!isBookFormats(formats)) return null;
  const style = surface === "poster"
    ? { background: "rgba(20,12,7,.78)", color: "#fff", border: "1px solid rgba(255,255,255,.22)" }
    : { background: "var(--panel-2)", color: "var(--ink-dim)", border: "1px solid var(--line)" };
  return (
    <span className="flex-none rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={style} title={`Asked for: ${FORMAT_CHOICE[formats].toLowerCase()}`}>
      {FORMAT_BADGE[formats]}
    </span>
  );
}
