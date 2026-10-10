import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { PlexMergeDialog } from "../../components/PlexMergeDialog";
import { Section, Toggle, inputStyle } from "../../components/settings/ui";
import { api, type AuthUser, type PlexBlock, type UserImpact } from "../../lib/api";
import { accountApi } from "../../lib/accountApi";
import { LINKS } from "../../lib/links";
import { useMe } from "../../lib/me";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";
import { managerExceptions, requesterPages } from "./roles";

// Settings → Users (admin only): the accounts and their request limits. Who may sign in
// with Plex lives in Settings → Plex.
export function UsersSettings() {
  const { user, booksEnabled } = useMe();
  const { s, patch } = useLoadedSettings();
  return (
    <div className="flex flex-col gap-6">
      <UsersManager meId={user?.id} />
      <p className="m-0 text-[11.5px] text-ink-faint">
        Who may sign in with Plex, and what new Plex sign-ins auto-approve, is in <Link to={LINKS.plexSignIn} style={{ color: "var(--accent)" }}>Settings → Plex</Link>.
      </p>
      <Section id="request-limits" title="Request limits" subtitle="Optional: how much one person can ask for in a stretch of days. Following someone else's request is free, a withdrawn or declined request gives its share back, and admins and managers are never limited. 0 means no limit.">
        <div className="flex flex-wrap items-end gap-3">
          <QuotaField label="Movies" value={s.request_quota_movies ?? 0} onChange={(v) => patch({ request_quota_movies: v })} />
          <QuotaField label="Seasons" value={s.request_quota_seasons ?? 0} onChange={(v) => patch({ request_quota_seasons: v })} />
          {booksEnabled && <QuotaField label="Books" value={s.request_quota_books ?? 0} onChange={(v) => patch({ request_quota_books: v })} />}
          <QuotaField label="Every … days" value={s.request_quota_days ?? 7} min={1} max={365} onChange={(v) => patch({ request_quota_days: v })} />
          <button
            type="button"
            onClick={() => patch({ request_quota_movies: 10, request_quota_seasons: 5, request_quota_books: 10, request_quota_days: 7 })}
            className="min-h-[36px] rounded-lg px-3 text-[12px] font-semibold"
            style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}
          >
            Typical household
          </button>
        </div>
        <p className="m-0 text-[11px] text-ink-faint">Typical household: 10 movies, 5 seasons and 10 books a week. A season request counts each season; asking for a whole show counts all of its seasons. Set a different limit for one person by editing them under Users.</p>
      </Section>
      <SaveBar />
    </div>
  );
}

// QuotaField is one number in the request limits card.
function QuotaField({ label, value, onChange, min = 0, max = 1000 }: { label: string; value: number; onChange: (v: number) => void; min?: number; max?: number }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{label}</span>
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        onChange={(e) => { const n = Math.round(Number(e.target.value)); if (Number.isFinite(n)) onChange(Math.min(max, Math.max(min, n))); }}
        className="w-[96px] rounded-lg px-3 py-2 text-[12.5px]"
        style={inputStyle}
      />
    </label>
  );
}

// Auto-approve is per media type: { movie, series, book }.
export type AutoTypes = { movie: boolean; series: boolean; book: boolean };
const NO_TYPES: AutoTypes = { movie: false, series: false, book: false };
const TYPE_LABEL: [keyof AutoTypes, string][] = [["movie", "Movies"], ["series", "Series"], ["book", "Books"]];
export const typesFromCSV = (csv: string): AutoTypes => {
  const on = csv.split(",").map((t) => t.trim());
  return { movie: on.includes("movie"), series: on.includes("series"), book: on.includes("book") };
};
export const typesToCSV = (t: AutoTypes) => TYPE_LABEL.filter(([k]) => t[k]).map(([k]) => k).join(",");
const typesOf = (u: AuthUser): AutoTypes => ({ movie: !!u.auto_approve_movie, series: !!u.auto_approve_series, book: !!u.auto_approve_book });
const typeFlags = (t: AutoTypes) => ({ auto_approve_movie: t.movie, auto_approve_series: t.series, auto_approve_book: t.book });

