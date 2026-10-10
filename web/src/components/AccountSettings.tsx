import { useEffect, useState, type FormEvent } from "react";
import { Button, useConfirm, useToast } from "../ui";
import { accountApi } from "../lib/accountApi";
import { useMe } from "../lib/me";
import { invalidate } from "../lib/query";

// The Me page's Account card, for every role: your password and your other devices. It is
// its own chunk, loaded with the Me page.
export function AccountSettings() {
  return (
    <>
      <PasswordSetting />
      <SignOutOthers />
    </>
  );
}

const field = "w-full rounded-lg px-2.5 py-2 text-[13px]";
const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" };

// PasswordSetting changes your password: the current one first, unless the account has
// none anyone knows (Plex sign-in), where this sets the first one — after which Plex can
// be unlinked without locking you out.
function PasswordSetting() {
  const { user } = useMe();
  const toast = useToast();
  const [hasPassword, setHasPassword] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    accountApi.account().then((a) => setHasPassword(a.password_set)).catch(() => setHasPassword(true));
  }, []);

  const reset = () => { setOpen(false); setCurrent(""); setNext(""); setConfirm(""); setError(null); };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (next.length < 8) { setError("The new password must be at least 8 characters."); return; }
    if (next !== confirm) { setError("The two new passwords don’t match."); return; }
    setBusy(true); setError(null);
    try {
      const r = await accountApi.changePassword(hasPassword ? current : "", next);
      const first = !hasPassword;
      setHasPassword(true);
      reset();
      // A first password means Plex can now be unlinked: the Plex card reads it again.
      if (first) invalidate("me:plex");
      toast(r.signed_out > 0 ? `Password ${first ? "set" : "changed"} — ${r.signed_out} other ${r.signed_out === 1 ? "device was" : "devices were"} signed out` : `Password ${first ? "set" : "changed"}`, { tone: "good" });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (hasPassword === null) return <div className="px-3.5 py-3 text-[12px] text-ink-faint">Loading…</div>;
  return (
    <div className="px-3.5 py-3">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-[13px] font-semibold">Password</div>
          <div className="text-[11.5px] text-ink-dim">
            {hasPassword ? "Changing it signs out your other devices." : "You sign in with Plex. Set a password to sign in with your name too — then you can unlink Plex if you like."}
          </div>
        </div>
        {!open && <Button size="sm" onClick={() => setOpen(true)}>{hasPassword ? "Change" : "Set a password"}</Button>}
      </div>
      {open && (
        <form onSubmit={submit} className="mt-3 flex flex-col gap-2" aria-label={hasPassword ? "Change password" : "Set a password"}>
          {/* The account name, hidden, so a password manager files the new password under it. */}
          <input type="text" autoComplete="username" value={user?.username ?? ""} readOnly hidden />
          {hasPassword && (
            <label className="flex flex-col gap-1 text-[11.5px] font-medium text-ink-dim">
              Current password
              <input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} className={field} style={fieldStyle} required />
            </label>
          )}
          <label className="flex flex-col gap-1 text-[11.5px] font-medium text-ink-dim">
            New password
            <input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} className={field} style={fieldStyle} minLength={8} required />
          </label>
          <label className="flex flex-col gap-1 text-[11.5px] font-medium text-ink-dim">
            New password again
            <input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className={field} style={fieldStyle} minLength={8} required />
          </label>
          {error && <div role="alert" className="text-[11.5px] font-medium" style={{ color: "var(--reject)" }}>{error}</div>}
          <div className="mt-1 flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={reset} disabled={busy}>Cancel</Button>
            <Button size="sm" variant="primary" type="submit" busy={busy} busyLabel="Saving…">{hasPassword ? "Change password" : "Set password"}</Button>
          </div>
        </form>
      )}
    </div>
  );
}

// SignOutOthers ends every other browser session; this one stays.
function SignOutOthers() {
  const confirm = useConfirm();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const run = async () => {
    const ok = await confirm({
      title: "Sign out your other devices?",
      body: "Every other phone, tablet and browser signed in as you will need to sign in again. This one stays signed in. Audiobook apps aren’t affected.",
      confirmLabel: "Sign out others",
      tone: "danger",
    });
    if (!ok) return;
    setBusy(true);
    try {
      const r = await accountApi.signOutOthers();
      toast(r.signed_out > 0 ? `Signed out of ${r.signed_out} other ${r.signed_out === 1 ? "device" : "devices"}` : "No other devices were signed in", { tone: "good" });
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex items-center justify-between gap-3 px-3.5 py-3" style={{ borderTop: "1px solid var(--line-soft)" }}>
      <div className="min-w-0">
        <div className="text-[13px] font-semibold">Other devices</div>
        <div className="text-[11.5px] text-ink-dim">Lost a phone, or signed in somewhere you shouldn’t stay?</div>
      </div>
      <Button size="sm" onClick={run} busy={busy} busyLabel="Signing out…">Sign out others</Button>
    </div>
  );
}
