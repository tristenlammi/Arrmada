import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type UserNotification } from "../lib/api";

// Pull the media title out of a notification: bodies read like “Dune” is ready to
// watch — the quoted part is the title. Falls back to the whole body if nothing is
// quoted (the title field is generic, e.g. "Your request is ready").
function searchTitleOf(n: UserNotification): string {
  const m = n.body?.match(/[“"]([^”"]+)[”"]/) || n.title?.match(/[“"]([^”"]+)[”"]/);
  return m ? m[1] : "";
}

// NotificationBell is the requester-facing inbox: a bell with an unread badge that opens a
// dropdown of "your request is ready" notifications, plus a place to set a personal Apprise URL
// for an external push. Polls the unread count on an interval.
export function NotificationBell() {
  const [items, setItems] = useState<UserNotification[]>([]);
  const [unread, setUnread] = useState(0);
  const [open, setOpen] = useState(false);
  const [settings, setSettings] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();

  const load = () => api.myNotifications().then((r) => { setItems(r.notifications ?? []); setUnread(r.unread); }).catch(() => {});
  useEffect(() => { load(); const t = setInterval(load, 30000); return () => clearInterval(t); }, []);

  // Close on outside click.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const toggle = () => { const next = !open; setOpen(next); if (next) { setError(null); load(); } };
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
  // Mark read (badge only drops when the server call succeeds), then jump to Discover
  // with the title prefilled in search.
  const clickItem = async (n: UserNotification) => {
    if (!n.read) {
      try {
        await api.markNotificationRead(n.id);
        setItems((xs) => xs.map((x) => (x.id === n.id ? { ...x, read: true } : x)));
        setUnread((u) => Math.max(0, u - 1));
      } catch (e) {
        console.warn("mark notification read failed", e);
      }
    }
    const title = searchTitleOf(n);
    if (title) {
      setOpen(false);
      navigate(n.media_type === "book" ? `/discover?tab=books&q=${encodeURIComponent(title)}` : `/discover?q=${encodeURIComponent(title)}`);
    }
  };

  return (
    <div className="relative" ref={ref}>
      <button onClick={toggle} className="relative grid h-9 w-9 place-items-center rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} aria-label="Notifications">
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /><path d="M13.7 21a2 2 0 0 1-3.4 0" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        {unread > 0 && <span className="absolute -right-1 -top-1 grid h-4 min-w-4 place-items-center rounded-full px-1 text-[9px] font-bold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{unread > 9 ? "9+" : unread}</span>}
      </button>

      {open && (
        <div className="absolute right-0 z-50 mt-2 w-[340px] overflow-hidden rounded-xl" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
          <div className="flex items-center justify-between px-3.5 py-2.5" style={{ borderBottom: "1px solid var(--line)" }}>
            <span className="text-[13px] font-bold">Notifications</span>
            <div className="flex items-center gap-3 text-[11px]">
              {items.some((i) => !i.read) && <button onClick={markAll} style={{ color: "var(--accent)" }}>Mark all read</button>}
              <button onClick={() => setSettings((s) => !s)} className="text-ink-faint hover:text-[var(--ink)]" aria-label="Notification settings">⚙</button>
            </div>
          </div>

          {error && <div className="px-3.5 py-1.5 text-[11px] font-medium" style={{ color: "var(--reject)", borderBottom: "1px solid var(--line-soft)" }}>{error}</div>}

          {settings && <AppriseSetting />}

          <div className="thin-scroll max-h-[360px] overflow-y-auto">
            {items.length === 0 ? (
              <div className="px-3.5 py-8 text-center text-[12px] text-ink-faint">Nothing yet. When a request you made is ready, it’ll show up here.</div>
            ) : items.map((n) => (
              <button key={n.id} onClick={() => clickItem(n)} className="flex w-full items-start gap-2.5 px-3.5 py-2.5 text-left" style={{ borderTop: "1px solid var(--line-soft)", background: n.read ? "transparent" : "var(--accent-soft)" }}>
                <span className="mt-1 h-2 w-2 flex-none rounded-full" style={{ background: n.read ? "transparent" : "var(--accent)" }} />
                <span className="min-w-0">
                  <span className="block text-[12.5px] font-semibold">{n.title}</span>
                  <span className="block text-[11.5px] text-ink-dim">{n.body}</span>
                </span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function AppriseSetting() {
  const [url, setUrl] = useState("");
  const [set, setSet] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.myApprise().then((r) => { setSet(r.set); setUrl(r.url); }).catch(() => {}); }, []);
  const save = async () => {
    setError(null);
    try {
      const r = await api.setMyApprise(url.trim());
      setSet(r.set);
      setSaved(true);
      window.setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <div className="px-3.5 py-3" style={{ background: "var(--panel-2)", borderBottom: "1px solid var(--line)" }}>
      <div className="text-[11.5px] font-semibold">Push notifications (optional)</div>
      <div className="mb-1.5 text-[10.5px] text-ink-faint">Paste your own Apprise URL to also get pushed (Discord, ntfy, email…). Leave blank for in-app only.{set ? " Currently set." : ""}</div>
      <div className="flex gap-1.5">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="ntfy://topic or discord://id/token" className="flex-1 rounded-lg px-2 py-1 font-mono text-[11px]" style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <button onClick={save} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{saved ? "✓" : "Save"}</button>
      </div>
      {error && <div className="mt-1.5 text-[10.5px] font-medium" style={{ color: "var(--reject)" }}>Couldn’t save — {error}</div>}
    </div>
  );
}
