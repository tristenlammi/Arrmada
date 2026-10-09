import { useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { FleetMark } from "./FleetMark";
import { ErrorBoundary } from "./ErrorBoundary";
import { useMe } from "../lib/me";
import { api } from "../lib/api";
import { requesterNav } from "../lib/nav";

// UserLayout is the requester-facing shell: no nav menu, just a slim branded top bar
// over Discover, Calendar, Books and Audiobooks. This is what installs as the PWA on phones.
export function UserLayout() {
  const { user, external, booksEnabled } = useMe();
  const [menu, setMenu] = useState(false);
  const { pathname } = useLocation();
  // Outside sessions get no Calendar (the /calendar route isn't mounted or
  // allowlisted for them) — don't show a link that silently bounces. "Your books"
  // is the exception: its two endpoints are allowlisted, so a requester can pick up
  // an ebook from anywhere.
  const nav = requesterNav({ external, booksEnabled });

  const logout = async () => {
    try { await api.logout(); } catch { /* ignore */ }
    window.location.href = "/";
  };

  return (
    <div className="flex h-full flex-col font-sans">
      {/* On a phone the wordmark drops out and the nav scrolls sideways inside its own
          strip, so the header never pushes the page wider than the screen. */}
      <header className="flex items-center justify-between gap-2 px-4 py-2.5 sm:px-6" style={{ borderBottom: "1px solid var(--line)", background: "var(--sidebar)" }}>
        <div className="flex flex-none items-center gap-2.5">
          <span className="grid h-[28px] w-[28px] place-items-center rounded-lg" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-[16px] w-[16px]" />
          </span>
          <span className="hidden text-[15px] font-extrabold tracking-[0.12em] sm:inline">ARRMADA</span>
        </div>
        <nav className="thin-scroll flex min-w-0 flex-1 items-center gap-1 overflow-x-auto whitespace-nowrap sm:flex-initial">
          {nav.map((n) => (
            <NavLink key={n.to} to={n.to} className="flex-none rounded-lg px-2 py-1.5 text-[12.5px] font-semibold sm:px-3" style={({ isActive }) => ({ background: isActive ? "var(--accent-soft)" : "transparent", color: isActive ? "var(--accent)" : "var(--ink-dim)" })}>{n.label}</NavLink>
          ))}
        </nav>
        <div className="relative flex-none">
          <button onClick={() => setMenu((m) => !m)} className="grid h-8 w-8 place-items-center rounded-full text-[12px] font-bold" style={{ background: "var(--accent-soft)", color: "var(--accent)", border: "1px solid var(--accent-line)" }}>
            {(user?.username?.[0] ?? "?").toUpperCase()}
          </button>
          {menu && (
            <>
              <div className="fixed inset-0 z-40" onClick={() => setMenu(false)} />
              <div className="absolute right-0 z-50 mt-2 w-[200px] rounded-xl p-2" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
                <div className="truncate px-2.5 py-1.5 text-[12px] text-ink-dim" title={user?.username}>{user?.username || "Guest"}</div>
                <div className="my-1 h-px" style={{ background: "var(--line)" }} />
                <Link to="/audiobooks" onClick={() => setMenu(false)} className="block w-full rounded-lg px-2.5 py-1.5 text-left text-[12.5px] font-medium hover:bg-[var(--panel-2)]" style={{ color: "var(--ink)" }}>Audiobook password</Link>
                <button onClick={logout} className="w-full rounded-lg px-2.5 py-1.5 text-left text-[12.5px] font-medium hover:bg-[var(--panel-2)]" style={{ color: "var(--reject)" }}>Sign out</button>
              </div>
            </>
          )}
        </div>
      </header>
      <main className="min-w-0 flex-1 overflow-y-auto">
        {/* Keyed by path so navigating away from a broken page recovers. */}
        <ErrorBoundary resetKey={pathname}>
          <Outlet />
        </ErrorBoundary>
      </main>
    </div>
  );
}
