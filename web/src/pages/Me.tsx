import { lazy, Suspense, useEffect, type ReactNode } from "react";
import { Link, useLocation } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { lazyPage } from "../lib/lazyPage";
import { useMe } from "../lib/me";
import { signOut } from "../lib/session";
import type { UserRole } from "../lib/api";

// "Get notified": push on this device, then a personal Apprise link under Advanced. The
// bell's Settings button lands here (/me#notifications).
const NotificationSettings = lazyPage(() => import("../components/NotificationSettings"), "NotificationSettings");
// Linking your Plex account (Discover's "Recommended for you", Sign in with Plex). Its own
// chunk, with the Plex sign-in code behind it.
const PlexAccountLink = lazy(() => import("../components/PlexAccountLink"));
// Your password and your other devices (every role).
const AccountSettings = lazyPage(() => import("../components/AccountSettings"), "AccountSettings");

const ROLE: Record<UserRole, string> = {
  admin: "Admin",
  manager: "Manager",
  requester: "Can request",
  readonly: "Can browse",
};

// Me is the one page about you: who you're signed in as, how Arrmada reaches you, and the
// places that don't get a tab of their own. It's the phone's last tab, the avatar menu's
// first item and the staff sidebar's name. Each part is its own MeSection, in this order,
// so later work slots in without a second account page:
//   Get notified — the one push switch for this device, with a plain answer when it can't
//                  be on, then Apprise under Advanced;
//   Plex         — link your Plex account (PLEX-14); plex.tv's redirect comes back here;
//   Account      — change your password (or set a first one after Plex sign-in) and sign
//                  out your other devices;
//   More, Sign out.
export function Me({ chrome = false }: { chrome?: boolean }) {
  const { user, external } = useMe();
  const name = user?.username || "Guest";
  useHashScroll();
  return (
    <>
      {chrome && <PageHeader title="Me" crumb={null} />}
      <div className="mx-auto flex w-full max-w-[640px] flex-col gap-5 px-4 py-5 sm:px-6">
        <div className="flex items-center gap-3.5">
          <span className="grid h-14 w-14 flex-none place-items-center rounded-full text-[22px] font-bold" style={{ background: "var(--accent-soft)", color: "var(--accent)", border: "1px solid var(--accent-line)" }} aria-hidden>
            {(name[0] ?? "?").toUpperCase()}
          </span>
          <div className="min-w-0">
            <h1 className="m-0 truncate text-[20px] font-bold">{name}</h1>
            {user && <div className="text-[12.5px] text-ink-dim">{ROLE[user.role] ?? user.role}</div>}
          </div>
        </div>

        <MeSection id="notifications" title="Get notified">
          <Suspense fallback={<Loading />}>
            <NotificationSettings />
          </Suspense>
        </MeSection>

        <MeSection id="plex" title="Plex">
          <Suspense fallback={<Loading />}>
            <PlexAccountLink variant="me" />
          </Suspense>
        </MeSection>

        <MeSection id="account" title="Account">
          <Suspense fallback={<Loading />}>
            <AccountSettings />
          </Suspense>
        </MeSection>

        <MeSection id="more" title="More">
          {/* Calendar has no phone tab while Requests does; from outside the network the
              server doesn't serve it at all. */}
          {!external && <MeRow to="/calendar">Calendar</MeRow>}
          <MeRow to="/audiobooks?tab=apps">Audiobook apps &amp; password</MeRow>
        </MeSection>

        <MeSection id="session">
          <button onClick={() => { void signOut(); }} className="flex min-h-[48px] w-full items-center px-3.5 text-left text-[13px] font-semibold" style={{ color: "var(--reject)" }}>
            Sign out
          </button>
        </MeSection>
      </div>
    </>
  );
}

function Loading() {
  return <div className="px-3.5 py-3 text-[12px] text-ink-faint">Loading…</div>;
}

// useHashScroll brings /me#notifications (the bell's Settings button) to its section. The
// router doesn't scroll to a hash, and the bell can send you here from /me itself.
function useHashScroll() {
  const { hash } = useLocation();
  useEffect(() => {
    const id = hash.slice(1);
    if (id) document.getElementById(id)?.scrollIntoView({ block: "start" });
  }, [hash]);
}

// MeSection is one card on the Me page; id makes it linkable (/me#notifications).
export function MeSection({ id, title, children }: { id: string; title?: string; children: ReactNode }) {
  return (
    <section id={id} aria-label={title} className="scroll-mt-4">
      {title && <h2 className="m-0 mb-2 px-1 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-ink-faint">{title}</h2>}
      <div className="overflow-hidden rounded-xl" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
        {children}
      </div>
    </section>
  );
}

function MeRow({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className="flex min-h-[48px] items-center justify-between gap-2 px-3.5 text-[13px] font-medium [&+&]:border-t [&+&]:border-[var(--line-soft)]" style={{ color: "var(--ink)" }}>
      {children}
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden style={{ color: "var(--ink-faint)" }}><path d="M9 6l6 6-6 6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
    </Link>
  );
}