// AutoApproveChecks is "Auto-approve: ☐ Movies ☐ Series ☐ Books" (Books only while the
// module is on; a hidden type keeps its value).
export function AutoApproveChecks({ value, onChange, books, label }: { value: AutoTypes; onChange: (v: AutoTypes) => void; books: boolean; label: string }) {
  return (
    <div role="group" aria-label={label} className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[12px] text-ink-dim">
      {TYPE_LABEL.filter(([k]) => k !== "book" || books).map(([k, name]) => (
        <label key={k} className="flex min-h-[28px] items-center gap-1.5">
          <input type="checkbox" checked={value[k]} onChange={(e) => onChange({ ...value, [k]: e.target.checked })} />
          {name}
        </label>
      ))}
    </div>
  );
}

// A user's own limit as typed: "" follows the global limit (-1), otherwise a number
// (0 = unlimited).
const limitText = (n: number) => (n < 0 ? "" : String(n));
const limitValue = (s: string) => {
  const n = Math.round(Number(s));
  return s.trim() === "" || !Number.isFinite(n) || n < 0 ? -1 : Math.min(n, 1000);
};

// autoChip names the types a user auto-approves, for the list ("Auto-approve: Movies,
// Series"); "" when none.
function autoChip(t: AutoTypes): string {
  const on = TYPE_LABEL.filter(([k]) => t[k]).map(([, name]) => name);
  if (on.length === 0) return "";
  return on.length === 3 ? "Auto-approve" : `Auto-approve: ${on.join(", ")}`;
}

const ROLE_TONE: Record<string, string> ={ admin: "var(--reject)", manager: "var(--accent)", requester: "var(--good)", readonly: "var(--ink-faint)" };

