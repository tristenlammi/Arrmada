import { lazy, Suspense, useEffect, useState } from "react";
import { FleetMark } from "../components/FleetMark";
import { EmbyMark, JellyfinMark, PlexMark } from "../components/brand/marks";
import { setJellyfinSlotKind } from "../components/brand/servers";
import { api, resetSignedOut, type AuthUser, type LoginAnswer } from "../lib/api";
import { nextAfterSignIn } from "../lib/session";
import { Field } from "../ui/Field";
import { Input } from "../ui/Input";

const PlexLoginButton = lazy(() => import("../components/PlexLoginButton"));
// The Jellyfin sheet (username + password, or Quick Connect) downloads when it's opened.
const JellyfinLoginSheet = lazy(() => import("../components/JellyfinSignInSheet"));
// Two-factor sign-in's code step (or an admin's required setup) downloads when a right
// password asks for it.
const TwoFactorLogin = lazy(() => import("../components/twofactor/TwoFactorLogin"));

// The media server buttons: the server's mark, then its name, on its brand fill.
const SERVER_BUTTON ="flex items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-md font-semibold";

// After any successful sign-in: back to the page they were on (or ?next=), else their home —
// the Dashboard for staff, Discover for everyone else.
// A full load, so MeProvider and /status start fresh for the new session.
function continueAfterSignIn(user?: AuthUser | null) {
  resetSignedOut();
  window.location.replace(nextAfterSignIn(user?.role));
}

