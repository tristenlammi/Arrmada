import { useState } from "react";
import { Outlet, useLocation } from "react-router-dom";
import { Sidebar } from "./Sidebar";
import { FleetMark } from "./FleetMark";
import { ErrorBoundary } from "./ErrorBoundary";
import { RestartBanner } from "./RestartBanner";
import { useDocumentTitle } from "../lib/title";

export function AppLayout() {
  const [navOpen, setNavOpen] = useState(false);
  const { pathname } = useLocation();
  useDocumentTitle();
  return (
    <div className="flex h-full font-sans">
      <Sidebar open={navOpen} onClose={() => setNavOpen(false)} />
      <div className="flex min-w-0 flex-1 flex-col">
        {/* Compact top bar — only on narrow windows where the sidebar is a drawer. */}
        <div className="flex items-center gap-3 px-4 py-2.5 lg:hidden" style={{ borderBottom: "1px solid var(--line)", background: "var(--sidebar)" }}>
          <button onClick={() => setNavOpen(true)} aria-label="Open menu" className="grid h-9 w-9 place-items-center rounded-lg" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none"><path d="M4 6h16M4 12h16M4 18h16" stroke="currentColor" strokeWidth="2" strokeLinecap="round" /></svg>
          </button>
          <span className="grid h-[26px] w-[26px] place-items-center rounded-lg" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-[15px] w-[15px]" />
          </span>
          <span className="text-[14px] font-extrabold tracking-[0.12em]">ARRMADA</span>
        </div>
        <RestartBanner />
        <main className="min-w-0 flex-1 overflow-y-auto">
          {/* Keyed by path: a broken page shows its error card inside the shell, and
              navigating elsewhere clears it. */}
          <ErrorBoundary resetKey={pathname}>
            <Outlet />
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}
