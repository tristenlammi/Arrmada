import { Suspense, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { api, type UserNotification } from "../lib/api";
import { lazyPage } from "../lib/lazyPage";
import { refToPath } from "../lib/refLink";
import { usePoll } from "../lib/usePoll";

// The settings panel (push on this device, a personal Apprise link) loads when ⚙ is
// first tapped, keeping lib/webpush out of the requester first load.
const NotificationSettings = lazyPage(() => import("./NotificationSettings"), "NotificationSettings");

// Pull the media title out of a notification: bodies read like “Dune” is ready to
// watch — the quoted part is the title. Falls back to the whole body if nothing is
// quoted (the title field is generic, e.g. "Your request is ready").
function searchTitleOf(n: UserNotification): string {
  const m = n.body?.match(/[“"]([^”"]+)[”"]/) || n.title?.match(/[“"]([^”"]+)[”"]/);
  return m ? m[1] : "";
}

// NotificationBell is everyone's inbox: a bell with an unread badge that opens a dropdown
// of notifications (a request ready, approved or declined; staff also hear of new
// requests), plus a place to set up push. Every layout mounts exactly one, in its top bar
// or, on a wide staff screen, the sidebar. Polls the unread count on an interval.
//
// placement "side": the bell sits in the sidebar, which scrolls and so would clip a
// dropdown; the panel opens beside the sidebar instead, outside it.
export function NotificationBell({ placement = "down" }: { placement?: "down" | "side" }) {
  const [items, setItems] = useState<UserNotification[]>([]);
  const [unread, setUnread] = useState(0);
  const [open, setOpen] = useState(false);
  const [settings, setSettings] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [anchor, setAnchor] = useState<{ left: number; top: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const side = placement === "side";

  const load = () => api.myNotifications().then((r) => { setItems(r.notifications ?? []); setUnread(r.unread); }).catch(() => {});
  usePoll(load, 30000);

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
        <span className="text-[13px] font-bold">Notifications</span>
        <div className="flex items-center gap-3 text-[11px]">
          {items.some((i) => !i.read) && <button onClick={markAll} style={{ color: "var(--accent)" }}>Mark all read</button>}
          <button onClick={() => setSettings((s) => !s)} className="text-ink-faint hover:text-[var(--ink)]" aria-label="Notification settings">⚙</button>
        </div>
      </div>

      {error && <div className="px-3.5 py-1.5 text-[11px] font-medium" style={{ color: "var(--reject)", borderBottom: "1px solid var(--line-soft)" }}>{error}</div>}

      {settings && (
        <Suspense fallback={<div className="px-3.5 py-2.5 text-[11px] text-ink-faint">Loading…</div>}>
          <NotificationSettings />
        </Suspense>
      )}

      <div className="thin-scroll max-h-[360px] overflow-y-auto">
        {items.length === 0 ? (
          <div className="px-3.5 py-8 text-center text-[12px] text-ink-faint">Nothing yet. When a request you made is ready, it’ll show up here.</div>
        ) : items.map((n) => (
          <div key={n.id} style={{ borderTop: "1px solid var(--line-soft)", background: n.read ? "transparent" : "var(--accent-soft)" }}>
            <button onClick={() => clickItem(n)} className="flex w-full items-start gap-2.5 px-3.5 py-2.5 text-left">
              <span className="mt-1 h-2 w-2 flex-none rounded-full" style={{ background: n.read ? "transparent" : "var(--accent)" }} />
              <span className="min-w-0">
                <span className="block text-[12.5px] font-semibold">{n.title}</span>
                <span className="block whitespace-pre-line text-[11.5px] text-ink-dim">{n.body}</span>
              </span>
            </button>
            {/* A ready title Plex has: straight to it in Plex (the row itself opens it here). */}
            {n.plex_url && (
              <a href={n.plex_url} target="_blank" rel="noopener noreferrer" onClick={() => { void markRead(n); }} className="-mt-1 mb-2.5 ml-[30px] inline-flex min-h-[30px] items-center rounded-md bg-accent-grad px-2.5 text-[11.5px] font-semibold text-accent-ink">▶ Watch on Plex</a>
            )}
          </div>
        ))}
      </div>
    </div>
  );

  return (
    <div className="relative" ref={ref}>
      <button onClick={toggle} className="relative grid h-9 w-9 place-items-center rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} aria-label="Notifications" aria-expanded={open}>
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /><path d="M13.7 21a2 2 0 0 1-3.4 0" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        {unread > 0 && <span className="absolute -right-1 -top-1 grid h-4 min-w-4 place-items-center rounded-full px-1 text-[9px] font-bold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{unread > 9 ? "9+" : unread}</span>}
      </button>

      {open && (side ? createPortal(panel, document.body) : panel)}
    </div>
  );
}
