import { useEffect, useId, useState, type ReactNode } from "react";
import { api, type MyApprise } from "../lib/api";
import { accountApi, type NotifyPrefs } from "../lib/accountApi";
import { isStaff, useMe } from "../lib/me";
import { deviceName, disablePush, enablePush, pushStatus, type PushStatus } from "../lib/webpush";

// The Me page's "Get notified" card: push on this device first, then which notices to
// send, then (for the few who want it) a personal Apprise link tucked under Advanced. It
// loads with the Me page only, so lib/webpush never rides in the requester's first load.
export function NotificationSettings() {
  return (
    <>
      <PushSetting />
      <EventPrefs />
      <details className="group" style={{ borderTop: "1px solid var(--line-soft)" }}>
        <summary className="flex min-h-[44px] cursor-pointer list-none items-center justify-between gap-2 px-3.5 text-[12px] font-medium text-ink-dim [&::-webkit-details-marker]:hidden">
          Advanced: Discord, ntfy, email (Apprise)
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" aria-hidden className="transition-transform group-open:rotate-90" style={{ color: "var(--ink-faint)" }}><path d="M9 6l6 6-6 6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        </summary>
        <AppriseSetting />
      </details>
    </>
  );
}

// What to say when push can't be switched on here, and what to do instead.
const HELP: Partial<Record<PushStatus, ReactNode>> = {
  unavailable: "Push isn’t set up on this server yet — ask the admin. Your bell still gets every notice.",
  insecure: "Notifications need the secure address — open Arrmada from your https link, then turn them on there.",
  "needs-install": (
    <>
      On iPhone and iPad, notifications only work from the Home Screen app:
      <ol className="mt-1 list-decimal pl-5">
        <li>Tap the Share button in Safari.</li>
        <li>Choose <b>Add to Home Screen</b>.</li>
        <li>Open Arrmada from the new icon and come back here.</li>
      </ol>
    </>
  ),
  unsupported: "This browser can’t show notifications. Try Chrome, Edge, Firefox, or Safari from the Home Screen.",
  blocked: "Notifications are blocked for this site. Allow them in your browser’s site settings (on a phone: Settings → Notifications → Arrmada), then reload this page.",
};

