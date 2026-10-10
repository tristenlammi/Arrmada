import { lazy, Suspense, useEffect, useState } from "react";
import { NavLink } from "react-router-dom";
import { visibleNav } from "../lib/nav";
import { useMe, isAdmin } from "../lib/me";
import { api } from "../lib/api";
import { badgeFor, useAttention, type NavBadge } from "../lib/useAttention";
import { FleetMark } from "./FleetMark";
import { Icon } from "./icons";
import { NotificationBell } from "./NotificationBell";

const PlexAccountLink = lazy(() => import("./PlexAccountLink"));

const PILL_TONE: Record<NavBadge["tone"], { background: string; color: string }> = {
  accent: { background: "var(--accent-soft)", color: "var(--accent)" },
  reject: { background: "var(--reject-soft)", color: "var(--reject)" },
  avoid: { background: "var(--avoid-soft)", color: "var(--avoid)" },
};

// CountPill is a sidebar entry's live count; nothing at zero.
function CountPill({ badge }: { badge: NavBadge | null }) {
  if (!badge) return null;
  return (
    <>
      <span
        className="ml-auto min-w-[18px] rounded-full px-1.5 py-px text-center font-mono text-[10px] font-semibold"
        style={PILL_TONE[badge.tone]}
        title={badge.label}
        aria-hidden
      >
        {badge.count > 99 ? "99+" : badge.count}
      </span>
      <span className="sr-only">, {badge.label}</span>
    </>
  );
}

function toggleTheme() {
  const root = document.documentElement;
  const current =
    root.getAttribute("data-theme") ??
    (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  root.setAttribute("data-theme", current === "dark" ? "light" : "dark");
}

// bell: this screen is wide enough that the sidebar is always open, so the inbox bell
// lives in its brand row (AppLayout's compact bar has it otherwise).
export function Sidebar({ open, onClose, bell = false }: { open: boolean; onClose: () => void; bell?: boolean }) {
  const { user, booksEnabled, musicEnabled } = useMe();
  // Count pills from the Needs-you feed (the staff layout polls it; this only reads).
  const attention = useAttention();
  // A dot beside Audiobooks: green when the server is running, red when it's switched on
  // but couldn't start, nothing when it's off.
  const [audioDot, setAudioDot] = useState<string | null>(null);
  useEffect(() => {
    api.myAudio()
      .then((a) => setAudioDot(a.enabled ? (a.running ? "var(--good)" : "var(--reject)") : null))
      .catch(() => {});
  }, []);
  const signOut = async () => {
    try { await api.logout(); } catch { /* ignore — clearing the session locally is enough */ }
    window.location.href = "/";
  };
  // Hide nav entries for modules an admin has turned off, and admin-only pages (Logs)
  // from managers.
  const nav = visibleNav({ admin: isAdmin(user), booksEnabled, musicEnabled });
  return (
    <>
      {/* Dim + click-to-close backdrop, only while the drawer is open on narrow windows. */}
      {open && <div className="fixed inset-0 z-40 bg-black/50 lg:hidden" onClick={onClose} />}

      <aside
        className={`fixed inset-y-0 left-0 z-50 flex w-[236px] flex-none transform flex-col overflow-y-auto bg-sidebar transition-transform duration-200 lg:static lg:z-auto lg:translate-x-0 ${
          open ? "translate-x-0" : "-translate-x-full"
        }`}
        // As a drawer in the installed iPhone app it runs edge to edge: keep the brand row
        // below the status bar and the last link above the home indicator.
        style={{ borderRight: "1px solid var(--line)", paddingTop: "env(safe-area-inset-top, 0px)", paddingBottom: "env(safe-area-inset-bottom, 0px)" }}
      >
        <div className="flex items-center gap-2.5 px-[18px] pb-3 pt-[18px]">
          <span
            className="grid h-[30px] w-[30px] place-items-center rounded-[9px]"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            <FleetMark className="h-[18px] w-[18px]" />
          </span>
          <span className="text-[15px] font-extrabold tracking-[0.12em]">ARRMADA</span>
          {bell && <div className="ml-auto"><NotificationBell placement="side" /></div>}
        </div>

        <nav className="flex flex-col gap-0.5 px-2.5 pt-2">
          {nav.map((group, gi) => (
            <div key={gi} className="flex flex-col gap-0.5">
              {group.group && (
                <div className="px-2.5 pb-1.5 pt-3.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.12em] text-ink-faint">
                  {group.group}
                </div>
              )}
              {group.items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  onClick={onClose}
                  className="flex w-full items-center gap-2.5 rounded-[9px] px-2.5 py-2 text-left text-[13.5px] transition-colors"
                  style={({ isActive }) =>
                    isActive
                      ? { background: "var(--accent-soft)", color: "var(--accent)", fontWeight: 600 }
                      : { color: "var(--ink-dim)" }
                  }
                >
                  <Icon name={item.icon} className="flex-none" />
                  {item.label}
                  {item.tag && (
                    <span
                      className="ml-auto rounded-full px-1.5 py-px font-mono text-[10.5px] uppercase"
                      style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}
                    >
                      {item.tag}
                    </span>
                  )}
                  {item.status === "audiobook-server" && audioDot && (
                    <span className="ml-auto h-1.5 w-1.5 rounded-full" style={{ background: audioDot }} title={audioDot === "var(--good)" ? "Running" : "Switched on but not running"} />
                  )}
                  {item.badge && <CountPill badge={badgeFor(item.badge, attention?.counts)} />}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>

        <div className="mt-auto flex items-center gap-2.5 p-3.5" style={{ borderTop: "1px solid var(--line)" }}>
          <span className="grid h-[30px] w-[30px] flex-none place-items-center rounded-full bg-[#5f5142] text-xs font-bold text-white">
            {(user?.username?.[0] ?? "?").toUpperCase()}
          </span>
          <div className="min-w-0">
            <div className="truncate text-[12.5px] font-semibold" title={user?.username}>{user?.username ?? "…"}</div>
            <div className="font-mono text-[10.5px] capitalize text-ink-faint">{user?.role ?? ""}</div>
          </div>
          <button
            onClick={toggleTheme}
            title="Toggle theme"
            aria-label="Toggle theme"
            className="ml-auto grid h-[30px] w-[30px] flex-none place-items-center rounded-lg"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
              <path d="M21 12.8A9 9 0 1111.2 3a7 7 0 009.8 9.8z" fill="currentColor" />
            </svg>
          </button>
          <button
            onClick={signOut}
            title="Sign out"
            aria-label="Sign out"
            className="grid h-[30px] w-[30px] flex-none place-items-center rounded-lg"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--reject)" }}
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4M16 17l5-5-5-5M21 12H9" />
            </svg>
          </button>
        </div>
        {/* Your own Plex link (Discover's recommendations use its watch history). */}
        <Suspense fallback={null}><PlexAccountLink variant="sidebar" /></Suspense>
      </aside>
    </>
  );
}
