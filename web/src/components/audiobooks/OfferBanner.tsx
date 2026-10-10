import type { AudioOffer } from "../../lib/api";
import { fmtClock, ghost, primary } from "../../pages/audiobooks/shared";

// OfferBanner offers back a later spot an app sent that wasn't used — usually offline
// listening uploaded after another device had moved the place on. Shared by the You page
// and the Listen tab's book sheet.
export function OfferBanner({ offer, busy, onUse, onDismiss }: { offer: AudioOffer; busy?: boolean; onUse: () => void; onDismiss: () => void }) {
  // An offline upload carries the phone's name; a progress update an app sent outside
  // playback is just "app".
  const named = !!offer.device && offer.device !== "app";
  const who = named ? `Your ${offer.device}` : "An app";
  const how = named ? " from listening offline" : "";
  return (
    <div className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2 text-[12px]" style={{ background: "var(--accent-soft)", border: "1px solid var(--accent-line)" }}>
      <span className="min-w-0">{who} uploaded a later spot (<b>{fmtClock(offer.position)}</b>){how} — use it?</span>
      <span className="flex flex-none gap-2">
        <button onClick={onUse} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>Use it</button>
        <button onClick={onDismiss} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={ghost}>Dismiss</button>
      </span>
    </div>
  );
}
