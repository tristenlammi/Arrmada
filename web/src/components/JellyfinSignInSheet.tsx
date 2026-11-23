import { useState, type FormEvent } from "react";
import { ApiError, type LoginAnswer } from "../lib/api";
import { jellyfinSignIn, type QuickConnectStart } from "../lib/jellyfinSignIn";
import { usePoll } from "../lib/usePoll";
import { Field } from "../ui/Field";
import { Input } from "../ui/Input";
import { Modal, type ModalProps } from "../ui/Modal";
import { Sheet } from "../ui/Sheet";
import { ServerTile } from "./brand/ServerBadge";
import { SERVER_FILL, SERVER_INK, SERVER_LABEL } from "./brand/servers";

// The Jellyfin sign-in sheet: a Jellyfin username and password, or (when the owner left it
// on) Quick Connect — a code to type into a Jellyfin app the person is already signed in
// to. The same sheet links Jellyfin to the account you're signed in as (JellyfinAccountLink).
// Its own chunk: only someone who taps a Jellyfin button downloads it.
//
// The password goes to Arrmada once, which hands it to Jellyfin; nothing here keeps it
// after the sheet closes. Quick Connect's secret never reaches the browser: the server
// gives an id to poll and the code to show.

export interface JellyfinSheetProps<T> {
  title: string;
  /** The password form's button ("Sign in", "Link"). */
  submitLabel: string;
  quickConnect: boolean;
  password: (username: string, password: string) => Promise<T | null>;
  qcStart: () => Promise<QuickConnectStart>;
  /** null while the code waits for approval. */
  qcPoll: (id: string) => Promise<T | null>;
  onDone: (result: T) => void;
  onClose: () => void;
  /** The login page sits outside the app's router, where Back can't be taken over: there
   *  the sheet is a plain bottom sheet. */
  outsideRouter?: boolean;
}

// PlainSheet is ui/Sheet without Back-to-close (which needs the router).
function PlainSheet({ children, ...rest }: Omit<ModalProps, "variant">) {
  return (
    <Modal {...rest} variant="sheet">
      <div aria-hidden className="mx-auto mt-2 h-1 w-10 rounded-full sm:hidden" style={{ background: "var(--line)" }} />
      {children}
    </Modal>
  );
}

const POLL_MS = 2000;

export function JellyfinSheet<T>({ title, submitLabel, quickConnect, password: signIn, qcStart, qcPoll, onDone, onClose, outsideRouter }: JellyfinSheetProps<T>) {
  const Shell = outsideRouter ? PlainSheet : Sheet;
  // "Jellyfin", or "Emby" when the Jellyfin slot is connected to Emby.
  const name = SERVER_LABEL.jellyfin;
  const [view, setView] = useState<"password" | "quick">("password");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [qc, setQC] = useState<QuickConnectStart | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const r = await signIn(username.trim(), password);
      setPassword("");
      if (r) onDone(r);
      else setBusy(false);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  const startQuick = async () => {
    setView("quick"); setError(null); setQC(null); setBusy(true);
    try { setQC(await qcStart()); }
    catch (err) { setError((err as Error).message); }
    finally { setBusy(false); }
  };

  // Ask every two seconds whether the code was approved; one answer signs in.
  usePoll(async () => {
    if (!qc) return;
    try {
      const r = await qcPoll(qc.id);
      if (r) { setQC(null); onDone(r); }
    } catch (err) {
      setQC(null);
      // 410: the code expired or was used; 403: started in another tab. Either way, a new code.
      setError(err instanceof ApiError && (err.status === 410 || err.status === 403) ? "That code has expired. Get a new one." : (err as Error).message);
    }
  }, view === "quick" && qc ? POLL_MS : null, { immediate: false });

  const heading = (
    <div className="flex items-center gap-3">
      <ServerTile server="jellyfin" size={36} />
      <h2 className="m-0 text-xl font-bold leading-tight">{title}</h2>
    </div>
  );

  return (
    <Shell onClose={onClose} ariaLabel={title} size="sm" dismissible={!busy || view === "quick"}>
      <div className="flex flex-col gap-4 p-5 sm:p-6">
        {heading}
        {view === "password" ? (
          <form onSubmit={submit} className="flex flex-col gap-3">
            <Field label={`${name} username`}>
              {/* The sheet opens for this one thing, so typing starts here. */}
              {/* eslint-disable-next-line jsx-a11y/no-autofocus */}
              <Input required autoFocus autoComplete="username" autoCapitalize="none" spellCheck={false} value={username} onChange={(e) => setUsername(e.target.value)} className={BOX} />
            </Field>
            <Field label="Password">
              <Input type="password" required autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} className={BOX} />
            </Field>
            {error && <div role="alert" className="text-sm" style={{ color: "var(--reject-text)" }}>{error}</div>}
            <button type="submit" disabled={busy} className="mt-1 rounded-lg px-4 py-2.5 text-md font-semibold" style={{ background: SERVER_FILL.jellyfin, color: SERVER_INK.jellyfin }}>
              {busy ? `Checking with ${name}…` : submitLabel}
            </button>
            {quickConnect && (
              <button type="button" onClick={startQuick} disabled={busy} className="rounded-lg px-4 py-2.5 text-md font-semibold" style={{ background: "var(--panel-2)", color: "var(--ink)", border: "1px solid var(--line)" }}>
                Use Quick Connect
              </button>
            )}
          </form>
        ) : (
          <div className="flex flex-col items-center gap-3 text-center">
            {qc ? (
              <>
                <div aria-live="polite" className="font-mono text-display font-bold tracking-[0.25em]" style={{ color: "var(--ink)" }}>{qc.code}</div>
                <p className="m-0 text-base text-ink-dim">Enter this code in a {name} app under Quick Connect.</p>
                <p className="m-0 text-sm text-ink-faint">Waiting for {name}…</p>
              </>
            ) : busy ? (
              <p className="m-0 text-base text-ink-dim">Getting a code…</p>
            ) : null}
            {error && (
              <>
                <div role="alert" className="text-sm" style={{ color: "var(--reject-text)" }}>{error}</div>
                <button type="button" onClick={startQuick} className="text-md font-semibold" style={{ color: "var(--accent-text)" }}>Get a new code</button>
              </>
            )}
            <button type="button" onClick={() => { setView("password"); setQC(null); setError(null); }} className="text-sm text-ink-faint underline">
              Use a password instead
            </button>
          </div>
        )}
      </div>
    </Shell>
  );
}

const BOX = "w-full px-3 py-2.5 text-md";

// The login page's sheet (Login.tsx loads this module when the button is tapped).
export default function JellyfinLoginSheet({ quickConnect, onClose, onSignedIn }: {
  quickConnect: boolean;
  onClose: () => void;
  /** Who signed in, or the two-factor step (a staff account that has it on). */
  onSignedIn: (answer: LoginAnswer) => void;
}) {
  return (
    <JellyfinSheet<LoginAnswer>
      title={`Sign in with ${SERVER_LABEL.jellyfin}`}
      submitLabel="Sign in"
      quickConnect={quickConnect}
      password={jellyfinSignIn.login}
      qcStart={jellyfinSignIn.loginQCStart}
      qcPoll={jellyfinSignIn.loginQCPoll}
      onDone={onSignedIn}
      onClose={onClose}
      outsideRouter
    />
  );
}
