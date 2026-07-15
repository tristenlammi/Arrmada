import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type AppSettings, type AuthUser } from "../lib/api";
import { useMe, isAdmin } from "../lib/me";

// Sample release used for the live naming preview.
const SAMPLE = {
  title: "Blade Runner 2049",
  year: "2017",
  quality: "2160p BluRay",
  resolution: "2160p",
  source: "BluRay",
  edition: "Director's Cut",
  codec: "x265",
  group: "FraMeSToR",
};

const TOKENS = ["title", "year", "quality", "resolution", "source", "edition", "codec", "group"];

function render(format: string): string {
  let out = format;
  for (const [k, v] of Object.entries(SAMPLE)) out = out.split(`{${k}}`).join(v);
  out = out.replace(/\s+/g, " ").replace(/[\s-]+$/g, "").trim();
  return out.replace(/[<>:"/\\|?*]/g, "");
}

type Tab = "media" | "library" | "system" | "users";

export function Settings() {
  const { user, setBooksEnabled, setMusicEnabled } = useMe();
  const admin = isAdmin(user);
  const [tab, setTab] = useState<Tab>("media");
  const [s, setS] = useState<AppSettings | null>(null);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.settings().then(setS).catch((e: Error) => setError(e.message));
  }, []);

  const patch = (p: Partial<AppSettings>) => setS((x) => (x ? { ...x, ...p } : x));

  const save = async () => {
    if (!s) return;
    setError(null);
    try {
      const next = await api.updateSettings(s);
      setS(next);
      setBooksEnabled(next.books_enabled); // reflect module on/off in nav + Discover live
      setMusicEnabled(next.music_enabled);
      setSaved(true);
      window.setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const tabs: { key: Tab; label: string }[] = [
    { key: "media", label: "Media" },
    { key: "library", label: "Library" },
    ...(admin ? [{ key: "system" as Tab, label: "System" }, { key: "users" as Tab, label: "Users" }] : []),
  ];

  const SaveBar = () => (
    <div className="flex items-center gap-3">
      <button onClick={save} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Save settings</button>
      {saved && <span className="text-[12px]" style={{ color: "var(--good)" }}>Saved ✓</span>}
    </div>
  );

  return (
    <>
      <PageHeader title="Settings" crumb="System / Settings" />
      <div className="mx-auto w-full max-w-[820px] px-4 py-6 sm:px-6">
        {/* Tabs */}
        <div className="mb-6 flex gap-1 border-b" style={{ borderColor: "var(--line)" }}>
          {tabs.map((t) => {
            const active = tab === t.key;
            return (
              <button key={t.key} onClick={() => setTab(t.key)} className="relative px-4 py-2.5 text-[13.5px] font-semibold transition-colors" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                {t.label}
                {active && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
              </button>
            );
          })}
        </div>

        {error && <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}
        {!s ? (
          <p className="text-[12.5px] text-ink-dim">Loading…</p>
        ) : tab === "media" ? (
          <div className="flex flex-col gap-6">
            <Section title="Media management" subtitle="How imported movie files are named. Tokens are replaced per release.">
              <Field label="Folder name">
                <input value={s.naming_movie_folder} onChange={(e) => patch({ naming_movie_folder: e.target.value })} className={input} style={inputStyle} />
                <Preview>{render(s.naming_movie_folder)}</Preview>
              </Field>
              <Field label="File name">
                <input value={s.naming_movie_file} onChange={(e) => patch({ naming_movie_file: e.target.value })} className={input} style={inputStyle} />
                <Preview>{render(s.naming_movie_file)}.mkv</Preview>
              </Field>
              <div className="flex flex-wrap gap-1.5">
                {TOKENS.map((t) => (
                  <code key={t} className="rounded px-1.5 py-0.5 font-mono text-[10.5px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{`{${t}}`}</code>
                ))}
              </div>
            </Section>
            <Section title="Metadata" subtitle="Written into each movie folder for Plex, Jellyfin, Emby and Kodi.">
              <Toggle label="Write movie.nfo" hint="A metadata sidecar with title, plot, ids, ratings." checked={s.write_nfo} onChange={(v) => patch({ write_nfo: v })} />
              <Toggle label="Download artwork" hint="Save poster.jpg and fanart.jpg next to the movie." checked={s.download_artwork} onChange={(v) => patch({ download_artwork: v })} />
            </Section>
            <SaveBar />
          </div>
        ) : tab === "library" ? (
          <div className="flex flex-col gap-6">
            <Section title="Library" subtitle="Defaults when adding movies and series.">
              <Toggle label="Search on add" hint="Start searching for a release as soon as a title is added." checked={s.search_on_add} onChange={(v) => patch({ search_on_add: v })} />
            </Section>
            <SaveBar />
          </div>
        ) : tab === "system" ? (
          admin && (
            <div className="flex flex-col gap-6">
              <Section title="Modules" subtitle="Turn modules on or off. Disabling hides a module from the navigation and from Discover — nothing is deleted, and it can be re-enabled anytime.">
                <Toggle label="Books" hint="Open Library metadata, ebook & audiobook library, and the Books tab in Discover." checked={s.books_enabled} onChange={(v) => patch({ books_enabled: v })} />
                <Toggle label="Music" hint="The Music library and its nav entry. (The Music module itself is still on the roadmap.)" checked={s.music_enabled} onChange={(v) => patch({ music_enabled: v })} />
              </Section>
              <SaveBar />
            </div>
          )
        ) : (
          admin && <UsersManager meId={user?.id} />
        )}
      </div>
    </>
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

  const load = () => api.users().then(setUsers).catch((e: Error) => setErr(e.message));
  useEffect(() => { load(); }, []);

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

  const remove = async (id: number) => {
    setErr(null);
    try { await api.deleteUser(id); load(); }
    catch (e) { setErr((e as Error).message); }
  };

  return (
    <Section title="Users" subtitle="Add people who can request media. Requesters see only the Discover page. Auto-approve lets a user's requests skip the queue and download immediately.">
      <div className="flex flex-col gap-1.5">
        {users === null ? (
          <p className="text-[12px] text-ink-dim">Loading…</p>
        ) : users.length === 0 ? (
          <p className="text-[12px] text-ink-dim">No users yet.</p>
        ) : (
          users.map((u) => (
            <div key={u.id} className="flex items-center gap-3 rounded-lg px-3 py-2" style={{ background: "var(--panel-2)" }}>
              <span className="grid h-7 w-7 flex-none place-items-center rounded-full text-[11px] font-bold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{u.username[0]?.toUpperCase()}</span>
              <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium">{u.username}</span>
              {u.auto_approve && <span className="rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: "var(--good-soft, rgba(90,140,90,.16))", color: "var(--good)" }}>Auto-approve</span>}
              <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel)", color: ROLE_TONE[u.role] ?? "var(--ink-faint)", border: "1px solid var(--line)" }}>{u.role}</span>
              <button onClick={() => setEditing(u)} title="Edit user" className="grid h-7 w-7 flex-none place-items-center rounded-lg" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M4 20h4L18 10l-4-4L4 16v4z M14 6l4 4" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
              </button>
              {u.id !== meId && (
                <button onClick={() => remove(u.id)} title="Remove user" className="grid h-7 w-7 flex-none place-items-center rounded-lg" style={{ border: "1px solid var(--line)", color: "var(--ink-faint)" }}>
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" /></svg>
                </button>
              )}
            </div>
          ))
        )}
      </div>

      <form onSubmit={add} className="mt-2 flex flex-col gap-2.5 rounded-lg p-3" style={{ border: "1px dashed var(--line)" }}>
        <div className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Add a user</div>
        <div className="flex flex-wrap gap-2">
          <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="email@example.com" className="min-w-[180px] flex-1 rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle} />
          <input type="password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="password (8+ chars)" className="min-w-[160px] flex-1 rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle} />
          <select value={role} onChange={(e) => setRole(e.target.value)} className="rounded-lg px-2.5 py-2 text-[12.5px]" style={inputStyle}>
            <option value="requester">Requester</option>
            <option value="manager">Manager</option>
            <option value="admin">Admin</option>
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

      {editing && <EditUserModal user={editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </Section>
  );
}

function EditUserModal({ user, onClose, onSaved }: { user: AuthUser; onClose: () => void; onSaved: () => void }) {
  const [role, setRole] = useState(user.role);
  const [autoApprove, setAutoApprove] = useState(user.auto_approve);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const save = async () => {
    setBusy(true); setErr(null);
    try {
      await api.updateUser(user.id, { role, auto_approve: autoApprove, ...(password ? { password } : {}) });
      onSaved();
    } catch (e) { setErr((e as Error).message); setBusy(false); }
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

        <label className="mb-4 flex flex-col gap-1.5">
          <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">New password <span className="text-ink-faint">(optional)</span></span>
          <input type="password" minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="leave blank to keep current" className="rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle} />
        </label>

        {err && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{err}</div>}
        <div className="flex justify-end gap-2.5">
          <button onClick={onClose} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={save} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save changes"}</button>
        </div>
      </div>
    </div>
  );
}

const input = "w-full rounded-lg px-3 py-2 font-mono text-[12.5px]";
const inputStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

function Section({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactNode }) {
  return (
    <div className="rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <h2 className="m-0 text-[14px] font-bold">{title}</h2>
      <p className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">{subtitle}</p>
      <div className="flex flex-col gap-4">{children}</div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{label}</span>
      {children}
    </label>
  );
}

function Preview({ children }: { children: React.ReactNode }) {
  return <span className="text-[11px] text-ink-dim">→ <span className="font-mono" style={{ color: "var(--accent)" }}>{children}</span></span>;
}

function Toggle({ label, hint, checked, onChange }: { label: string; hint: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="min-w-0">
        <div className="text-[12.5px] font-semibold">{label}</div>
        <div className="text-[10.5px] text-ink-faint">{hint}</div>
      </div>
      <button
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors"
        style={{ background: checked ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)" }}
      >
        <span className="inline-block h-4 w-4 rounded-full bg-white transition-transform" style={{ transform: checked ? "translateX(22px)" : "translateX(3px)" }} />
      </button>
    </div>
  );
}
