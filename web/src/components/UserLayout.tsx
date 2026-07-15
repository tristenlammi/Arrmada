import { useState } from "react";
import { Outlet } from "react-router-dom";
import { FleetMark } from "./FleetMark";
import { useMe } from "../lib/me";
import { api } from "../lib/api";

// UserLayout is the requester-facing shell: no nav menu, just a slim branded top bar
// and the Discover experience. This is what installs as the PWA on phones.
export function UserLayout() {
  const { user } = useMe();
  const [menu, setMenu] = useState(false);

  const logout = async () => {
    try { await api.logout(); } catch { /* ignore */ }
    window.location.href = "/";
  };

  return (
    <div className="flex h-full flex-col font-sans">
      <header className="flex items-center justify-between px-4 py-2.5 sm:px-6" style={{ borderBottom: "1px solid var(--line)", background: "var(--sidebar)" }}>
        <div className="flex items-center gap-2.5">
          <span className="grid h-[28px] w-[28px] place-items-center rounded-lg" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-[16px] w-[16px]" />
          </span>
          <span className="text-[15px] font-extrabold tracking-[0.12em]">ARRMADA</span>
        </div>
        <div className="relative">
          <button onClick={() => setMenu((m) => !m)} className="grid h-8 w-8 place-items-center rounded-full text-[12px] font-bold" style={{ background: "var(--accent-soft)", color: "var(--accent)", border: "1px solid var(--accent-line)" }}>
            {(user?.username?.[0] ?? "?").toUpperCase()}
          </button>
          {menu && (
            <>
              <div className="fixed inset-0 z-40" onClick={() => setMenu(false)} />
              <div className="absolute right-0 z-50 mt-2 w-[200px] rounded-xl p-2" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
                <div className="truncate px-2.5 py-1.5 text-[12px] text-ink-dim" title={user?.username}>{user?.username || "Guest"}</div>
                <div className="my-1 h-px" style={{ background: "var(--line)" }} />
                <button onClick={logout} className="w-full rounded-lg px-2.5 py-1.5 text-left text-[12.5px] font-medium hover:bg-[var(--panel-2)]" style={{ color: "var(--reject)" }}>Sign out</button>
              </div>
            </>
          )}
        </div>
      </header>
      <main className="min-w-0 flex-1 overflow-y-auto">
        <Outlet />
      </main>
    </div>
  );
}
