import { Suspense, useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router-dom";
import { Sidebar } from "./Sidebar";
import { FleetMark } from "./FleetMark";
import { ErrorBoundary } from "./ErrorBoundary";
import { PageSkeleton } from "./PageSkeleton";
import { RestartBanner } from "./RestartBanner";
import { NotificationBell } from "./NotificationBell";
import { PushPromptHost } from "./PushPromptHost";
import { useDocumentTitle } from "../lib/title";
import { prefetchStaffPages } from "../lib/prefetch";
import { useAttention, useAttentionPoll } from "../lib/useAttention";

// Tailwind's lg: the sidebar is a drawer below it and always open from it.
const WIDE = "(min-width: 1024px)";
function wideNow(): boolean {
  try { return window.matchMedia(WIDE).matches; } catch { return true; }
}

// useWide follows the lg breakpoint, so the bell renders once — in the compact top bar
// while the sidebar is a drawer, in the sidebar's brand row once it's always open —
// rather than twice with one hidden, which would poll the inbox twice.
function useWide(): boolean {
  const [wide, setWide] = useState(wideNow);
  useEffect(() => {
    let mq: MediaQueryList;
    try { mq = window.matchMedia(WIDE); } catch { return; }
    const onChange = () => setWide(mq.matches);
    onChange();
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return wide;
}

export function AppLayout() {
  const [navOpen, setNavOpen] = useState(false);
  const { pathname } = useLocation();
  const wide = useWide();
  useDocumentTitle();
  // Only staff get this layout, so this is the "staff signed in" moment.
  useEffect(() => prefetchStaffPages(), []);
  // The one Needs-you poller for this tab: the sidebar pills, the menu dot below and the
  // Dashboard card all read its answer.
  useAttentionPoll();
  const attention = useAttention();
  const needsYou = attention?.counts.total ?? 0;
  const urgent = (attention?.groups ?? []).some((g) => g.level === "error");
  return (
    <div className="flex h-full font-sans">
      <Sidebar open={navOpen} onClose={() => setNavOpen(false)} bell={wide} />
      <div className="flex min-w-0 flex-1 flex-col">
        {/* Compact top bar — only on narrow windows where the sidebar is a drawer. It
            clears the iPhone status bar and notch when installed as an app. */}
        <div
          className="flex items-center gap-3 pb-2.5 lg:hidden"
          style={{
            borderBottom: "1px solid var(--line)",
            background: "var(--sidebar)",
            paddingTop: "max(10px, env(safe-area-inset-top, 0px))",
            paddingLeft: "max(1rem, env(safe-area-inset-left, 0px))",
            paddingRight: "max(1rem, env(safe-area-inset-right, 0px))",
          }}
        >
          <button onClick={() => setNavOpen(true)} aria-label={needsYou > 0 ? "Open menu — something needs you" : "Open menu"} className="relative grid h-9 w-9 place-items-center rounded-lg" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none"><path d="M4 6h16M4 12h16M4 18h16" stroke="currentColor" strokeWidth="2" strokeLinecap="round" /></svg>
            {/* The sidebar is a drawer here, so its count pills are out of sight: a dot says
                there's something in it worth opening for. */}
            {needsYou > 0 && (
              <span
                data-testid="needs-you-dot"
                className="absolute -right-1 -top-1 h-2.5 w-2.5 rounded-full"
                style={{ background: urgent ? "var(--reject)" : "var(--accent)", border: "2px solid var(--sidebar)" }}
              />
            )}
          </button>
          <span className="grid h-[26px] w-[26px] place-items-center rounded-lg" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-[15px] w-[15px]" />
          </span>
          <span className="text-[14px] font-extrabold tracking-[0.12em]">ARRMADA</span>
          {!wide && <div className="ml-auto"><NotificationBell /></div>}
        </div>
        <RestartBanner />
        <PushPromptHost />
        <main className="min-w-0 flex-1 overflow-y-auto">
          {/* Keyed by path: a broken page shows its error card inside the shell, and
              navigating elsewhere clears it. */}
          <ErrorBoundary resetKey={pathname}>
            <Suspense fallback={<PageSkeleton />}>
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}
