import { useEffect, useState } from "react";
import { api, type AudioHistoryEntry, type AudioItemDetail, type AudioPlace } from "../../lib/api";
import { useMe } from "../../lib/me";
import { playAudiobook } from "../../lib/playerStub";
import { pause as pausePlayer, seek as seekPlayer, usePlayer } from "../../lib/player";
import { chapterAt, fmtTime } from "../../lib/playerMath";
import { Sheet } from "../../ui";
import { fmtAgo, ghost, primary } from "../../pages/audiobooks/shared";
import { Cover } from "./BookCard";
import { PlaceTimeline } from "./PlaceTimeline";
import { OfferBanner } from "./OfferBanner";

// BookSheet is one audiobook from the Listen tab: what it is, Play or Resume, its
// chapters (tap one to play from there), bookmarks, other versions, and "Your place" with
// everything the place guards kept — earlier places to go back to, a later spot an app
// sent, a held jump. Deep-linked as /audiobooks?book=<key>, so Back closes it.

function fmtLength(sec: number): string {
  if (!(sec > 0)) return "";
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return h > 0 ? `${h} h ${m} min` : `${m} min`;
}

export function BookSheet({ itemKey, place, onClose, onOpenBook, onChanged }: {
  itemKey: string;
  /** The person's place in this book from GET /me/audio (offer, held jump), if they have one. */
  place?: AudioPlace;
  onClose: () => void;
  onOpenBook: (key: string) => void;
  /** The place changed (restored, offer used): the page reloads it. */
  onChanged: () => void;
}) {
  const { booksEnabled } = useMe();
  const player = usePlayer();
  const [d, setD] = useState<AudioItemDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [moreText, setMoreText] = useState(false);
  const load = () => api.audioItem(itemKey).then((x) => { setD(x); setError(null); }).catch((e) => setError((e as Error).message));
  useEffect(() => { setD(null); void load(); }, [itemKey]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!d) {
    return (
      <Sheet onClose={onClose} closeOnBack={false} ariaLabel="Audiobook" size="lg">
        <div className="p-6 text-[12.5px]" style={{ color: error ? "var(--reject-text)" : "var(--ink-dim)" }}>{error ?? "Loading…"}</div>
      </Sheet>
    );
  }

  const meta = { title: d.title, author: d.author, cover: d.cover };
  const here = player.key === d.key;
  const playingHere = here && player.playing;
  const prog = d.progress;
  const position = here ? player.position : prog?.position ?? 0;
  const current = here ? chapterAt(d.chapters, player.position) : -1;
  const play = (at?: number) => playAudiobook(d.key, { at, meta });
  const mainLabel = playingHere ? "Pause" : here ? `Resume at ${fmtTime(player.position)}`
    : !prog ? "Play" : prog.finished ? "Listen again" : `Resume at ${fmtTime(prog.position)}`;
  const series = d.series ? `${d.series}${d.series_seq ? ` #${d.series_seq}` : ""}` : "";

  return (
    <Sheet onClose={onClose} closeOnBack={false} ariaLabel={d.title} size="lg">
      <div className="flex flex-col gap-5 p-5 sm:p-6">
        <div className="flex gap-4">
          <Cover src={d.cover} title={d.title} className="w-[112px] flex-none sm:w-[140px]" />
          <div className="min-w-0 flex-1">
            <h2 className="m-0 text-[17px] font-bold leading-snug">{d.title}</h2>
            {d.author && <div className="mt-0.5 text-[12.5px] text-ink-dim">{d.author}</div>}
            {series && <div className="mt-0.5 text-[12px] text-ink-faint">{series}</div>}
            <div className="mt-1 text-[11.5px] text-ink-faint">{[fmtLength(d.duration), d.year ? String(d.year) : "", d.chapters.length ? `${d.chapters.length} chapters` : ""].filter(Boolean).join(" · ")}</div>
            <div className="mt-3 flex flex-wrap gap-2">
              <button
                onClick={() => (playingHere ? pausePlayer() : play())}
                className="rounded-lg px-4 py-2 text-[13px] font-semibold"
                style={primary}
              >
                {playingHere ? "❚❚ Pause" : `▶ ${mainLabel}`}
              </button>
              {booksEnabled && (
                <a href={api.audiobookDownloadURL(d.book_id, d.version_id ?? 0)} download className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={ghost}>Download</a>
              )}
            </div>
          </div>
        </div>

        {d.description && (
          <div className="text-[12.5px] leading-relaxed text-ink-dim">
            <p className={`m-0 whitespace-pre-line ${moreText ? "" : "line-clamp-4"}`}>{d.description}</p>
            {d.description.length > 240 && (
              <button onClick={() => setMoreText(!moreText)} className="mt-1 text-[12px] font-semibold" style={{ color: "var(--accent)" }}>{moreText ? "Less" : "More"}</button>
            )}
          </div>
        )}

        <YourPlace d={d} place={place} position={position} here={here} onChanged={() => { onChanged(); void load(); }} />

        {d.chapters.length > 0 && (
          <section aria-label="Chapters">
            <h3 className="m-0 mb-1.5 text-[13.5px] font-bold">Chapters</h3>
            <ol className="m-0 flex list-none flex-col p-0">
              {d.chapters.map((c, i) => (
                <li key={c.id ?? i}>
                  <button
                    onClick={() => play(c.start)}
                    aria-current={i === current ? "true" : undefined}
                    className="flex min-h-[40px] w-full items-center justify-between gap-3 rounded-lg px-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]"
                    style={i === current ? { background: "var(--accent-soft)", color: "var(--accent-text)" } : undefined}
                  >
                    <span className="min-w-0 truncate">{c.title || `Chapter ${i + 1}`}</span>
                    <span className="flex-none text-[11.5px] text-ink-faint">{fmtTime(c.start)}</span>
                  </button>
                </li>
              ))}
            </ol>
          </section>
        )}

        {d.bookmarks.length > 0 && (
          <section aria-label="Bookmarks">
            <h3 className="m-0 mb-1.5 text-[13.5px] font-bold">Bookmarks</h3>
            {d.bookmarks.map((b) => (
              <button key={b.time} onClick={() => play(b.time)} className="flex min-h-[40px] w-full items-center justify-between gap-3 rounded-lg px-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]">
                <span className="min-w-0 truncate">{b.title || "Bookmark"}</span>
                <span className="flex-none text-[11.5px] text-ink-faint">{fmtTime(b.time)}</span>
              </button>
            ))}
          </section>
        )}

        {d.versions.length > 0 && (
          <section aria-label="Other versions">
            <h3 className="m-0 mb-1.5 text-[13.5px] font-bold">Other versions</h3>
            {d.versions.map((v) => (
              <button key={v.key} onClick={() => onOpenBook(v.key)} className="flex min-h-[40px] w-full items-center rounded-lg px-2 text-left text-[12.5px] font-semibold hover:bg-[var(--panel-2)]" style={{ color: "var(--accent)" }}>
                {v.title}
              </button>
            ))}
          </section>
        )}
      </div>
    </Sheet>
  );
}

