import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Button, useConfirm, useToast } from "../ui";
import { accountApi, type MySession } from "../lib/accountApi";
import { formatAgo } from "../lib/format";
import { useMe } from "../lib/me";
import { invalidate } from "../lib/query";

// The Me page's Account card, for every role: your password and the devices you're signed
// in on. It is its own chunk, loaded with the Me page.
export function AccountSettings() {
  // A password change signs the other devices out: the list reads itself again after one.
  const [changes, setChanges] = useState(0);
  return (
    <>
      <PasswordSetting onChanged={() => setChanges((n) => n + 1)} />
      <Devices key={changes} />
    </>
  );
}

const field = "w-full rounded-lg px-2.5 py-2 text-[13px]";
const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" };

// PasswordSetting changes your password: the current one first, unless the account has
// none anyone knows (Plex sign-in), where this sets the first one — after which Plex can
// be unlinked without locking you out.
function PasswordSetting({ onChanged }: { onChanged: () => void }) {
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
      onChanged();
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

// Devices is where you're signed in: each browser with when it was last used and roughly
// where from (only you ever see this), "This device" marked. Any other one can be signed
// out on its own, or all of them at once; this one stays.
function Devices() {
  const confirm = useConfirm();
  const toast = useToast();
  const [list, setList] = useState<MySession[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // a session id, or "others"

  const load = useCallback(() => {
    accountApi.sessions().then((s) => { setList(s); setError(null); }).catch((e) => setError((e as Error).message));
  }, []);
  useEffect(load, [load]);

  const signOutOne = async (s: MySession) => {
    setBusy(s.id);
    try {
      await accountApi.signOutSession(s.id);
      setList((l) => l?.filter((x) => x.id !== s.id) ?? null);
      toast(`Signed out ${s.device}`, { tone: "good" });
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
      load();
    } finally {
      setBusy(null);
    }
  };

  const signOutOthers = async () => {
    const ok = await confirm({
      title: "Sign out your other devices?",
      body: "Every other phone, tablet and browser signed in as you will need to sign in again. This one stays signed in. Audiobook apps aren’t affected.",
      confirmLabel: "Sign out others",
      tone: "danger",
    });
    if (!ok) return;
    setBusy("others");
    try {
      const r = await accountApi.signOutOthers();
      toast(r.signed_out > 0 ? `Signed out of ${r.signed_out} other ${r.signed_out === 1 ? "device" : "devices"}` : "No other devices were signed in", { tone: "good" });
      load();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(null);
    }
  };

  const others = (list ?? []).filter((s) => !s.current).length;
  return (
    <div style={{ borderTop: "1px solid var(--line-soft)" }}>
      <div className="flex items-center justify-between gap-3 px-3.5 pt-3">
        <div className="min-w-0">
          <h3 className="m-0 text-[13px] font-semibold">Signed-in devices</h3>
          <div className="text-[11.5px] text-ink-dim">Don’t recognise one? Sign it out.</div>
        </div>
        {others > 0 && <Button size="sm" onClick={signOutOthers} busy={busy === "others"} busyLabel="Signing out…">Sign out others</Button>}
      </div>
      {error && <div role="alert" className="px-3.5 pt-2 text-[11.5px] font-medium" style={{ color: "var(--reject)" }}>Couldn’t load your devices — {error}</div>}
      {list === null && !error && <div className="px-3.5 py-3 text-[12px] text-ink-faint">Loading…</div>}
      {list && (
        <ul aria-label="Signed-in devices" className="m-0 mt-1.5 list-none p-0 pb-1.5">
          {list.map((s) => (
            <li key={s.id} className="flex min-h-[52px] items-center justify-between gap-3 px-3.5 py-1.5 [&+&]:border-t [&+&]:border-[var(--line-soft)]">
              <div className="min-w-0">
                <div className="flex items-center gap-2 text-[12.5px] font-medium">
                  <span className="truncate">{s.device}</span>
                  {s.current && <span className="flex-none rounded-full px-1.5 py-px text-[10px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>This device</span>}
                </div>
                <div className="truncate text-[11px] text-ink-faint">{s.current ? "Active now" : `Last seen ${seenAgo(s.last_seen_at)}`}{networkLabel(s.network)}</div>
              </div>
              {!s.current && (
                <Button size="sm" variant="ghost" onClick={() => signOutOne(s)} busy={busy === s.id} busyLabel="…" aria-label={`Sign out ${s.device}`}>Sign out</Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function seenAgo(iso: string): string {
  const ms = Date.parse(iso);
  return ms > 0 ? formatAgo(ms) : "a while ago";
}

function networkLabel(n: string): string {
  if (n === "local") return " · home network";
  return n ? ` · from ${n}` : "";
}
