import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Section, Toggle, inputStyle } from "../../components/settings/ui";
import { api, type AuthUser, type PlexBlock, type UserImpact } from "../../lib/api";
import { LINKS } from "../../lib/links";
import { useMe } from "../../lib/me";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";

// Settings → Users (admin only): the accounts, and who may sign in with Plex.
export function UsersSettings() {
  const { user, booksEnabled } = useMe();
  const { s, patch } = useLoadedSettings();
  return (
    <div className="flex flex-col gap-6">
      <UsersManager meId={user?.id} />
      <Section id="plex-sign-in" title="Plex sign-in" subtitle={<>Let your Plex Home members and shared users sign in with Plex — no accounts to hand out. They get a Requester account ({requesterPages(booksEnabled)}), and only people with access to your Plex server get in. Needs your Plex server connected in <Link to={LINKS.plexConnection} style={{ color: "var(--accent)" }}>Insights → Settings</Link>.</>}>
        <Toggle label="Allow Sign in with Plex" hint="Adds a 'Sign in with Plex' button to the login page." checked={s.plex_login_enabled} onChange={(v) => patch({ plex_login_enabled: v })} />
        <Toggle label="Auto-approve their requests" hint="Plex sign-ins' requests download immediately instead of waiting for your approval." checked={s.plex_login_auto_approve} onChange={(v) => patch({ plex_login_auto_approve: v })} />
      </Section>
      <SaveBar />
    </div>
  );
}

const ROLE_TONE: Record<string, string> = { admin: "var(--reject)", manager: "var(--accent)", requester: "var(--good)", readonly: "var(--ink-faint)" };

function UsersManager({ meId }: { meId?: number }) {
  const [users, setUsers] = useState<AuthUser[] | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("requester");
  const [autoApprove, setAutoApprove] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<AuthUser | null>(null);
  const [blocks, setBlocks] = useState<PlexBlock[]>([]);
  const [blockErr, setBlockErr] = useState<string | null>(null);

  const load = () => {
    api.users().then(setUsers).catch((e: Error) => setErr(e.message));
    api.plexBlocks().then(setBlocks).catch(() => {});
  };
  useEffect(() => { load(); }, []);

  const unblock = async (b: PlexBlock) => {
    setBlockErr(null);
    try { setBlocks(await api.unblockPlex(b.plex_id)); load(); }
    catch (e) { setBlockErr((e as Error).message); }
  };

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      await api.createUser({ email: email.trim(), password, role, auto_approve: autoApprove });
      setEmail(""); setPassword(""); setRole("requester"); setAutoApprove(false);
      load();
    } catch (e) { setErr((e as Error).message); }
    finally { setBusy(false); }
  };

  // The X only opens the dialog; nothing is deleted until it's confirmed there.
  const [removing, setRemoving] = useState<AuthUser | null>(null);

  return (
    <Section id="users" title="Users" subtitle="Add people who can use Arrmada. Auto-approve lets a user's requests download without waiting for you.">
      <RoleLegend />
      <div className="flex flex-col gap-1.5">
        {users === null ? (
          <p className="text-[12px] text-ink-dim">Loading…</p>
        ) : users.length === 0 ? (
          <p className="text-[12px] text-ink-dim">No users yet.</p>
        ) : (
          users.map((u) => (
            <div key={u.id} className="flex items-center gap-3 rounded-lg px-3 py-2" style={{ background: "var(--panel-2)" }}>
              <span className="grid h-7 w-7 flex-none place-items-center rounded-full text-[11px] font-bold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{u.username[0]?.toUpperCase()}</span>
              <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium" style={u.disabled ? { color: "var(--ink-faint)" } : undefined}>{u.username}</span>
              {u.disabled && <span className="rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: "var(--reject-soft)", color: "var(--reject)" }} title="Can't sign in. Nothing of theirs was deleted.">Disabled</span>}
              {u.auto_approve &&<span className="rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: "var(--good-soft, rgba(90,140,90,.16))", color: "var(--good)" }}>Auto-approve</span>}
              <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel)", color: ROLE_TONE[u.role] ?? "var(--ink-faint)", border: "1px solid var(--line)" }}>{u.role}</span>
              <button onClick={() => setEditing(u)} title="Edit user" className="grid h-7 w-7 flex-none place-items-center rounded-lg" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M4 20h4L18 10l-4-4L4 16v4z M14 6l4 4" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
              </button>
              {u.id !== meId && (
                <button onClick={() => setRemoving(u)} title="Remove user" className="grid h-7 w-7 flex-none place-items-center rounded-lg" style={{ border: "1px solid var(--line)", color: "var(--ink-faint)" }}>
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" /></svg>
                </button>
              )}
            </div>
          ))
        )}
      </div>

      {blocks.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <div className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Blocked Plex accounts</div>
          {blocks.map((b) => (
            <div key={b.plex_id} className="flex items-center gap-3 rounded-lg px-3 py-2" style={{ background: "var(--panel-2)" }}>
              <span className="min-w-0 flex-1 truncate text-[12.5px]">{b.name || `Plex account ${b.plex_id}`}</span>
              <span className="flex-none text-[10.5px] text-ink-faint">can't sign in with Plex</span>
              <button onClick={() => unblock(b)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Unblock</button>
            </div>
          ))}
          {blockErr && <div className="text-[12px]" style={{ color: "var(--reject)" }}>{blockErr}</div>}
        </div>
      )}

      <form onSubmit={add} className="mt-2 flex flex-col gap-2.5 rounded-lg p-3" style={{ border: "1px dashed var(--line)" }}>
        <div className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Add a user</div>
        <div className="flex flex-wrap gap-2">
          <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="email@example.com" className="min-w-[180px] flex-1 rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle} />
          <input type="password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="password (8+ chars)" className="min-w-[160px] flex-1 rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle} />
          <select value={role} onChange={(e) => setRole(e.target.value)} className="rounded-lg px-2.5 py-2 text-[12.5px]" style={inputStyle}>
            <option value="requester">Requester</option>
            <option value="readonly">Read-only</option>
            <option value="manager">Manager</option>
            <option value="admin">Admin</option>
            <option value="readonly">Read-only</option>
          </select>
        </div>
        <div className="flex items-center justify-between gap-3">
          <label className="flex items-center gap-2 text-[12px] text-ink-dim">
            <input type="checkbox" checked={autoApprove} onChange={(e) => setAutoApprove(e.target.checked)} />
            Auto-approve this user's requests
          </label>
          <button type="submit" disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Adding…" : "Add user"}</button>
        </div>
        {err && <div className="text-[12px]" style={{ color: "var(--reject)" }}>{err}</div>}
      </form>

      {editing && <EditUserModal user={editing} isMe={editing.id === meId} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
      {removing && <DeleteUserDialog user={removing} onClose={() => setRemoving(null)} onDeleted={() => { setRemoving(null); load(); }} />}
    </Section>
  );
}

