import { useEffect, useState } from "react";
import { api, type MyApprise } from "../lib/api";
import { pushSupported, subscribeThisDevice, thisDeviceEndpoint, unsubscribeThisDevice } from "../lib/webpush";

// The bell's settings panel (the ⚙ in its dropdown). It loads only when opened: most
// visits never open it, and lib/webpush would otherwise ride in every requester's first
// load.
export function NotificationSettings() {
  return (
    <>
      <PushSetting />
      <AppriseSetting />
    </>
  );
}

// PushSetting is the per-device Web Push toggle: real notifications on this
// phone/desktop when a request is ready — no extra app. iOS needs the PWA added
// to the Home Screen (16.4+); Android/desktop work in the browser directly.
function PushSetting() {
  const supported = pushSupported();
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [available, setAvailable] = useState(false);

  useEffect(() => {
    if (!supported) return;
    let alive = true;
    (async () => {
      try {
        const key = await api.pushKey();
        if (!alive || !key) return;
        setAvailable(true);
        const endpoint = await thisDeviceEndpoint();
        if (alive) setEnabled(!!endpoint);
      } catch { /* push stays hidden */ }
    })();
    return () => { alive = false; };
  }, [supported]);

  const toggle = async () => {
    if (busy) return;
    setBusy(true); setError(null);
    try {
      if (enabled) {
        await unsubscribeThisDevice();
        setEnabled(false);
      } else {
        // Permission must be requested from this user gesture (iOS requires it).
        await subscribeThisDevice();
        setEnabled(true);
      }
    } catch (e) {
      setError((e as Error).message || "Couldn't set up push on this device.");
    } finally {
      setBusy(false);
    }
  };

  if (!supported || !available) return null;
  return (
    <div className="px-3.5 py-2.5" style={{ borderBottom: "1px solid var(--line)" }}>
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="text-[12px] font-semibold">Push notifications</div>
          <div className="text-[10.5px] text-ink-faint">Real notifications on this device when a request is ready. On iPhone, add Arrmada to your Home Screen first.</div>
        </div>
        <button
          onClick={toggle}
          disabled={busy}
          role="switch"
          aria-checked={enabled}
          className="relative h-5 w-9 flex-none rounded-full transition-colors"
          style={{ background: enabled ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)", opacity: busy ? 0.6 : 1 }}
        >
          <span className="absolute top-1/2 h-3.5 w-3.5 -translate-y-1/2 rounded-full transition-[left]" style={{ left: enabled ? "calc(100% - 16px)" : "2px", background: enabled ? "var(--accent-ink)" : "var(--ink-faint)" }} />
        </button>
      </div>
      {error && <div className="mt-1 text-[10.5px]" style={{ color: "var(--reject)" }}>{error}</div>}
    </div>
  );
}

// AppriseSetting is the personal push link. Once saved it isn't shown again (it usually
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
    <div className="px-3.5 py-3" style={{ background: "var(--panel-2)", borderBottom: "1px solid var(--line)" }}>
      <div className="text-[11.5px] font-semibold">Push notifications (optional)</div>
      <div className="mb-1.5 text-[10.5px] text-ink-faint">Paste your own Apprise URL to also get pushed (Discord, ntfy, email…). Leave it unset for in-app only.{set ? " Currently set — paste a new one to replace it." : ""}</div>
      <div className="flex gap-1.5">
        <input type="password" autoComplete="off" aria-label="Your Apprise URL" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={set ? `${status?.hint || "saved"} (saved)` : "ntfy://topic or discord://id/token"} className="min-w-0 flex-1 rounded-lg px-2 py-1 font-mono text-[11px]" style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <button onClick={() => write(url.trim())} disabled={!url.trim()} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold disabled:opacity-50" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{saved ? "✓" : "Save"}</button>
        {set && <button onClick={() => write("")} className="rounded-lg px-2 py-1 text-[11px] text-ink-faint">Remove</button>}
      </div>
      {status?.blocked_reason && <div className="mt-1.5 text-[10.5px] font-medium" style={{ color: "var(--avoid)" }}>Not sending to this link: {status.blocked_reason}</div>}
      {error && <div className="mt-1.5 text-[10.5px] font-medium" style={{ color: "var(--reject)" }}>Couldn’t save — {error}</div>}
    </div>
  );
}
