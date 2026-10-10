import { useEffect } from "react";
import { chapterAt, close, resume, skip, toggle, usePlayer, type PlayerState } from "../../lib/player";
import { fmtTime } from "../../lib/playerMath";
import { PlayerIcon } from "./PlayerIcons";

// MiniPlayer is the audiobook bar pinned above the phone tab bar (or the bottom of the
// window) on every page while a book is loaded — or, after a reload, offering the one
// left part-way. While it shows, <html> carries has-player, so --player-h joins the
// bottom chrome (index.css): pages pad past it and toasts and sheets sit above it.
export function MiniPlayer({ sidebar = false }: { sidebar?: boolean }) {
  const p = usePlayer();
  const visible = !!(p.key || p.resume);
  useEffect(() => {
    if (!visible) return;
    const root = document.documentElement;
    root.classList.add("has-player");
    return () => root.classList.remove("has-player");
  }, [visible]);
  if (!visible) return null;

  const title = p.meta?.title ?? p.resume?.meta.title ?? "Audiobook";
  const cover = p.meta?.cover ?? p.resume?.meta.cover;
  const pct = p.duration > 0 ? Math.min(100, (p.position / p.duration) * 100) : 0;
  return (
    <div
      role="region"
      aria-label="Audiobook player"
      data-testid="mini-player"
      className={`fixed inset-x-0 z-player font-sans ${sidebar ? "lg:left-[236px]" : ""}`}
      style={{ bottom: "calc(var(--tabbar-h) + env(safe-area-inset-bottom, 0px))", height: "var(--player-h)", background: "var(--sidebar)", borderTop: "1px solid var(--line)" }}
    >
      {/* Whole-book progress along the top edge. */}
      <div className="absolute inset-x-0 top-0 h-[2px]" style={{ background: "var(--line-soft)" }}>
        <div className="h-full" style={{ width: `${pct}%`, background: "var(--accent)" }} />
      </div>
      <div className="px-safe flex h-full items-center gap-3">
        <div className="h-11 w-11 flex-none overflow-hidden rounded-md" style={{ background: "var(--panel-2)" }}>
          {cover && <img src={cover} alt="" className="h-full w-full object-cover" />}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold">{p.key ? title : <>Resume <span>{title}</span></>}</div>
          <div className="truncate text-[11.5px]" style={{ color: subtitleTone(p) }}>{subtitle(p)}</div>
        </div>
        {p.key && (
          <button onClick={() => skip(-30)} aria-label="Back 30 seconds" className="hidden h-11 w-11 flex-none place-items-center rounded-full min-[360px]:grid" style={{ color: "var(--ink)" }}>
            <PlayerIcon name="back30" />
          </button>
        )}
        <button
          onClick={() => (p.key ? toggle() : resume())}
          aria-label={p.playing ? "Pause" : "Play"}
          className="grid h-11 w-11 flex-none place-items-center rounded-full"
          style={{ background: "var(--accent)", color: "var(--accent-ink)" }}
        >
          <PlayerIcon name={p.playing ? "pause" : "play"} size={20} />
        </button>
        <button onClick={close} aria-label="Close the player" className="grid h-11 w-9 flex-none place-items-center rounded-full text-ink-dim">
          <PlayerIcon name="close" size={18} />
        </button>
      </div>
    </div>
  );
}

// subtitle is the bar's second line: what needs saying most.
export function subtitle(p: PlayerState): string {
  if (p.error) return p.error;
  if (!p.key && p.resume) return `at ${fmtTime(p.resume.position)}`;
  if (p.loading && p.tracks.length === 0) return "Starting…";
  if (p.hold) return holdShort(p.hold.kind);
  if (p.note) return p.note;
  const i = chapterAt(p.chapters, p.position);
  const chapter = i >= 0 ? p.chapters[i]?.title : "";
  return [chapter || p.meta?.author, `${fmtTime(p.position)} / ${fmtTime(p.duration)}`].filter(Boolean).join(" · ");
}

function subtitleTone(p: PlayerState): string {
  if (p.error) return "var(--reject-text)";
  if (p.hold && p.hold.kind !== "again") return "var(--avoid-text)";
  return "var(--ink-dim)";
}

export function holdShort(kind: "back" | "forward" | "again"): string {
  if (kind === "again") return "Listening again — saved once you listen on";
  if (kind === "forward") return "Jumped ahead — kept once you play to the end";
  return "Jumped back — kept once you listen on";
}
