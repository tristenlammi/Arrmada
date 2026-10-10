import { useEffect, useState } from "react";
import { api, type AudioBookmark } from "../../lib/api";
import {
  chapterAt, keepHeld, nextChapter, playerKeys, prevChapter, seek, setRate, setSleep, skip, sleepLeft, toggle, usePlayer,
} from "../../lib/player";
import { MAX_RATE, MIN_RATE, chapterEndAt, fmtLeft, fmtTime, type HoldKind } from "../../lib/playerMath";
import { Sheet, useConfirm } from "../../ui";
import { ghost, inputStyle, primary } from "../../pages/audiobooks/shared";
import { Cover } from "./BookCard";
import { PlayerIcon } from "./PlayerIcons";

// FullPlayer is the whole player, opened by tapping the mini-player: the chapter and a
// scrubber through it, the transport, speed, a sleep timer, bookmarks, the chapter list,
// and — when the place guards hold a jump — a plain note saying so with "Keep it now".
// Full height on a phone; Back (or a swipe back) closes it.

const SLEEP_MINUTES = [15, 30, 45, 60];

export function FullPlayer({ onClose }: { onClose: () => void }) {
  const p = usePlayer();
  // Desktop keys while it's open: Space, ←/→ 30 s, Shift+←/→ chapters.
  useEffect(() => {
    window.addEventListener("keydown", playerKeys);
    return () => window.removeEventListener("keydown", playerKeys);
  }, []);
  if (!p.key) return null;

  const i = chapterAt(p.chapters, p.position);
  const ch = i >= 0 ? p.chapters[i] : null;
  const start = ch ? ch.start : 0;
  const end = ch ? chapterEndAt(p.chapters, p.position, p.duration) : p.duration;
  const bookPct = p.duration > 0 ? Math.min(100, (p.position / p.duration) * 100) : 0;

  return (
    <Sheet
      onClose={onClose}
      ariaLabel="Audiobook player"
      panelClassName="thin-scroll w-full max-w-[560px] overflow-y-auto rounded-t-2xl pb-[env(safe-area-inset-bottom)] h-[calc(100dvh-0.75rem)] sm:h-auto sm:max-h-[calc(100dvh-3rem)] sm:rounded-2xl sm:pb-0"
    >
      <div className="flex flex-col gap-5 px-5 pb-6 pt-3 sm:px-6 sm:pt-5">
        <div className="flex items-start justify-between gap-2">
          <button onClick={onClose} aria-label="Close the full player" className="grid h-10 w-10 place-items-center rounded-full text-ink-dim" style={{ background: "var(--panel-2)" }}>
            <PlayerIcon name="chevron" size={20} />
          </button>
          {p.loading && <span className="mt-2 text-[11.5px] text-ink-faint">Loading…</span>}
        </div>

        <div className="flex flex-col items-center text-center">
          <Cover src={p.meta?.cover} title={p.meta?.title ?? ""} className="w-[min(62vw,240px)]" />
          <h2 className="m-0 mt-4 text-[17px] font-bold leading-snug">{p.meta?.title}</h2>
          {p.meta?.author && <div className="mt-0.5 text-[12.5px] text-ink-dim">{p.meta.author}</div>}
          {ch && <div className="mt-1.5 text-[13px] font-semibold" style={{ color: "var(--accent-text)" }}>{ch.title || `Chapter ${i + 1}`}</div>}
        </div>

        {p.hold && <HeldNote kind={p.hold.kind} position={p.hold.position} />}
        {p.error && <div className="rounded-lg px-3 py-2 text-[12.5px]" style={{ background: "var(--reject-soft)", color: "var(--reject-text)" }}>{p.error}</div>}
        {p.note && !p.error && <div className="text-center text-[12px] text-ink-dim">{p.note}</div>}

        <Scrubber start={start} end={end} position={p.position} />
        <div aria-hidden className="-mt-3 h-[2px] overflow-hidden rounded-full" style={{ background: "var(--line-soft)" }} title="Whole book">
          <div className="h-full" style={{ width: `${bookPct}%`, background: "var(--accent-line)" }} />
        </div>
        <div className="-mt-3 text-center text-[11px] text-ink-faint">{fmtTime(p.position)} of {fmtTime(p.duration)} · {fmtLeft((p.duration - p.position) / (p.rate || 1))}</div>

        <div className="flex items-center justify-center gap-2 sm:gap-4">
          <Round label="Previous chapter" onClick={prevChapter}><PlayerIcon name="prev" /></Round>
          <Round label="Back 30 seconds" onClick={() => skip(-30)}><PlayerIcon name="back30" size={28} /></Round>
          <button onClick={toggle} aria-label={p.playing ? "Pause" : "Play"} className="grid h-16 w-16 place-items-center rounded-full" style={primary}>
            <PlayerIcon name={p.playing ? "pause" : "play"} size={30} />
          </button>
          <Round label="Forward 30 seconds" onClick={() => skip(30)}><PlayerIcon name="fwd30" size={28} /></Round>
          <Round label="Next chapter" onClick={nextChapter}><PlayerIcon name="next" /></Round>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <SpeedControl rate={p.rate} />
          <SleepControl />
        </div>

        <Bookmarks itemKey={p.key} position={p.position} />

        {p.chapters.length > 0 && (
          <section aria-label="Chapters">
            <h3 className="m-0 mb-1.5 text-[13.5px] font-bold">Chapters</h3>
            <ol className="m-0 flex list-none flex-col p-0">
              {p.chapters.map((c, n) => (
                <li key={c.id ?? n}>
                  <button
                    onClick={() => seek(c.start)}
                    aria-current={n === i ? "true" : undefined}
                    className="flex min-h-[40px] w-full items-center justify-between gap-3 rounded-lg px-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]"
                    style={n === i ? { background: "var(--accent-soft)", color: "var(--accent-text)" } : undefined}
                  >
                    <span className="min-w-0 truncate">{c.title || `Chapter ${n + 1}`}</span>
                    <span className="flex-none text-[11.5px] text-ink-faint">{fmtTime(c.start)}</span>
                  </button>
                </li>
              ))}
            </ol>
          </section>
        )}
      </div>
    </Sheet>
  );
}

