import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link, useNavigate } from "react-router-dom";
import { api, type UserNotification } from "../lib/api";
import { posterThumb } from "../lib/img";
import { refToPath } from "../lib/refLink";
import { useLiveRefresh } from "../lib/useLiveRefresh";
import { REQUEST_TOPICS } from "../lib/useRequestsFeed";
import { SERVER_FILL, SERVER_INK, isMediaServer, safeWatchURL, watchLabel, watchLinksOf, type WatchLink } from "./brand/servers";

// BellWatchLinks is a notice's Watch buttons, small enough for the bell: the first (the
// reader's own server) in the accent, any other in its server's colours. Plain links,
// http(s) only; marks are left out to keep the bell light.
function BellWatchLinks({ links, onOpen, indent }: { links: readonly WatchLink[]; onOpen: () => void; indent: boolean }) {
  const usable = links.filter((l) => safeWatchURL(l.url));
  if (usable.length === 0) return null;
  return (
    <div className={`-mt-0.5 flex flex-wrap gap-2 pb-3.5 pr-3.5 ${indent ? "pl-[4.75rem]" : "pl-8"}`}>
      {usable.map((l, i) => {
        const known = isMediaServer(l.server) ? l.server : null;
        return (
          <a
            key={`${l.server}:${l.url}`}
            href={safeWatchURL(l.url) ?? undefined}
            target="_blank"
            rel="noopener noreferrer"
            onClick={onOpen}
            className={`inline-flex min-h-[30px] items-center rounded-md px-2.5 text-sm font-semibold ${i === 0 ? "bg-accent-grad text-accent-ink" : ""}`}
            style={i === 0 ? undefined : known ? { background: SERVER_FILL[known], color: SERVER_INK[known] } : { background: "var(--panel-2)", color: "var(--ink)" }}
          >
            ▶ Watch on {watchLabel(l)}
          </a>
        );
      })}
    </div>
  );
}

// Where the bell's Settings button goes: the Me page's "Get notified" card, the one place
// push and Apprise are set up.
const SETTINGS = "/me#notifications";
const BELL_TOPICS = [...REQUEST_TOPICS, "user.*.inbox.updated"];

// Pull the media title out of a notification: bodies read like “Dune” is ready to
// watch — the quoted part is the title. Falls back to the whole body if nothing is
// quoted (the title field is generic, e.g. "Your request is ready").
function searchTitleOf(n: UserNotification): string {
  const m = n.body?.match(/[“"]([^”"]+)[”"]/) || n.title?.match(/[“"]([^”"]+)[”"]/);
  return m ? m[1] : "";
}

// posterOf is a notice's poster as a small thumbnail: only the public TMDB image the server
// stored on a ready movie or show. Older notices carry none and show text alone.
export function posterOf(n: Pick<UserNotification, "poster_url">): string | undefined {
  const u = n.poster_url;
  return u && u.startsWith("https://image.tmdb.org/") ? posterThumb(u) : undefined;
}

