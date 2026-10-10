import { useState } from "react";
import type { AudioHistoryEntry } from "../../lib/api";
import { fmtClock } from "../../pages/audiobooks/shared";

// PlaceTimeline is one audiobook place's history: every spot the place moved through,
// the spot just before each change, places an app sent that weren't used, held jumps and
// removals — each with a reason in words and "Go back here". Shared by the You page and
// the Listen tab's book sheet.

const SHOW = 20;

// timelineReason says what a row is, in words. Rows from before kinds were recorded read
// as "applied", with the old reason words.
export function timelineReason(h: AudioHistoryEntry): string {
  const on = h.device ? ` on ${h.device}` : "";
  switch (h.kind) {
    case "before":
      return "place before a change";
    case "rejected":
      return h.reason === "unproven" ? "not used: jump not proven" : "not used: older than your place";
    case "held":
      return h.reason === "held-forward" ? "held: jumped ahead" : "held: jumped back";
    case "discarded":
      return `discarded in ${h.device || "an app"}`;
    case "restored":
      return h.reason === "undiscard" ? "put back after it was removed" : "restored";
  }
  switch (h.reason) {
    case "rewind":
      return "jumped back";
    case "forward-proven":
      return "jumped ahead";
    case "back":
      return `skipped back${on}`;
    case "offline":
      return `offline upload${on}`;
    case "set":
      return "set by an app";
    case "import":
      return "imported from Audiobookshelf";
    case "restore":
      return "restored";
    case "manual":
      return h.device && h.device !== "Arrmada" ? `changed in ${h.device}` : "changed by you";
  }
  return `played${on}`;
}

export function PlaceTimeline({ history, busy, onRestore }: { history: AudioHistoryEntry[] | null; busy?: boolean; onRestore: (h: AudioHistoryEntry) => void }) {
  const [all, setAll] = useState(false);
  if (history === null) return <span className="text-[11.5px] text-ink-faint">Loading…</span>;
  if (history.length === 0) return <span className="text-[11.5px] text-ink-faint">No earlier places yet.</span>;
  const rows = all ? history : history.slice(0, SHOW);
  return (
    <div className="flex flex-col gap-1">
      {rows.map((h) => {
        const dim = h.kind === "rejected" && h.dismissed;
        return (
          <div key={h.id} className="flex items-center justify-between gap-2 py-0.5 text-[11.5px]" style={dim ? { opacity: 0.55 } : undefined}>
            <span className="min-w-0 flex-1 text-ink-dim">
              <span className="block"><b className="font-semibold" style={{ color: "var(--ink)" }}>{fmtClock(h.position)}</b> · {timelineReason(h)}</span>
              <span className="block text-[11px] text-ink-faint">{new Date(h.at).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</span>
            </span>
            <button onClick={() => onRestore(h)} disabled={busy} className="flex-none rounded px-2 py-1 font-semibold disabled:opacity-60" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Go back here</button>
          </div>
        );
      })}
      {history.length > SHOW && (
        <button onClick={() => setAll(!all)} className="self-start text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>
          {all ? "Show fewer" : `Show all (${history.length})`}
        </button>
      )}
    </div>
  );
}