// PushSetting is the per-device Web Push switch, with a plain answer when it can't be on:
// no push key on the server, an http address, an iPhone outside the Home Screen app, or
// notifications blocked.
function PushSetting() {
  const [status, setStatus] = useState<PushStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const labelId = useId();

  useEffect(() => {
    let alive = true;
    pushStatus().then((s) => { if (alive) setStatus(s); }).catch(() => { if (alive) setStatus("unsupported"); });
    return () => { alive = false; };
  }, []);

  const on = status === "on";
  const toggle = async () => {
    if (busy) return;
    setBusy(true); setError(null);
    try {
      if (on) {
        await disablePush();
        setStatus("off");
      } else {
        // The permission prompt must come straight from this tap (iOS insists): enablePush
        // asks for it before awaiting anything else.
        await enablePush();
        setStatus("on");
      }
    } catch (e) {
      setError((e as Error).message || "Couldn’t set up notifications on this device.");
      setStatus(await pushStatus().catch(() => status));
    } finally {
      setBusy(false);
    }
  };

  const toggleable = status === "on" || status === "off";
  const line = status === null ? "Checking this device…"
    : on ? `On for ${deviceName()}`
    : status === "off" ? `Off on ${deviceName()}`
    : "Not available here";
  const help = status ? HELP[status] : undefined;
  return (
    <div className="px-3.5 py-3">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div id={labelId} className="text-[13px] font-semibold">Notifications on this device</div>
          <div className="text-[11.5px] text-ink-dim">{line}</div>
        </div>
        {toggleable && (
          <button
            onClick={toggle}
            disabled={busy}
            role="switch"
            aria-checked={on}
            aria-labelledby={labelId}
            className="relative h-6 w-11 flex-none rounded-full transition-colors"
            style={{ background: on ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)", opacity: busy ? 0.6 : 1 }}
          >
            <span className="absolute top-1/2 h-4 w-4 -translate-y-1/2 rounded-full transition-[left]" style={{ left: on ? "calc(100% - 19px)" : "3px", background: on ? "var(--accent-ink)" : "var(--ink-faint)" }} />
          </button>
        )}
      </div>
      {toggleable && !on && <p className="m-0 mt-1.5 text-[11px] text-ink-faint">Turn it on to hear when something you asked for is ready, without opening Arrmada.</p>}
      {help && <div role="note" className="mt-2 rounded-lg px-3 py-2 text-[11.5px] leading-relaxed text-ink-dim" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>{help}</div>}
      {error && <div role="alert" className="mt-1.5 text-[11px] font-medium" style={{ color: "var(--reject)" }}>{error}</div>}
    </div>
  );
}

const EVENTS: { key: keyof NotifyPrefs; label: string; staff?: boolean }[] = [
  { key: "approved", label: "A request is approved" },
  { key: "declined", label: "A request is declined" },
  { key: "ready", label: "It’s ready to watch or read" },
  { key: "new_request", label: "Someone requests something", staff: true },
];

// EventPrefs is which notices reach your phones and Apprise link. The bell keeps every
// one either way; an unticked box only stops the buzz.
function EventPrefs() {
  const { user } = useMe();
  const staff = isStaff(user);
  const [prefs, setPrefs] = useState<NotifyPrefs | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    accountApi.notifyPrefs().then(setPrefs).catch((e) => setError((e as Error).message));
  }, []);

  const change = async (key: keyof NotifyPrefs, on: boolean) => {
    if (!prefs) return;
    const before = prefs;
    setPrefs({ ...prefs, [key]: on });
    setError(null);
    try {
      setPrefs(await accountApi.setNotifyPrefs({ [key]: on }));
    } catch (e) {
      setPrefs(before);
      setError(`Couldn’t save — ${(e as Error).message}`);
    }
  };

  return (
    <fieldset className="m-0 border-0 px-3.5 pb-3 pt-2.5" style={{ borderTop: "1px solid var(--line-soft)" }}>
      <legend className="float-left mb-1 w-full p-0 text-[12px] font-semibold">Notify me when</legend>
      {prefs === null && !error && <div className="clear-left text-[11.5px] text-ink-faint">Loading…</div>}
      {prefs && (
        <div className="clear-left flex flex-col">
          {EVENTS.filter((e) => !e.staff || staff).map((e) => (
            <label key={e.key} className="flex min-h-[40px] cursor-pointer items-center gap-2.5 text-[12.5px]">
              <input type="checkbox" checked={prefs[e.key]} onChange={(ev) => change(e.key, ev.target.checked)} className="h-4 w-4 flex-none accent-[var(--accent)]" />
              {e.label}
            </label>
          ))}
        </div>
      )}
      <p className="clear-left m-0 mt-1 text-[11px] text-ink-faint">
        These choose what reaches your phones and Apprise link; the bell keeps every notice either way.
        {staff && " Alert connections in Settings → Alerts keep their own event lists."}
      </p>
      {error && <div role="alert" className="mt-1.5 text-[11px] font-medium" style={{ color: "var(--reject)" }}>{error}</div>}
    </fieldset>
  );
}

// AppriseSetting is the personal Apprise link. Once saved it isn't shown again (it usually
// holds a token): the field's placeholder hints at it, typing replaces it, Remove clears it.
function AppriseSetting() {
  const [url, setUrl] = useState("");
  const [status, setStatus] = useState<MyApprise | null>(null);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.myApprise().then(setStatus).catch(() => {}); }, []);
  const write = async (next: string) => {
    setError(null);
    try {
      setStatus(await api.setMyApprise(next));
      setUrl("");
      setSaved(true);
      window.setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const set = !!status?.set;
  return (
    <div className="px-3.5 pb-3">
      <h3 className="m-0 text-[12px] font-semibold">Apprise link</h3>
      <div className="mb-1.5 text-[11px] text-ink-faint">Also send your notices to Discord, ntfy, email or anything else Apprise reaches. Most people don’t need this.{set ? " A link is saved — paste a new one to replace it." : ""}</div>
      <div className="flex gap-1.5">
        <input type="password" autoComplete="off" aria-label="Your Apprise URL" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={set ? `${status?.hint || "saved"} (saved)` : "ntfy://topic or discord://id/token"} className="min-w-0 flex-1 rounded-lg px-2 py-1.5 font-mono text-[11px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <button onClick={() => write(url.trim())} disabled={!url.trim()} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold disabled:opacity-50" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{saved ? "✓" : "Save"}</button>
        {set && <button onClick={() => write("")} className="rounded-lg px-2 py-1 text-[11px] text-ink-faint">Remove</button>}
      </div>
      {status?.blocked_reason && <div className="mt-1.5 text-[10.5px] font-medium" style={{ color: "var(--avoid)" }}>Not sending to this link: {status.blocked_reason}</div>}
      {error && <div className="mt-1.5 text-[10.5px] font-medium" style={{ color: "var(--reject)" }}>Couldn’t save — {error}</div>}
    </div>
  );
}