// NotificationBell is everyone's inbox: a bell with an unread badge that opens a dropdown
// of notifications (a request ready, approved or declined; staff also hear of new
// requests), and a Settings button to the Me page, where push is set up. Every layout mounts exactly one, in its top bar
// or, on a wide staff screen, the sidebar. Kept fresh by the request feed (below).
//
// placement "side": the bell sits in the sidebar, which scrolls and so would clip a
// dropdown; the panel opens beside the sidebar instead, outside it.
export function NotificationBell({ placement = "down" }: { placement?: "down" | "side" }) {
  const [items, setItems] = useState<UserNotification[]>([]);
  const [unread, setUnread] = useState(0);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [anchor, setAnchor] = useState<{ left: number; top: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const side = placement === "side";

  const load = () => api.myNotifications().then((r) => { setItems(r.notifications ?? []); setUnread(r.unread); }).catch(() => {});
  // Most notices are about a request (approved, declined, ready; a new one, for staff), and
  // each is written before the request's change is announced: the request feed's events
  // bring the badge along within a second; a notice about nothing on the request feed (a
  // Send to Kindle result) says so on user.<id>.inbox.updated. Once a minute catches the rest.
  useLiveRefresh(load, { topics: BELL_TOPICS, fallbackMs: 60_000, debounceMs: 1000, immediate: true });

  // Close on outside click (the side panel lives outside the bell's own box).
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || panelRef.current?.contains(t)) return;
      setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const toggle = () => {
    const next = !open;
    if (next && side && ref.current) {
      const r = ref.current.getBoundingClientRect();
      setAnchor({ left: r.right + 12, top: r.top });
    }
    setOpen(next);
    if (next) { setError(null); load(); }
  };
  // Only zero the badge when the server actually marked everything read.
  const markAll = async () => {
    setError(null);
    try {
      await api.markAllNotificationsRead();
      setItems((xs) => xs.map((x) => ({ ...x, read: true })));
      setUnread(0);
    } catch (e) {
      console.warn("mark all notifications read failed", e);
      setError((e as Error).message);
    }
  };
  // Mark read (badge only drops when the server call succeeds), then jump to what it's
  // about, by its reference: the exact title (Dune 2021, not 1984), the book on Discover's
  // Books tab, or a staff "New request". Only an old notice whose reference says nothing
  // falls back to searching Discover for the quoted title.
  const markRead = async (n: UserNotification) => {
    if (n.read) return;
    try {
      await api.markNotificationRead(n.id);
      setItems((xs) => xs.map((x) => (x.id === n.id ? { ...x, read: true } : x)));
      setUnread((u) => Math.max(0, u - 1));
    } catch (e) {
      console.warn("mark notification read failed", e);
    }
  };
  const clickItem = async (n: UserNotification) => {
    await markRead(n);
    const path = refToPath(n.ref);
    if (path) {
      setOpen(false);
      navigate(path);
      return;
    }
    const title = searchTitleOf(n);
    if (title) {
      setOpen(false);
      navigate(n.media_type === "book" ? `/discover?tab=books&q=${encodeURIComponent(title)}` : `/discover?q=${encodeURIComponent(title)}`);
    }
  };

  // Phone: pinned to the viewport edges (fixed, auto top) so a 340px panel never runs
  // off the left edge; sm+ anchors it under the bell. Beside the sidebar it is fixed at
  // the bell's height, just right of the sidebar.
  const panel = (
    <div
      ref={panelRef}
      className={side ? "fixed z-50 w-[340px] overflow-hidden rounded-xl" : "fixed inset-x-3 z-50 mt-2 overflow-hidden rounded-xl sm:absolute sm:inset-x-auto sm:right-0 sm:w-[340px]"}
      style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", ...(side && anchor ? anchor : {}) }}
    >
      <div className="flex items-center justify-between px-3.5 py-2.5" style={{ borderBottom: "1px solid var(--line)" }}>
        <span className="text-md font-bold">Notifications</span>
        <div className="flex items-center gap-3 text-xs">
          {items.some((i) => !i.read) && <button onClick={markAll} style={{ color: "var(--accent-text)" }}>Mark all read</button>}
          <Link to={SETTINGS} onClick={() => setOpen(false)} className="font-medium text-ink-dim hover:text-[var(--ink)]">Settings</Link>
        </div>
      </div>

      {error && <div className="px-3.5 py-1.5 text-xs font-medium" style={{ color: "var(--reject-text)", borderBottom: "1px solid var(--line-soft)" }}>{error}</div>}

      <div className="thin-scroll max-h-[360px] overflow-y-auto">
        {items.length === 0 ? (
          <div className="px-3.5 py-8 text-center text-sm text-ink-faint">
            Nothing yet. When a request you made is ready, it’ll show up here.
            <div className="mt-1.5"><Link to={SETTINGS} onClick={() => setOpen(false)} style={{ color: "var(--accent-text)" }}>Turn on notifications in Settings</Link></div>
          </div>
        ) : items.map((n) => {
          const poster = posterOf(n);
          return (
          <div key={n.id} style={{ borderTop: "1px solid var(--line-soft)", background: n.read ? "transparent" : "var(--accent-soft)" }}>
            <button onClick={() => clickItem(n)} className="flex w-full items-start gap-2.5 px-3.5 py-2.5 text-left">
              <span className="mt-1 h-2 w-2 flex-none rounded-full" style={{ background: n.read ? "transparent" : "var(--accent)" }} />
              {poster && <img src={poster} alt="" loading="lazy" className="h-[54px] w-9 flex-none rounded object-cover" style={{ background: "var(--panel-2)" }} onError={(e) => { e.currentTarget.style.display = "none"; }} />}
              <span className="min-w-0">
                <span className="block text-base font-semibold">{n.title}</span>
                <span className="block whitespace-pre-line text-sm text-ink-dim">{n.body}</span>
              </span>
            </button>
            {/* A ready title a media server has: straight to it there, one button per server,
                the reader's own first (the row itself opens it here). */}
            <BellWatchLinks links={watchLinksOf(n)} onOpen={() => { void markRead(n); }} indent={!!poster} />
          </div>
          );
        })}
      </div>
    </div>
  );

  return (
    <div className="relative" ref={ref}>
      <button onClick={toggle} className="relative grid h-9 w-9 place-items-center rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} aria-label="Notifications" aria-expanded={open}>
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /><path d="M13.7 21a2 2 0 0 1-3.4 0" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        {unread > 0 && <span className="absolute -right-1 -top-1 grid h-4 min-w-4 place-items-center rounded-full px-1 text-xs font-bold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{unread > 9 ? "9+" : unread}</span>}
      </button>

      {open && (side ? createPortal(panel, document.body) : panel)}
    </div>
  );
}