// Login is shown when auth is enabled and no session is active. It doubles as the
// first-run setup screen (create the first admin) when the instance has no users yet.
// signedOut: the session ended while the app was open (expired, revoked, password changed).
export function Login({ signedOut = false }: { signedOut?: boolean }) {
  // remote: setup is needed but refused from here (outside the home network).
  const [mode, setMode] = useState<"loading" | "login" | "setup" | "remote">("loading");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [plexEnabled, setPlexEnabled] = useState(false);
  const [plexBusy, setPlexBusy] = useState(false);
  const [jellyfin, setJellyfin] = useState({ enabled: false, quickConnect: false, emby: false, open: false });
  const [error, setError] = useState<string | null>(null);
  // The two-factor step after a right password: a ticket, never a session.
  const [second, setSecond] = useState<{ step: "code" | "setup"; ticket: string } | null>(null);

  useEffect(() => {
    api.status().then((s) => {
      setMode(!s.needs_setup ? "login" : s.setup_blocked ? "remote" : "setup");
      setPlexEnabled(s.plex_login);
      setJellyfinSlotKind(s.jellyfin_server_kind);
      setJellyfin((j) => ({ ...j, enabled: !!s.jellyfin_login, quickConnect: !!s.jellyfin_quickconnect, emby: s.jellyfin_server_kind === "emby" }));
    }).catch(() => setMode("login"));
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      // A new install's first admin lands on "/", where the setup wizard opens.
      const r: LoginAnswer = mode === "setup" ? await api.setupAdmin(email.trim(), password) : await api.login(email.trim(), password);
      if (r.two_factor) {
        setPassword("");
        setSecond({ step: r.two_factor, ticket: r.ticket });
        setBusy(false);
        return;
      }
      continueAfterSignIn(r.user);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  // Plex and Jellyfin sign-in into a staff account with two-factor on ask for the code too.
  const afterServerSignIn = (r: LoginAnswer) => {
    if (!r.two_factor) { continueAfterSignIn(r.user); return; }
    setJellyfin((j) => ({ ...j, open: false }));
    setSecond({ step: r.two_factor, ticket: r.ticket });
  };

  const setup = mode === "setup";

  return (
    <div className="grid h-full place-items-center px-5" style={{ background: "var(--bg)" }}>
      <div className="w-full max-w-[380px]">
        <div className="mb-6 flex flex-col items-center gap-2.5 text-center">
          <span className="grid h-12 w-12 place-items-center rounded-2xl" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-6 w-6" />
          </span>
          <h1 className="m-0 text-2xl font-extrabold tracking-[0.14em]">ARRMADA</h1>
          <p className="m-0 text-base text-ink-dim">{mode === "loading" ? "…" : mode !== "login" ? "Create your admin account to get started." : "Sign in to discover and request."}</p>
        </div>

        {second && (
          <div className="rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
            <Suspense fallback={<div className="text-sm text-ink-faint">Loading…</div>}>
              <TwoFactorLogin step={second.step} ticket={second.ticket} username={email.trim()} onSignedIn={continueAfterSignIn}
                onRestart={(why) => { setSecond(null); setError(why || null); }} />
            </Suspense>
          </div>
        )}
        {mode === "remote" && (
          <div role="status" className="rounded-2xl p-5 text-md leading-snug" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
            For safety, the first admin account can only be made from your home network. Open Arrmada at its address there
            (like <b>http://192.168.1.10:7878</b>), or add <code>ARRMADA_ALLOW_REMOTE_SETUP=1</code> to <code>.env</code> and
            run <code>docker compose up -d</code>.
          </div>
        )}
        {(mode === "login" || mode === "setup") && !second && (
          <form onSubmit={submit} className="flex flex-col gap-3 rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
            {signedOut && !setup && !error && (
              <div role="status" className="rounded-lg px-3 py-2.5 text-sm text-ink-dim" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
                You were signed out. Sign in again to carry on.
              </div>
            )}
            <Field label="Email">
              {/* The page is only this form, so the cursor starts where the first thing is typed. */}
              {/* eslint-disable-next-line jsx-a11y/no-autofocus */}
              <Input type="email" required autoFocus autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" className={BOX} />
            </Field>
            <Field label="Password">
              <Input type="password" required minLength={setup ? 8 : undefined} autoComplete={setup ? "new-password" : "current-password"} value={password} onChange={(e) => setPassword(e.target.value)} placeholder={setup ? "8+ characters" : "••••••••"} className={BOX} />
            </Field>
            {error && <div className="text-sm" style={{ color: "var(--reject-text)" }}>{error}</div>}
            <button type="submit" disabled={busy || plexBusy} className="mt-1 rounded-lg px-4 py-2.5 text-md font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
              {busy ? "…" : setup ? "Create admin & continue" : "Sign in"}
            </button>
            {setup && (
              <p className="m-0 mt-1 text-xs leading-snug text-ink-faint">
                Next, a short setup asks for your free <b>TMDB</b> key and where your media lives. You can skip it and do it later in Settings.
              </p>
            )}

            {!setup && (plexEnabled || jellyfin.enabled) && (
              <div className="my-0.5 flex items-center gap-2 text-xs uppercase tracking-wide text-ink-faint">
                <span className="h-px flex-1" style={{ background: "var(--line)" }} /> or <span className="h-px flex-1" style={{ background: "var(--line)" }} />
              </div>
            )}
            {/* Sign in with Plex: the server checks the Plex account can reach this server
                and signs it into its requester account. */}
            {!setup && plexEnabled && (
              <Suspense fallback={<button type="button" disabled className={SERVER_BUTTON} style={{ background: "var(--plex)", color: "var(--plex-ink)" }}><PlexMark />Sign in with Plex</button>}>
                <PlexLoginButton disabled={busy} onBusy={setPlexBusy} onError={setError} onSignedIn={afterServerSignIn} />
              </Suspense>
            )}
            {/* Sign in with Jellyfin: Jellyfin checks the username and password (or the
                Quick Connect code) and the server signs that user into their account. */}
            {!setup && jellyfin.enabled && (
              <button type="button" disabled={busy || plexBusy} onClick={() => { setError(null); setJellyfin((j) => ({ ...j, open: true })); }} className={SERVER_BUTTON} style={jellyfin.emby ? { background: "var(--emby)", color: "var(--emby-ink)" } : { background: "var(--jellyfin)", color: "var(--jellyfin-ink)" }}>
                {jellyfin.emby ? <><EmbyMark />Sign in with Emby</> : <><JellyfinMark />Sign in with Jellyfin</>}
              </button>
            )}
          </form>
        )}
        {jellyfin.open && (
          <Suspense fallback={null}>
            <JellyfinLoginSheet quickConnect={jellyfin.quickConnect} onClose={() => setJellyfin((j) => ({ ...j, open: false }))} onSignedIn={afterServerSignIn} />
          </Suspense>
        )}
      </div>
    </div>
  );
}

const BOX = "w-full px-3 py-2.5 text-md";