function UsersManager({ meId }: { meId?: number }) {
  const [users, setUsers] = useState<AuthUser[] | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("requester");
  const [autoApprove, setAutoApprove] = useState<AutoTypes>(NO_TYPES);
  const { booksEnabled } = useMe();
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
      await api.createUser({ email: email.trim(), password, role, ...typeFlags(autoApprove) });
      setEmail(""); setPassword(""); setRole("requester"); setAutoApprove(NO_TYPES);
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
              {u.plex_linked && <span className="hidden max-w-[140px] truncate rounded-full px-2 py-0.5 text-[10.5px] sm:inline" style={{ background: "var(--panel)", color: "var(--ink-dim)", border: "1px solid var(--line)" }} title="Linked Plex account">Plex{u.plex_username ? ` · ${u.plex_username}` : ""}</span>}
              {autoChip(typesOf(u)) && <span className="rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: "var(--good-soft, rgba(90,140,90,.16))", color: "var(--good)" }}>{autoChip(typesOf(u))}</span>}
              {/* A Plex sign-in from before per-type auto-approve may still approve whole shows. */}
              {u.plex_linked && u.auto_approve_series && u.role === "requester" && (
                <span className="rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }} title="Their series requests are approved at once, every season asked for. Edit to tighten.">Approves whole shows</span>
              )}
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
          </select>
        </div>
        <div className="flex items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-ink-dim">
            <span>Auto-approve:</span>
            <AutoApproveChecks value={autoApprove} onChange={setAutoApprove} books={booksEnabled} label="Auto-approve this user's requests" />
          </div>
          <button type="submit" disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Adding…" : "Add user"}</button>
        </div>
        {err && <div className="text-[12px]" style={{ color: "var(--reject)" }}>{err}</div>}
      </form>

      {editing && <EditUserModal user={editing} users={users ?? []} isMe={editing.id === meId} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
      {removing && <DeleteUserDialog user={removing} onClose={() => setRemoving(null)} onDeleted={() => { setRemoving(null); load(); }} />}
    </Section>
  );
}

// RoleLegend says what each role can do, from today's gates (see roles.ts): the requester
// pages are their top bar's, and Read-only can't request (POST /requests needs Requester).
function RoleLegend() {
  const { booksEnabled } = useMe();
  const pages = requesterPages(booksEnabled);
  const rows: [string, string][] = [
    ["Requester", `${pages}, and can request.`],
    ["Read-only", `the same pages, but can't request.`],
    ["Manager", managerExceptions()],
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

function EditUserModal({ user, users, isMe, onClose, onSaved }: { user: AuthUser; users: AuthUser[]; isMe: boolean; onClose: () => void; onSaved: () => void }) {
  const [role, setRole] = useState(user.role);
  const [autoApprove, setAutoApprove] = useState<AutoTypes>(typesOf(user));
  const { booksEnabled } = useMe();
  // Their own request limits, as typed: blank follows the global limit, 0 is unlimited.
  const own = user.quota ?? { movies: -1, seasons: -1, books: -1 };
  const [quota, setQuota] = useState({ movies: limitText(own.movies), seasons: limitText(own.seasons), books: limitText(own.books) });
  const [canSignIn, setCanSignIn] = useState(!user.disabled);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [confirmBlock, setConfirmBlock] = useState(false);

  const save = async () => {
    setBusy(true); setErr(null);
    try {
      const signIn = canSignIn === !user.disabled ? {} : { disabled: !canSignIn };
      const limits = { movies: limitValue(quota.movies), seasons: limitValue(quota.seasons), books: limitValue(quota.books) };
      await api.updateUser(user.id, { role, ...typeFlags(autoApprove), quota: limits, ...signIn, ...(password ? { password } : {}) });
      onSaved();
    } catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  const [confirmUnlink, setConfirmUnlink] = useState(false);
  // Accounts this one could be merged into: any other account with no Plex link of its own.
  const mergeTargets = users.filter((u) => u.id !== user.id && !u.plex_linked);
  const [mergeInto, setMergeInto] = useState<number | null>(null);
  const unlinkPlex = async () => {
    setBusy(true); setErr(null);
    try { await api.updateUser(user.id, { plex_unlink: true }); onSaved(); }
    catch (e) { setErr((e as Error).message); setBusy(false); setConfirmUnlink(false); }
  };

  const [confirmSignOut, setConfirmSignOut] = useState(false);
  const [signedOut, setSignedOut] = useState<string | null>(null);
  const signOutEverywhere = async () => {
    setBusy(true); setErr(null);
    try {
      const r = await accountApi.signOutEverywhere(user.id);
      setSignedOut(r.signed_out > 0 ? `Signed out of ${r.signed_out} ${r.signed_out === 1 ? "browser" : "browsers"}.` : "They weren’t signed in anywhere.");
    } catch (e) { setErr((e as Error).message); }
    setBusy(false); setConfirmSignOut(false);
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
          <div className="mb-1 font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Auto-approve</div>
          <AutoApproveChecks value={autoApprove} onChange={setAutoApprove} books={booksEnabled} label="Auto-approve requests for" />
          <p className="m-0 mt-1 text-[11px] text-ink-faint">Ticked types download straight away, skipping the approval queue. A series request can pull many seasons.</p>
        </div>

        <div className="mb-3">
          <div className="mb-1 font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Their request limits</div>
          <div className="flex flex-wrap gap-2">
            {([["movies", "Movies"], ["seasons", "Seasons"], ...(booksEnabled ? [["books", "Books"]] : [])] as ["movies" | "seasons" | "books", string][]).map(([k, label]) => (
              <label key={k} className="flex flex-col gap-1 text-[11px] text-ink-dim">
                {label}
                <input
                  type="number"
                  min={0}
                  max={1000}
                  value={quota[k]}
                  placeholder="Default"
                  aria-label={`${label} limit`}
                  onChange={(e) => setQuota((q) => ({ ...q, [k]: e.target.value }))}
                  className="w-[88px] rounded-lg px-2.5 py-1.5 text-[12.5px]"
                  style={inputStyle}
                />
              </label>
            ))}
          </div>
          <p className="m-0 mt-1 text-[11px] text-ink-faint">Blank uses the limits under Request limits; 0 means no limit for them.</p>
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
                : `Linked to Plex${user.plex_username ? ` as ${user.plex_username}` : ""}. Blocking their Plex account stops them signing in with it (and stops a new account being made if you delete this one).`}
            </span>
            <div className="flex flex-none flex-col gap-1.5">
              {!user.plex_blocked && (
                <button onClick={() => setConfirmBlock(true)} disabled={busy} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Block their Plex account</button>
              )}
              <button onClick={() => setConfirmUnlink(true)} disabled={busy} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Unlink Plex</button>
            </div>
          </div>
        )}

        {/* A duplicate Plex requester (the owner or a family member signed in with Plex
            before their own account was linked) can be folded into that account. */}
        {user.plex_linked && !isMe && (user.role === "requester" || user.role === "readonly") && mergeTargets.length > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-lg p-3 text-[11.5px] text-ink-dim" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            <span className="min-w-0 flex-1">Same person as another account? Merge this one into it: requests, inbox, phones, audiobook progress and the Plex link move there.</span>
            <select aria-label="Merge into" value={mergeInto ?? ""} onChange={(e) => setMergeInto(e.target.value ? Number(e.target.value) : null)} className="rounded-lg px-2 py-1.5 text-[12px]" style={inputStyle}>
              <option value="">Merge into…</option>
              {mergeTargets.map((t) => <option key={t.id} value={t.id}>{t.username} ({t.role})</option>)}
            </select>
          </div>
        )}
        {mergeInto !== null && (
          <PlexMergeDialog targetId={mergeInto} fromId={user.id} onClose={() => setMergeInto(null)} onMerged={() => { setMergeInto(null); onSaved(); }} />
        )}

        {/* A lost phone or a shared computer: end every browser session without changing
            their password or turning their sign-in off. You never see their devices. */}
        {!isMe && (
          <div className="mb-3 flex items-center justify-between gap-3 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            <span className="text-[11.5px] text-ink-dim">{signedOut ?? "Signs them out of Arrmada in every browser. They can sign straight back in; audiobook apps aren’t affected."}</span>
            <button onClick={() => setConfirmSignOut(true)} disabled={busy} className="flex-none rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Sign out everywhere</button>
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
        {confirmUnlink && (
          <ConfirmDialog
            title={<>Unlink {user.username}'s Plex account{user.plex_username ? ` (${user.plex_username})` : ""}?</>}
            body={user.plex_only
              ? <p className="m-0">Plex is their only way in: nobody knows a password for this account. After unlinking, their next Sign in with Plex makes a new, empty account. Set a new password here first if they should keep using this one.</p>
              : <p className="m-0">Sign in with Plex won't open this account any more, and Discover stops using their Plex watch history. They can link it again from their account menu.</p>}
            confirmLabel="Unlink"
            busyLabel="Unlinking…"
            busy={busy}
            onConfirm={unlinkPlex}
            onCancel={() => setConfirmUnlink(false)}
          />
        )}
        {confirmSignOut && (
          <ConfirmDialog
            title={<>Sign {user.username} out everywhere?</>}
            body={<p className="m-0">Every browser and phone signed in as them will have to sign in again. Their password, requests and audiobook apps stay as they are.</p>}
            confirmLabel="Sign out everywhere"
            busyLabel="Signing out…"
            busy={busy}
            onConfirm={signOutEverywhere}
            onCancel={() => setConfirmSignOut(false)}
          />
        )}
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