// requesterPages is what a non-staff account's top bar shows (see App.tsx's requester and
// outside route trees): Calendar only at home, Books only while the module is on.
function requesterPages(booksEnabled: boolean): string {
  return ["Discover", "Calendar (at home only)", ...(booksEnabled ? ["Books"] : []), "Audiobooks"].join(", ");
}

// RoleLegend says what each role can do, from today's gates: admin-only routes are the
// System and Users tabs, the audiobook server settings and the Overseerr/Tautulli imports.
function RoleLegend() {
  const { booksEnabled } = useMe();
  const pages = requesterPages(booksEnabled);
  const rows: [string, string][] = [
    ["Requester", `${pages}, and can request.`],
    ["Read-only", `the same pages, but can't request.`],
    ["Manager", "the whole console except Settings → System and Users, the audiobook server settings and the Overseerr/Tautulli imports."],
    ["Admin", "everything."],
  ];
  return (
    <div className="-mt-2 flex flex-col gap-0.5 text-[11px] text-ink-faint">
      {rows.map(([role, what]) => <div key={role}><b className="text-ink-dim">{role}</b> — {what}</div>)}
    </div>
  );
}

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// DeleteUserDialog says what a delete would erase before it happens — as counts only, never
// which books (admins see how much, not what) — and asks for the username when it would
// erase someone's audiobook places. The server copies the database first either way.
function DeleteUserDialog({ user, onClose, onDeleted }: { user: AuthUser; onClose: () => void; onDeleted: () => void }) {
  const [impact, setImpact] = useState<UserImpact | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // Plex-linked users come straight back on their next Plex sign-in unless this is ticked.
  const [blockPlex, setBlockPlex] = useState(false);

  useEffect(() => {
    api.userImpact(user.id).then(setImpact).catch((e: Error) => setErr(`Couldn't check what this would remove: ${e.message}`));
  }, [user.id]);

  const listening = !!impact && (impact.places > 0 || impact.listening_hours > 0);
  const hours = impact ? (impact.listening_hours >= 10 ? Math.round(impact.listening_hours) : Math.round(impact.listening_hours * 10) / 10) : 0;
  const canBlock = !!user.plex_linked && !user.plex_blocked;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try { await api.deleteUser(user.id, listening ? user.username : undefined, canBlock && blockPlex); onDeleted(); }
    catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  return (
    <ConfirmDialog
      title={<>Delete {user.username}?</>}
      body={impact ? (
        <>
          <p className="m-0">
            This permanently removes their place in {plural(impact.places, "audiobook")}, {hours} hour{hours === 1 ? "" : "s"} of listening history, {plural(impact.bookmarks, "bookmark")} and {plural(impact.devices, "signed-in device")}.
            {" "}Their {plural(impact.requests, "request")} stay.
          </p>
          <p className="m-0 mt-2">A copy of the database from just before is kept under Backups.</p>
          {canBlock && !blockPlex && <p className="m-0 mt-2">They can sign in again with Plex unless you block their Plex account below, or turn off their sign-in instead of deleting them.</p>}
        </>
      ) : !err ? "Checking what this would remove…" : null}
      extra={canBlock ? (
        <label className="flex items-center gap-2 text-[12px] text-ink-dim">
          <input type="checkbox" checked={blockPlex} disabled={busy} onChange={(e) => setBlockPlex(e.target.checked)} />
          Also block their Plex account from signing in here
        </label>
      ) : undefined}
      typedPhrase={listening ? user.username : undefined}
      confirmLabel="Delete user"
      busyLabel="Deleting…"
      busy={busy}
      error={err}
      confirmDisabled={!impact}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}

function EditUserModal({ user, isMe, onClose, onSaved }: { user: AuthUser; isMe: boolean; onClose: () => void; onSaved: () => void }) {
  const [role, setRole] = useState(user.role);
  const [autoApprove, setAutoApprove] = useState(user.auto_approve);
  const [canSignIn, setCanSignIn] = useState(!user.disabled);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [confirmBlock, setConfirmBlock] = useState(false);

  const save = async () => {
    setBusy(true); setErr(null);
    try {
      const signIn = canSignIn === !user.disabled ? {} : { disabled: !canSignIn };
      await api.updateUser(user.id, { role, auto_approve: autoApprove, ...signIn, ...(password ? { password } : {}) });
      onSaved();
    } catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  const blockPlex = async () => {
    setBusy(true); setErr(null);
    try { await api.blockUserPlex(user.id); onSaved(); }
    catch (e) { setErr((e as Error).message); setBusy(false); setConfirmBlock(false); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-6" style={{ background: "rgba(0,0,0,.6)" }} onClick={onClose}>
      <div className="w-full max-w-[420px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <h2 className="m-0 text-[15px] font-bold">Edit user</h2>
        <p className="mt-0.5 mb-4 truncate text-[12px] text-ink-dim">{user.username}</p>

        <label className="mb-3 flex flex-col gap-1.5">
          <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Role</span>
          <select value={role} onChange={(e) => setRole(e.target.value as AuthUser["role"])} className="rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle}>
            <option value="requester">Requester</option>
            <option value="manager">Manager</option>
            <option value="admin">Admin</option>
            <option value="readonly">Read-only</option>
          </select>
        </label>

        <div className="mb-3">
          <Toggle label="Auto-approve requests" hint="This user's requests download immediately, skipping the approval queue." checked={autoApprove} onChange={setAutoApprove} />
        </div>

        {!isMe && (
          <div className="mb-3">
            <Toggle
              label="Can sign in"
              hint={canSignIn
                ? "Turn off to sign them out of the web app and their audiobook apps. Nothing of theirs is deleted, so turning it back on restores everything."
                : "They're signed out everywhere and can't sign in. Their requests and listening places are kept."}
              checked={canSignIn}
              onChange={setCanSignIn}
            />
          </div>
        )}

        {user.plex_linked && !isMe && (
          <div className="mb-3 flex items-center justify-between gap-3 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            <span className="text-[11.5px] text-ink-dim">
              {user.plex_blocked
                ? "Their Plex account is blocked from signing in here. Unblock it under Blocked Plex accounts."
                : "Signs in with Plex. Blocking their Plex account stops them signing in with it (and stops a new account being made if you delete this one)."}
            </span>
            {!user.plex_blocked && (
              <button onClick={() => setConfirmBlock(true)} disabled={busy} className="flex-none rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Block their Plex account</button>
            )}
          </div>
        )}

        <label className="mb-4 flex flex-col gap-1.5">
          <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">New password <span className="text-ink-faint">(optional)</span></span>
          <input type="password" minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="leave blank to keep current" className="rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle} />
        </label>

        {err && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{err}</div>}
        <div className="flex justify-end gap-2.5">
          <button onClick={onClose} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={save} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save changes"}</button>
        </div>
        {confirmBlock && (
          <ConfirmDialog
            title={<>Block {user.username}'s Plex account?</>}
            body={<p className="m-0">They won't be able to sign in here with Plex. Devices they're already signed in on stay signed in — turn off Can sign in to sign them out too.</p>}
            confirmLabel="Block"
            busyLabel="Blocking…"
            busy={busy}
            onConfirm={blockPlex}
            onCancel={() => setConfirmBlock(false)}
          />
        )}
      </div>
    </div>
  );
}
