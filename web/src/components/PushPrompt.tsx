import { useEffect, useState } from "react";
import { Button, Sheet } from "../ui";
import { markPromptShown, rememberPrompt } from "../lib/pushPrompt";
import { deviceName, enablePush, pushStatus, type PushStatus } from "../lib/webpush";

// PushPrompt asks "Get notified when it's ready?" once per device, right after a request.
// It only asks when the answer can be yes here: push is off on this device ("Turn on"), or
// this is an iPhone outside the Home Screen app (how to install, then "Got it"). Push
// already on, blocked, unavailable on the server or an http address: it says nothing.
export function PushPrompt({ onDone }: { onDone: () => void }) {
  const [status, setStatus] = useState<PushStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    pushStatus().then((s) => {
      if (!alive) return;
      if (s === "off" || s === "needs-install") {
        markPromptShown();
        setStatus(s);
      } else {
        onDone();
      }
    }).catch(() => { if (alive) onDone(); });
    return () => { alive = false; };
  }, [onDone]);

  if (!status) return null;
  const close = (answer: "dismissed" | "done") => { rememberPrompt(answer); onDone(); };

  // The permission prompt must come from this tap (iOS insists): enablePush asks for it
  // before it awaits anything else.
  const turnOn = async () => {
    setBusy(true); setError(null);
    try {
      await enablePush();
      close("done");
    } catch (e) {
      setError((e as Error).message || "Couldn’t turn on notifications.");
      setBusy(false);
    }
  };

  const install = status === "needs-install";
  return (
    <Sheet onClose={() => close("dismissed")} ariaLabel="Get notified when it’s ready?" size="sm" dismissible={!busy}>
      <div className="p-5">
        <h2 className="m-0 text-[16px] font-bold">Get notified when it’s ready?</h2>
        {install ? (
          <div className="mt-1.5 text-[12.5px] leading-relaxed text-ink-dim">
            On iPhone and iPad, notifications come through the Home Screen app:
            <ol className="m-0 mt-1 list-decimal pl-5">
              <li>Tap the Share button in Safari.</li>
              <li>Choose <b>Add to Home Screen</b>.</li>
              <li>Open Arrmada from the new icon and turn on notifications under Me.</li>
            </ol>
          </div>
        ) : (
          <p className="m-0 mt-1.5 text-[12.5px] text-ink-dim">A notification on {deviceName()} when what you asked for can be watched or read — no need to keep checking. You can change it any time under Me.</p>
        )}
        {error && <p role="alert" className="m-0 mt-2 text-[11.5px] font-medium" style={{ color: "var(--reject)" }}>{error}</p>}
        <div className="mt-4 flex justify-end gap-2.5">
          {install ? (
            <Button variant="primary" onClick={() => close("dismissed")}>Got it</Button>
          ) : (
            <>
              <Button onClick={() => close("dismissed")} disabled={busy}>Not now</Button>
              <Button variant="primary" onClick={turnOn} busy={busy} busyLabel="Turning on…">Turn on</Button>
            </>
          )}
        </div>
      </div>
    </Sheet>
  );
}