// YourPlace is the person's place in the book and everything kept about it: finished or
// how far, where it was saved from, a held jump, a later spot an app sent that wasn't
// used, and earlier places to go back to.
function YourPlace({ d, place, position, here, onChanged }: { d: AudioItemDetail; place?: AudioPlace; position: number; here: boolean; onChanged: () => void }) {
  const [hist, setHist] = useState<AudioHistoryEntry[] | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const prog = d.progress;
  const toggle = () => {
    if (!open && hist === null) api.audioHistory(d.key).then((r) => setHist(r.history)).catch(() => setHist([]));
    setOpen(!open);
  };
  const act = async (f: () => Promise<unknown>) => {
    setBusy(true); setError(null);
    try { await f(); setHist(null); setOpen(false); onChanged(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  // Restoring a place while this book is in the player moves the player there too, or its
  // next report would carry the place straight back to where it was playing.
  const restore = (historyId: number) => act(async () => {
    const r = await api.restoreAudioPlace(d.key, historyId);
    if (here) seekPlayer(r.position, false);
  });
  // While this book is in the player, the player's own note says what's held.
  const pending = here ? null : prog?.pending_position ?? null;
  const dur = prog?.duration || d.duration;
  const pct = prog?.finished ? 100 : dur > 0 ? Math.min(100, (position / dur) * 100) : 0;
  return (
    <section aria-label="Your place" className="rounded-xl p-3.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
      <div className="flex items-center justify-between gap-2">
        <h3 className="m-0 text-[13.5px] font-bold">Your place</h3>
        {prog && <button onClick={toggle} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>{open ? "Hide" : "Earlier places"}</button>}
      </div>
      {!prog ? (
        <div className="mt-1 text-[12px] text-ink-faint">Not started yet. Your place is kept in Arrmada, so it follows you to a listening app too.</div>
      ) : (
        <>
          <div className="mt-2 h-[4px] overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: prog.finished ? "var(--good)" : "var(--accent)" }} />
          </div>
          <div className="mt-1 text-[11.5px] text-ink-faint">
            {prog.finished && !here ? "Finished" : `${fmtTime(position)}${dur > 0 ? ` of ${fmtTime(dur)} · ${Math.round(pct)}%` : ""}`} · saved {fmtAgo(prog.updated_at)}{prog.device ? ` on ${prog.device}` : ""}
          </div>
        </>
      )}
      {place?.offer && (
        <OfferBanner offer={place.offer} busy={busy}
          onUse={() => restore(place.offer!.history_id)}
          onDismiss={() => act(() => api.dismissAudioOffer(d.key, place.offer!.history_id))} />
      )}
      {pending !== null && prog && (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2 text-[12px]"
          style={prog.finished ? { background: "var(--panel)", border: "1px solid var(--line-soft)" } : { background: "var(--avoid-soft)", border: "1px solid var(--avoid)" }}>
          <span>
            {prog.finished
              ? <>Listening again from {pending === 0 ? "the start" : <b>{fmtTime(pending)}</b>} — saved once you've listened for a moment.</>
              : pending > prog.position
                ? <>An app jumped ahead to <b>{fmtTime(pending)}</b>, near the end. It's kept once you listen on from there — or use it now.</>
                : <>An app jumped back to <b>{fmtTime(pending)}</b>. It's kept once you listen on from there for a bit — or use it now if that was you.</>}
          </span>
          <button onClick={() => act(() => api.acceptAudioJump(d.key))} disabled={busy} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>
            {prog.finished && pending === 0 ? "Start over now" : "Use this spot"}
          </button>
        </div>
      )}
      {open && (
        <div className="mt-2">
          <PlaceTimeline history={hist} busy={busy} onRestore={(h) => restore(h.id)} />
        </div>
      )}
      {error && <div className="mt-1 text-[12px]" style={{ color: "var(--reject-text)" }}>{error}</div>}
    </section>
  );
}