function Round({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} aria-label={label} className="grid h-12 w-12 place-items-center rounded-full" style={{ color: "var(--ink)" }}>
      {children}
    </button>
  );
}

// HeldNote says plainly that the spot playing isn't the saved place yet, and why.
export function HeldNote({ kind, position }: { kind: HoldKind; position: number }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const keep = async () => {
    setBusy(true); setError(null);
    try { await keepHeld(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const again = kind === "again";
  const text = again
    ? "Listening again from the start — saved once you've listened for a moment."
    : kind === "forward"
      ? <>Jumped ahead to <b>{fmtTime(position)}</b>, near the end — kept once you play on to the end.</>
      : <>Jumped back to <b>{fmtTime(position)}</b> — kept once you listen on for a bit.</>;
  return (
    <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2 text-[12px]"
      style={again ? { background: "var(--panel-2)", border: "1px solid var(--line-soft)" } : { background: "var(--avoid-soft)", border: "1px solid var(--avoid)" }}>
      <span className="min-w-0">{text}</span>
      <button onClick={keep} disabled={busy} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>
        {again ? "Start over now" : "Keep it now"}
      </button>
      {error && <span className="w-full text-[11.5px]" style={{ color: "var(--reject-text)" }}>{error}</span>}
    </div>
  );
}

// Scrubber moves through the chapter playing; the place moves when the finger lifts.
function Scrubber({ start, end, position }: { start: number; end: number; position: number }) {
  const [drag, setDrag] = useState<number | null>(null);
  const span = Math.max(1, end - start);
  const at = drag ?? Math.min(Math.max(position, start), end);
  const commit = () => {
    if (drag !== null) seek(drag);
    setDrag(null);
  };
  return (
    <div>
      <input
        type="range"
        aria-label="Position in this chapter"
        aria-valuetext={`${fmtTime(at - start)} of ${fmtTime(span)}`}
        min={start}
        max={start + span}
        step={1}
        value={at}
        onChange={(e) => setDrag(Number(e.target.value))}
        onPointerUp={commit}
        onKeyUp={commit}
        onBlur={commit}
        className="w-full"
        style={{ accentColor: "var(--accent)" }}
      />
      <div className="flex justify-between text-[11px] text-ink-faint">
        <span>{fmtTime(at - start)}</span>
        <span>-{fmtTime(start + span - at)}</span>
      </div>
    </div>
  );
}

function SpeedControl({ rate }: { rate: number }) {
  const step = (d: number) => setRate(Math.round((rate + d) * 10) / 10);
  return (
    <div className="flex items-center justify-between gap-1 rounded-xl px-2 py-1.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
      <button onClick={() => step(-0.1)} disabled={rate <= MIN_RATE} aria-label="Slower" className="grid h-9 w-9 place-items-center rounded-lg text-[16px] font-bold disabled:opacity-40">−</button>
      <span className="text-center">
        <span className="block text-[14px] font-bold" aria-live="polite">{rate.toFixed(1)}×</span>
        <span className="block text-[10.5px] text-ink-faint">Speed</span>
      </span>
      <button onClick={() => step(0.1)} disabled={rate >= MAX_RATE} aria-label="Faster" className="grid h-9 w-9 place-items-center rounded-lg text-[16px] font-bold disabled:opacity-40">+</button>
    </div>
  );
}

function SleepControl() {
  const p = usePlayer();
  const left = sleepLeft(p);
  // ?sleepdebug=1 adds a one-minute timer, for trying the fade without waiting.
  const debug = typeof window !== "undefined" && new URLSearchParams(window.location.search).has("sleepdebug");
  const value = !p.sleep ? "" : p.sleep.kind === "chapter" ? "chapter" : String(p.sleep.minutes);
  return (
    <label className="flex items-center justify-between gap-2 rounded-xl px-3 py-1.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
      <span className="min-w-0">
        <span className="flex items-center gap-1 text-[12.5px] font-semibold"><PlayerIcon name="sleep" size={14} /> Sleep</span>
        <span className="block truncate text-[10.5px] text-ink-faint" data-testid="sleep-left">{left === null ? "Off" : p.sleep?.kind === "chapter" ? `End of chapter · ${fmtLeft(left)}` : fmtLeft(left)}</span>
      </span>
      <select
        aria-label="Sleep timer"
        value={value}
        onChange={(e) => { const v = e.target.value; setSleep(v === "" ? null : v === "chapter" ? "chapter" : Number(v)); }}
        className="max-w-[7.5rem] rounded-lg px-1.5 py-1 text-[12px]"
        style={inputStyle}
      >
        <option value="">Off</option>
        {debug && <option value="1">1 minute</option>}
        {SLEEP_MINUTES.map((m) => <option key={m} value={String(m)}>{m} minutes</option>)}
        <option value="chapter">End of chapter</option>
      </select>
    </label>
  );
}

// Bookmarks are the person's own, in the same store the listening apps use, so one added
// here shows up in Lissen too.
function Bookmarks({ itemKey, position }: { itemKey: string; position: number }) {
  const confirm = useConfirm();
  const [list, setList] = useState<AudioBookmark[] | null>(null);
  const [adding, setAdding] = useState<number | null>(null);
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    setList(null);
    api.audioBookmarks(itemKey).then((r) => setList(r.bookmarks)).catch((e) => { setList([]); setError((e as Error).message); });
  }, [itemKey]);
  const save = async () => {
    if (adding === null) return;
    setError(null);
    try {
      const b = await api.addAudioBookmark(itemKey, adding, note.trim());
      setList((cur) => [...(cur ?? []).filter((x) => x.time !== b.time), b].sort((a, z) => a.time - z.time));
      setAdding(null); setNote("");
    } catch (e) { setError((e as Error).message); }
  };
  const remove = async (b: AudioBookmark) => {
    const ok = await confirm({ title: "Delete this bookmark?", body: `${b.title || "Bookmark"} at ${fmtTime(b.time)}. It goes from your listening apps too.`, confirmLabel: "Delete", tone: "danger" });
    if (!ok) return;
    setError(null);
    try {
      await api.deleteAudioBookmark(itemKey, b.time);
      setList((cur) => (cur ?? []).filter((x) => x.time !== b.time));
    } catch (e) { setError((e as Error).message); }
  };
  return (
    <section aria-label="Bookmarks">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <h3 className="m-0 text-[13.5px] font-bold">Bookmarks</h3>
        {adding === null && (
          <button onClick={() => setAdding(Math.floor(position))} className="flex items-center gap-1 rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>
            <PlayerIcon name="bookmark" size={14} /> Add at {fmtTime(position)}
          </button>
        )}
      </div>
      {adding !== null && (
        <form onSubmit={(e) => { e.preventDefault(); void save(); }} className="mb-2 flex gap-2">
          <input value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} placeholder={`Note for ${fmtTime(adding)} (optional)`} aria-label="Bookmark note" className="min-w-0 flex-1 rounded-lg px-3 py-1.5 text-[12.5px]" style={inputStyle} />
          <button type="submit" className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={primary}>Save</button>
          <button type="button" onClick={() => { setAdding(null); setNote(""); }} className="flex-none rounded-lg px-2.5 py-1.5 text-[12px] font-semibold" style={ghost}>Cancel</button>
        </form>
      )}
      {list === null ? <div className="text-[12px] text-ink-faint">Loading…</div> : list.length === 0 ? (
        <div className="text-[12px] text-ink-faint">None yet. Add one to come back to a moment.</div>
      ) : list.map((b) => (
        <div key={b.time} className="flex items-center gap-2">
          <button onClick={() => seek(b.time)} className="flex min-h-[40px] min-w-0 flex-1 items-center justify-between gap-3 rounded-lg px-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]">
            <span className="min-w-0 truncate">{b.title || "Bookmark"}</span>
            <span className="flex-none text-[11.5px] text-ink-faint">{fmtTime(b.time)}</span>
          </button>
          <button onClick={() => void remove(b)} aria-label={`Delete the bookmark at ${fmtTime(b.time)}`} className="grid h-9 w-9 flex-none place-items-center rounded-lg text-ink-faint">
            <PlayerIcon name="close" size={14} />
          </button>
        </div>
      ))}
      {error && <div className="mt-1 text-[12px]" style={{ color: "var(--reject-text)" }}>{error}</div>}
    </section>
  );
}
