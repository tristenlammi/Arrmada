import { useEffect, useRef, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { api, type APIKeyStatus, type AppSettings, type AuthUser, type DiskGuardStatus, type RecycleStats, type RecycleItem, type UserImpact } from "../lib/api";
import { useMe, isAdmin } from "../lib/me";
import { LibraryFolders } from "./Library";

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

// Sample series episode for the series naming preview.
const SERIES_SAMPLE = {
  title: "The Bear",
  year: "2022",
  season: "4",
  season00: "04",
  episode: "S04E01",
  episodetitle: "Tomorrow",
  quality: "1080p WEB-DL",
  resolution: "1080p",
  source: "WEB-DL",
  codec: "x264",
  group: "NTb",
};
const SERIES_TOKENS = ["title", "year", "season", "season00", "episode", "episodetitle", "quality", "resolution", "source", "codec", "group"];

// renderWith mirrors the backend renderName tidy-up: substitute tokens, drop empty
// bracket pairs and stranded "- -" separators, strip illegal filename chars.
function renderWith(format: string, sample: Record<string, string>): string {
  let out = format;
  for (const [k, v] of Object.entries(sample)) out = out.split(`{${k}}`).join(v);
  out = out.replace(/\(\)/g, "").replace(/\[\]/g, "").replace(/\s+/g, " ");
  while (out.includes("- -")) out = out.replace("- -", "-");
  out = out.replace(/^[\s-]+|[\s-]+$/g, "");
  return out.replace(/[<>:"/\\|?*]/g, "");
}
const render = (format: string) => renderWith(format, SAMPLE);
const renderSeries = (format: string) => renderWith(format, SERIES_SAMPLE);

type Tab = "media" | "library" | "system" | "users";

export function Settings() {
  const { user, setBooksEnabled, setMusicEnabled } = useMe();
  const admin = isAdmin(user);
  // ?tab=system opens straight onto a tab, so other pages (Convert's Problems) can link to a setting.
  const [picked, setTab] = useState<Tab>(() => {
    const t = new URLSearchParams(window.location.search).get("tab");
    return t === "library" || t === "system" || t === "users" ? t : "media";
  });
  // The admin-only tabs fall back to Media for everyone else, so a link to one never opens
  // onto an empty page.
  const tab: Tab = !admin && (picked === "system" || picked === "users") ? "media" : picked;
  const [s, setS] = useState<AppSettings | null>(null);
  // What the server last told us. Save sends only the keys that differ from it, so a
  // value saved elsewhere on the page (the Discovery region) or by another tab isn't
  // overwritten by a stale copy, and nothing read-only is ever echoed back.
  const [loaded, setLoaded] = useState<AppSettings | null>(null);
  const [flash, setFlash] = useState<{ text: string; good: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.settings().then((x) => { setS(x); setLoaded(x); }).catch((e: Error) => setError(e.message));
  }, []);

  const patch = (p: Partial<AppSettings>) => setS((x) => (x ? { ...x, ...p } : x));

  // Only the newest message's timer may clear it; otherwise an earlier save's timer
  // wipes "Saved ✓" a moment after it appears.
  const flashTimer = useRef<number | undefined>(undefined);
  const showFlash = (text: string, good: boolean) => {
    setFlash({ text, good });
    window.clearTimeout(flashTimer.current);
    flashTimer.current = window.setTimeout(() => setFlash(null), 2000);
  };
  useEffect(() => () => window.clearTimeout(flashTimer.current), []);

  const save = async () => {
    if (!s || !loaded) return;
    setError(null);
    const diff: Record<string, unknown> = {};
    for (const k of Object.keys(s) as (keyof AppSettings)[]) {
      if (s[k] !== loaded[k]) diff[k] = s[k];
    }
    if (Object.keys(diff).length === 0) {
      showFlash("Nothing to change", false);
      return;
    }
    try {
      const sent = s;
      const next = await api.updateSettings(diff as Partial<AppSettings>);
      // Take the server's copy, except for fields edited while the request was in
      // flight: those stay as typed and show up as unsaved changes for the next Save.
      setS((cur) => {
        if (!cur) return next;
        const merged = { ...next };
        for (const k of Object.keys(cur) as (keyof AppSettings)[]) {
          if (cur[k] !== sent[k]) (merged as Record<string, unknown>)[k] = cur[k];
        }
        return merged;
      });
      setLoaded(next);
      setBooksEnabled(next.books_enabled); // reflect module on/off in nav + Discover live
      setMusicEnabled(next.music_enabled);
      showFlash("Saved ✓", true);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  // The Discovery region has its own Save button; keep both copies in step with it so
  // the page's snapshot never disagrees with what was just stored.
  const syncRegion = (region: string) => {
    setS((x) => (x ? { ...x, tmdb_region: region } : x));
    setLoaded((x) => (x ? { ...x, tmdb_region: region } : x));
  };

  const tabs: { key: Tab; label: string }[] = [
    { key: "media", label: "Media" },
    { key: "library", label: "Library" },
    ...(admin ? [{ key: "system" as Tab, label: "System" }, { key: "users" as Tab, label: "Users" }] : []),
  ];

  const SaveBar = () => (
    <div className="flex items-center gap-3">
      <button onClick={save} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Save settings</button>
      {flash && <span className="text-[12px]" style={{ color: flash.good ? "var(--good)" : "var(--ink-faint)" }}>{flash.text}</span>}
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
            <Section title="Movie naming" subtitle="How imported movie files are named. Tokens are replaced per release.">
              <Field label="Folder name">
                <input value={s.naming_movie_folder} onChange={(e) => patch({ naming_movie_folder: e.target.value })} className={input} style={inputStyle} />
                <Preview>{render(s.naming_movie_folder)}</Preview>
              </Field>
              <Field label="File name">
                <input value={s.naming_movie_file} onChange={(e) => patch({ naming_movie_file: e.target.value })} className={input} style={inputStyle} />
                <Preview>{render(s.naming_movie_file)}.mkv</Preview>
              </Field>
              <TokenList tokens={TOKENS} />
            </Section>
            <Section title="Series naming" subtitle="How imported episodes are named: the show folder holds season folders, which hold episode files.">
              <Field label="Series folder">
                <input value={s.naming_series_folder} onChange={(e) => patch({ naming_series_folder: e.target.value })} className={input} style={inputStyle} />
                <Preview>{renderSeries(s.naming_series_folder)}</Preview>
              </Field>
              <Field label="Season folder">
                <input value={s.naming_series_season} onChange={(e) => patch({ naming_series_season: e.target.value })} className={input} style={inputStyle} />
                <Preview>{renderSeries(s.naming_series_season)}</Preview>
              </Field>
              <Field label="Episode file">
                <input value={s.naming_series_episode} onChange={(e) => patch({ naming_series_episode: e.target.value })} className={input} style={inputStyle} />
                <Preview>{renderSeries(s.naming_series_episode)}.mkv</Preview>
              </Field>
              <p className="text-[11px] text-ink-faint">
                <code className="font-mono">{"{episode}"}</code> is the SxxExx tag (a range for double episodes). Use <code className="font-mono">{"{season00}"}</code> for a zero-padded season number. Season 0 is always “Specials”.
              </p>
              <TokenList tokens={SERIES_TOKENS} />
            </Section>
            <Section title="Metadata" subtitle="Written into each movie folder for Plex, Jellyfin, Emby and Kodi.">
              <Toggle label="Write movie.nfo" hint="A metadata sidecar with title, plot, ids, ratings." checked={s.write_nfo} onChange={(v) => patch({ write_nfo: v })} />
              <Toggle label="Download artwork" hint="Save poster.jpg and fanart.jpg next to the movie." checked={s.download_artwork} onChange={(v) => patch({ download_artwork: v })} />
            </Section>
            <SaveBar />
          </div>
        ) : tab === "library" ? (
          <div className="flex flex-col gap-6">
            <Section title="Media folders" subtitle="Point each library at a folder in your mounted media, then scan it for existing titles. (Has its own Save folders button below the list.)">
              <LibraryFolders />
            </Section>
            <Section title="Adding titles" subtitle="Defaults when adding movies and series.">
              <Toggle label="Search on add" hint="Start searching for a release as soon as a title is added." checked={s.search_on_add} onChange={(v) => patch({ search_on_add: v })} />
            </Section>
            <SaveBar />
          </div>
        ) : tab === "system" ? (
          admin && (
            <div className="flex flex-col gap-6">
              <Section title="Modules" subtitle="Turn modules on or off. Disabling hides a module from the navigation and from Discover — nothing is deleted, and it can be re-enabled anytime.">
                <Toggle label="Books" hint="Open Library metadata, ebook & audiobook library, and the Books tab in Discover." checked={s.books_enabled} onChange={(v) => patch({ books_enabled: v })} />
                <Toggle label="Music (preview)" hint="Artists and albums from MusicBrainz with automatic album downloads. Still being hardened. Turning it off hides Music and stops its searches and imports; finished downloads wait until it's back on. Nothing is deleted." checked={s.music_enabled} onChange={(v) => patch({ music_enabled: v })} />
              </Section>
              <Section title="Plex sign-in" subtitle="Let your Plex Home members and shared users sign in with Plex — no accounts to hand out. They get a Requester account (Discover-only), and only people who actually have access to your Plex server are allowed in. Requires your Plex server to be connected in Insights.">
                <Toggle label="Allow Sign in with Plex" hint="Adds a 'Sign in with Plex' button to the login page." checked={s.plex_login_enabled} onChange={(v) => patch({ plex_login_enabled: v })} />
                <Toggle label="Auto-approve their requests" hint="Plex sign-ins' requests download immediately instead of waiting for your approval." checked={s.plex_login_auto_approve} onChange={(v) => patch({ plex_login_auto_approve: v })} />
              </Section>
              <APIKeysSection onRegionSaved={syncRegion} />
              <DiskGuardSection s={s} patch={patch} />
              <StallSection s={s} patch={patch} />
              <RecycleBin s={s} patch={patch} />
              <SaveBar />
              <DatabaseBackup />
              <OverseerrImport />
              <TautulliImport />
            </div>
          )
        ) : (
          admin && <UsersManager meId={user?.id} />
        )}
      </div>
    </>
  );
}

// DatabaseBackup is the manual "Back up now". Nightly copies happen on their own; this is
// for right before something risky. It reports the file's name and size, never its contents.
function DatabaseBackup() {
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const run = async () => {
    setBusy(true); setMsg(null);
    try {
      const b = await api.backupNow();
      setMsg({ ok: true, text: `Saved ${b.name} (${fmtBytes(b.size_bytes)}).` });
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); }
    finally { setBusy(false); }
  };
  return (
    <Section title="Database backup" subtitle="Arrmada copies its database every night (newest 7 kept) and before every update, into the backups folder next to the database. Back up now before anything risky. Backups hold your API keys and password hashes.">
      <div className="flex flex-wrap items-center gap-3">
        <button onClick={run} disabled={busy} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{busy ? "Backing up…" : "Back up now"}</button>
        {msg && <span className="break-all text-[12px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</span>}
      </div>
    </Section>
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

  // The X only opens the dialog; nothing is deleted until it's confirmed there.
  const [removing, setRemoving] = useState<AuthUser | null>(null);

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
                <button onClick={() => setRemoving(u)} title="Remove user" className="grid h-7 w-7 flex-none place-items-center rounded-lg" style={{ border: "1px solid var(--line)", color: "var(--ink-faint)" }}>
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
      {removing && <DeleteUserDialog user={removing} onClose={() => setRemoving(null)} onDeleted={() => { setRemoving(null); load(); }} />}
    </Section>
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

  useEffect(() => {
    api.userImpact(user.id).then(setImpact).catch((e: Error) => setErr(`Couldn't check what this would remove: ${e.message}`));
  }, [user.id]);

  const listening = !!impact && (impact.places > 0 || impact.listening_hours > 0);
  const hours = impact ? (impact.listening_hours >= 10 ? Math.round(impact.listening_hours) : Math.round(impact.listening_hours * 10) / 10) : 0;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try { await api.deleteUser(user.id, listening ? user.username : undefined); onDeleted(); }
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
          {impact.plex_linked && <p className="m-0 mt-2">They can sign in again with Plex unless you disable them or remove their access in Plex.</p>}
        </>
      ) : !err ? "Checking what this would remove…" : null}
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

function fmtBytes(b: number): string {
  if (!b || b <= 0) return "0 MB";
  const tb = b / 1024 ** 4;
  if (tb >= 1) return `${tb.toFixed(2)} TB`;
  const gb = b / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${(b / 1024 ** 2).toFixed(0)} MB`;
}
function fmtDay(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}
function ageOf(unix: number): string {
  const days = Math.floor((Date.now() / 1000 - unix) / 86400);
  return days <= 0 ? "today" : `${days} day${days === 1 ? "" : "s"} ago`;
}

// RecycleBin shows what the bin is holding and lets an admin set the guard rails (max size /
// retention) — saved with the page's Save button — and empty it on demand. Deleted & replaced
// files (movie/episode deletes, Convert originals) land here instead of being erased.
function RecycleBin({ s, patch }: { s: AppSettings; patch: (p: Partial<AppSettings>) => void }) {
  const [stats, setStats] = useState<RecycleStats | null>(null);
  const [items, setItems] = useState<RecycleItem[] | null>(null);
  const [showItems, setShowItems] = useState(false);
  const [busy, setBusy] = useState(false);
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const load = () => api.recycleStats().then(setStats).catch(() => setStats(null));
  const loadItems = () => api.recycleItems().then(setItems).catch(() => setItems([]));
  useEffect(() => { load(); }, []);
  useEffect(() => { if (showItems) loadItems(); }, [showItems]);

  // Both "for good" actions ask through the shared dialog; a refusal shows inside it.
  const [confirm, setConfirm] = useState<{ kind: "empty" } | { kind: "item"; it: RecycleItem } | null>(null);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);
  const closeConfirm = () => { setConfirm(null); setConfirmErr(null); };

  const empty = async () => {
    setBusy(true); setMsg(null); setConfirmErr(null);
    try {
      const r = await api.emptyRecycle();
      setMsg(`Freed ${fmtBytes(r.freed_bytes)}.`);
      closeConfirm();
      load(); if (showItems) loadItems();
    } catch (e) { setConfirmErr((e as Error).message); }
    finally { setBusy(false); }
  };

  const restore = async (it: RecycleItem) => {
    setRowBusy(it.id); setMsg(null);
    try { await api.restoreRecycle(it.id); setMsg(`Restored ${it.name}.`); load(); loadItems(); }
    catch (e) { setMsg((e as Error).message); }
    finally { setRowBusy(null); }
  };
  const deleteItem = async (it: RecycleItem) => {
    setRowBusy(it.id); setMsg(null); setConfirmErr(null);
    try { await api.deleteRecycleItem(it.id); closeConfirm(); load(); loadItems(); }
    catch (e) { setConfirmErr((e as Error).message); }
    finally { setRowBusy(null); }
  };

  const digits = (v: string) => v.replace(/[^0-9]/g, "");

  return (
    <Section title="Recycle bin" subtitle="Deleted & replaced files (movie/episode deletes and Convert originals) are moved here instead of being erased, so a mistake can be undone until the guard rails below purge it — the oldest files go first once the bin is over its size cap. Convert only starts a file whose original fits under the cap. To restore a converted film, delete the converted file first: the bin won't restore over it.">
      {stats && !stats.enabled ? (
        <p className="text-[12px] text-ink-dim">Recycling is turned off (<code>ARRMADA_RECYCLE_DIR=off</code>) — deleted files are erased immediately, and Convert deletes each original once its conversion is verified, with no undo.</p>
      ) : (
        <>
          <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1 text-[12.5px]">
            <span>Holding <b>{stats ? fmtBytes(stats.bytes) : "…"}</b>{stats ? ` · ${stats.files} file${stats.files === 1 ? "" : "s"}` : ""}</span>
            {stats?.oldest_unix ? <span className="text-ink-faint">oldest {ageOf(stats.oldest_unix)}</span> : null}
          </div>
          {stats?.dir && <div className="truncate font-mono text-[10.5px] text-ink-faint" title={stats.dir}>{stats.dir}</div>}
          {stats && stats.over_cap_bytes > 0 && (
            <p className="m-0 text-[11.5px]" style={{ color: "var(--avoid)" }}>
              Over the cap by {fmtBytes(stats.over_cap_bytes)} — {stats.protected_bytes > 0 && stats.over_cap_bytes <= stats.protected_bytes
                ? "items from the last 3 days are kept until they're older."
                : "the next hourly clean-up will trim it."}
            </p>
          )}
          {stats && stats.max_gb > 0 && stats.largest_item_bytes > stats.max_gb * 1024 ** 3 && (
            <p className="m-0 text-[11.5px]" style={{ color: "var(--avoid)" }}>Your cap ({stats.max_gb} GB) is smaller than the largest file here ({fmtBytes(stats.largest_item_bytes)}).</p>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Max size (GB)">
              <input inputMode="numeric" value={s.recycle_max_gb} onChange={(e) => patch({ recycle_max_gb: digits(e.target.value) })} placeholder="0" className={input} style={inputStyle} />
              <span className="text-[10.5px] text-ink-faint">0 = unlimited. Over this, the oldest files are purged first — never anything deleted in the last 3 days, or the newest file.</span>
            </Field>
            <Field label="Keep for (days)">
              <input inputMode="numeric" value={s.recycle_retention_days} onChange={(e) => patch({ recycle_retention_days: digits(e.target.value) })} placeholder="0" className={input} style={inputStyle} />
              <span className="text-[10.5px] text-ink-faint">0 = keep forever. Older files are auto-deleted.</span>
            </Field>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <button onClick={() => setShowItems((v) => !v)} disabled={(stats?.files ?? 0) === 0} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>{showItems ? "Hide contents" : `Manage contents${stats ? ` (${stats.files})` : ""}`}</button>
            <button onClick={() => setConfirm({ kind: "empty" })} disabled={busy || (stats?.files ?? 0) === 0} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{busy ? "Emptying…" : "Empty now"}</button>
            {msg && <span className="text-[11.5px] text-ink-dim">{msg}</span>}
          </div>

          {showItems && (
            <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
              {items === null ? (
                <div className="p-4 text-center text-[12px] text-ink-dim">Loading…</div>
              ) : items.length === 0 ? (
                <div className="p-4 text-center text-[12px] text-ink-dim">The recycle bin is empty.</div>
              ) : (
                <div className="thin-scroll max-h-[340px] overflow-y-auto">
                  {items.map((it) => (
                    <div key={it.id} className="flex items-center gap-3 px-3 py-2" style={{ borderTop: "1px solid var(--line-soft)" }}>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-[12.5px] font-medium" title={it.name}>{it.name}</div>
                        <div className="truncate font-mono text-[10px] text-ink-faint" title={it.orig_path || "origin not recorded"}>{it.orig_path || "origin not recorded"}</div>
                        {it.expires_at > 0 && <div className="text-[10px] text-ink-faint">Deleted for good on {fmtDay(it.expires_at)}</div>}
                      </div>
                      <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtBytes(it.size_bytes)}</span>
                      <span className="hidden flex-none font-mono text-[10.5px] text-ink-faint sm:block">{ageOf(it.deleted_unix)}</span>
                      <button onClick={() => restore(it)} disabled={!it.restorable || rowBusy !== null} title={it.restorable ? "Move back to its original location" : "Original location wasn't recorded for this item"} className="flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{rowBusy === it.id ? "…" : "Restore"}</button>
                      <button onClick={() => setConfirm({ kind: "item", it })} disabled={rowBusy !== null} title="Delete permanently" className="flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>Delete</button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          <p className="text-[10.5px] text-ink-faint">Guard rails run automatically about once an hour. The size/retention values save with the button below. Restore moves a file back to where it was deleted from (when that location is free).</p>
        </>
      )}
      {confirm?.kind === "empty" && (
        <ConfirmDialog
          title="Empty the recycle bin?"
          body={<>Permanently deletes {stats ? <b>{fmtBytes(stats.bytes)}</b> : "everything"}{stats ? ` (${stats.files} file${stats.files === 1 ? "" : "s"})` : ""}. This can't be undone.</>}
          confirmLabel="Delete everything for good"
          busyLabel="Emptying…"
          busy={busy}
          error={confirmErr}
          onConfirm={empty}
          onCancel={closeConfirm}
        />
      )}
      {confirm?.kind === "item" && (
        <ConfirmDialog
          title={<>Delete “{confirm.it.name}” for good?</>}
          body={<>Permanently deletes {fmtBytes(confirm.it.size_bytes)}. It can't be restored afterwards.</>}
          confirmLabel="Delete for good"
          busyLabel="Deleting…"
          busy={rowBusy !== null}
          error={confirmErr}
          onConfirm={() => deleteItem(confirm.it)}
          onCancel={closeConfirm}
        />
      )}
    </Section>
  );
}


function APIKeysSection({ onRegionSaved }: { onRegionSaved: (region: string) => void }) {
  const [keys, setKeys] = useState<APIKeyStatus[] | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  // Result of the last "Test" per key: a live request with the saved value.
  const [tests, setTests] = useState<Record<string, { ok: boolean; detail: string }>>({});
  const testKey = async (id: string) => {
    setBusy("test:" + id);
    setTests((t) => { const n = { ...t }; delete n[id]; return n; });
    try { const r = await api.testAPIKey(id); setTests((t) => ({ ...t, [id]: r })); }
    catch (e) { setTests((t) => ({ ...t, [id]: { ok: false, detail: (e as Error).message } })); }
    finally { setBusy(null); }
  };
  // Discovery region rides along in this section: it tunes what the TMDB key returns.
  const [region, setRegion] = useState("");
  const [regionSaved, setRegionSaved] = useState<string | null>(null);
  const [regionBusy, setRegionBusy] = useState(false);
  const [regionMsg, setRegionMsg] = useState<{ ok: boolean; text: string } | null>(null);

  useEffect(() => { api.apiKeys().then(setKeys).catch((e: Error) => setErr(e.message)); }, []);
  useEffect(() => {
    api.settings().then((s) => { setRegion(s.tmdb_region ?? ""); setRegionSaved(s.tmdb_region ?? ""); }).catch(() => {});
  }, []);

  const saveRegion = async () => {
    setRegionBusy(true); setRegionMsg(null);
    try {
      const next = await api.updateSettings({ tmdb_region: region.trim().toUpperCase() });
      setRegion(next.tmdb_region);
      setRegionSaved(next.tmdb_region);
      onRegionSaved(next.tmdb_region);
      setRegionMsg({ ok: true, text: region.trim() ? `Discover now favours ${region.trim().toUpperCase()} listings` : "Back to global listings" });
    } catch (e) {
      setRegionMsg({ ok: false, text: (e as Error).message });
    } finally {
      setRegionBusy(false);
    }
  };

  const saveKey = async (id: string) => {
    setBusy(id); setErr(null);
    try {
      const next = await api.setAPIKey(id, drafts[id] ?? "");
      setKeys(next);
      setDrafts((d) => { const n = { ...d }; delete n[id]; return n; }); // clear the field on success
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  return (
    <Section title="API keys" subtitle="External services Arrmada can use. A key entered here takes effect immediately — no restart — and overrides any set at install. The saved value is never shown back to you; only whether it's set.">
      {err && <div className="rounded-lg p-2.5 text-[11.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{err}</div>}
      {keys === null ? (
        <p className="text-[11.5px] text-ink-dim">Loading…</p>
      ) : (
        keys.map((k) => (
          <div key={k.id} className="flex flex-col gap-1.5 rounded-lg p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            <div className="flex items-center justify-between gap-2">
              <span className="text-[12.5px] font-semibold">{k.label}</span>
              {k.configured ? (
                <span className="rounded-full px-2 py-0.5 text-[10px] font-semibold" style={{ background: "var(--good-soft, rgba(80,200,120,.15))", color: "var(--good)" }}>
                  Set {k.hint ? `(${k.hint})` : ""}{k.source === "env" ? " · from install" : ""}
                </span>
              ) : (
                <span className="rounded-full px-2 py-0.5 text-[10px] font-semibold" style={{ background: "var(--panel)", color: "var(--ink-faint)" }}>Not set</span>
              )}
            </div>
            <p className="text-[10.5px] text-ink-faint">{k.purpose}</p>
            <div className="flex items-center gap-2">
              <input
                type={k.secret ? "password" : "text"}
                value={drafts[k.id] ?? ""}
                onChange={(e) => setDrafts((d) => ({ ...d, [k.id]: e.target.value }))}
                placeholder={k.configured ? "Enter a new value to replace it" : "Paste your key here"}
                className="min-w-0 flex-1 rounded-lg px-3 py-1.5 text-[12px]"
                style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
              />
              <button
                onClick={() => saveKey(k.id)}
                disabled={busy !== null}
                className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold"
                style={{ border: "1px solid var(--accent-line, var(--line))", color: "var(--accent)" }}
              >
                {busy === k.id ? "Saving…" : "Save"}
              </button>
              {k.testable && k.configured && (
                <button
                  onClick={() => testKey(k.id)}
                  disabled={busy !== null}
                  className="flex-none rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold"
                  style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}
                  title="Make a real request with the saved key and show what came back"
                >
                  {busy === "test:" + k.id ? "Testing…" : "Test"}
                </button>
              )}
              {k.configured && k.source === "settings" && (
                <button
                  onClick={() => { setDrafts((d) => ({ ...d, [k.id]: "" })); saveKey(k.id); }}
                  disabled={busy !== null}
                  className="flex-none rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold"
                  style={{ border: "1px solid var(--line)", color: "var(--ink-faint)" }}
                  title="Clear this key"
                >
                  Clear
                </button>
              )}
            </div>
            {tests[k.id] && (
              <p className="text-[11px]" style={{ color: tests[k.id].ok ? "var(--good)" : "var(--reject)" }}>
                {tests[k.id].ok ? "✓ " : "✗ "}{tests[k.id].detail}
              </p>
            )}
            <p className="text-[10px] text-ink-faint">
              {k.steps} <a href={k.help_url} target="_blank" rel="noreferrer" style={{ color: "var(--accent)" }}>Get one →</a>
            </p>
          </div>
        ))
      )}
      {/* Discovery region — tunes what the TMDB key returns, so it lives with the keys. */}
      <div className="flex flex-col gap-1.5 rounded-lg p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
        <div className="flex items-center justify-between gap-2">
          <span className="text-[12.5px] font-semibold">Discovery region</span>
          {regionSaved ? (
            <span className="rounded-full px-2 py-0.5 text-[10px] font-semibold" style={{ background: "var(--good-soft, rgba(80,200,120,.15))", color: "var(--good)" }}>{regionSaved}</span>
          ) : (
            <span className="rounded-full px-2 py-0.5 text-[10px] font-semibold" style={{ background: "var(--panel)", color: "var(--ink-faint)" }}>Global</span>
          )}
        </div>
        <p className="text-[10.5px] text-ink-faint">
          Localizes Discover's popular, upcoming and genre lists (release dates, theatrical calendar). Two-letter
          country code — e.g. AU, US, GB. Leave empty for TMDB's global lists.
        </p>
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={region}
            maxLength={2}
            onChange={(e) => setRegion(e.target.value.toUpperCase().replace(/[^A-Z]/g, ""))}
            placeholder="AU"
            className="w-[80px] rounded-lg px-3 py-1.5 text-center font-mono text-[12px] uppercase"
            style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
          <button
            onClick={saveRegion}
            disabled={regionBusy || region === (regionSaved ?? "")}
            className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50"
            style={{ border: "1px solid var(--accent-line, var(--line))", color: "var(--accent)" }}
          >
            {regionBusy ? "Saving…" : "Save"}
          </button>
          {regionMsg && (
            <span className="text-[10.5px]" style={{ color: regionMsg.ok ? "var(--good)" : "var(--reject)" }}>{regionMsg.text}</span>
          )}
        </div>
      </div>
    </Section>
  );
}

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

function TokenList({ tokens }: { tokens: string[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {tokens.map((t) => (
        <code key={t} className="rounded px-1.5 py-0.5 font-mono text-[10.5px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{`{${t}}`}</code>
      ))}
    </div>
  );
}

// A full downloads volume errors every torrent at once and, on a shared cache pool,
// takes everything else on that pool down with it. This is on by default for that
// reason — it isn't a preference anyone opts into deliberately after the fact.
function DiskGuardSection({ s, patch }: { s: AppSettings; patch: (p: Partial<AppSettings>) => void }) {
  const digits = (v: string) => v.replace(/[^0-9]/g, "").slice(0, 3);
  const pause = Number(s.downloads_disk_guard_pause_pct);
  const resume = Number(s.downloads_disk_guard_resume_pct);
  // Mirrors the server's rule. Equal or inverted thresholds would pause and resume on
  // alternate passes forever, so the server rejects them — say so before saving.
  const bad = !Number.isNaN(pause) && !Number.isNaN(resume) && resume >= pause;

  // The guard measures ARRMADA_DOWNLOADS_DIR and nothing else. Whether that path is
  // the torrent drive is not knowable from in here, so show the resolved path and the
  // reading taken from it and let the user confirm it against their own setup.
  const [status, setStatus] = useState<DiskGuardStatus | null>(null);
  useEffect(() => {
    api.diskGuard().then(setStatus).catch(() => setStatus(null));
  }, []);

  return (
    <Section
      title="Download disk guard"
      subtitle="Pause downloads before the downloads volume fills up. A full disk errors every torrent at once, and on a shared cache pool it takes everything else on that pool with it. Seeding torrents are never paused — they aren't writing anything, and pausing them would put your seed goals at risk."
    >
      <Note tone="warn">
        <b>This only works if your torrents live on their own drive.</b> The guard measures
        one folder — <code>ARRMADA_DOWNLOADS_DIR</code> in your <code>.env</code> — and nothing
        else. If that points at a folder on your main array rather than at the cache/torrent
        drive, the percentage here is measuring the array, and it will either never trigger or
        pause your queue for a reason that has nothing to do with downloads. Set it in
        <code>.env</code> and re-run <code>./update.sh</code> before relying on this.
      </Note>

      {status && (
        <div className="rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          <div className="text-[10px] uppercase tracking-[0.1em] text-ink-faint">Currently watching</div>
          <div className="mt-0.5 break-all font-mono text-[11.5px]">{status.path || "(not set)"}</div>
          {status.measurable ? (
            <div className="mt-1 text-[11.5px] text-ink-dim">
              {status.used_pct.toFixed(1)}% full
              {status.holding > 0 && (
                <span style={{ color: "var(--avoid)" }}>
                  {" "}· holding {status.holding} torrent{status.holding === 1 ? "" : "s"} paused
                </span>
              )}
            </div>
          ) : (
            <div className="mt-1 text-[11.5px]" style={{ color: "var(--reject)" }}>
              Arrmada can't read this folder's disk usage, so the guard will do nothing. Check the
              path exists and is mounted into the container.
            </div>
          )}
          {status.shared_with_library && (
            <div className="mt-1.5 text-[11.5px]" style={{ color: "var(--reject)" }}>
              This is the same drive as your library (<span className="font-mono">{status.library_path}</span>).
              The guard will be measuring your whole array, not a torrent drive — a threshold like
              85% almost certainly isn't what you want here.
            </div>
          )}
        </div>
      )}

      <Toggle
        label="Pause downloads when the disk gets full"
        hint="Checked every minute. Only torrents Arrmada paused are resumed — anything you paused by hand stays paused."
        checked={s.downloads_disk_guard}
        onChange={(v) => patch({ downloads_disk_guard: v })}
      />
      {s.downloads_disk_guard && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Pause at (% full)">
              <input
                inputMode="numeric"
                value={s.downloads_disk_guard_pause_pct}
                onChange={(e) => patch({ downloads_disk_guard_pause_pct: digits(e.target.value) })}
                placeholder="85"
                className={input}
                style={inputStyle}
              />
              <span className="text-[10.5px] text-ink-faint">Active downloads are paused once the volume is this full.</span>
            </Field>
            <Field label="Resume at (% full)">
              <input
                inputMode="numeric"
                value={s.downloads_disk_guard_resume_pct}
                onChange={(e) => patch({ downloads_disk_guard_resume_pct: digits(e.target.value) })}
                placeholder="80"
                className={input}
                style={inputStyle}
              />
              <span className="text-[10.5px] text-ink-faint">They restart once it drops back to this. Must be below the pause point.</span>
            </Field>
          </div>
          {bad && (
            <p className="m-0 text-[11.5px]" style={{ color: "var(--reject)" }}>
              Resume ({resume}%) must be below pause ({pause}%) — otherwise downloads would pause and resume on alternate checks.
            </p>
          )}
        </>
      )}
    </Section>
  );
}

// The global stall timeout, shown in hours and stored in minutes. Profiles left on "Use
// default" follow it. Safe to have on: a stalled download is only removed once another
// release has been grabbed in its place.
function StallSection({ s, patch }: { s: AppSettings; patch: (p: Partial<AppSettings>) => void }) {
  const minutes = s.downloads_stall_minutes ?? 360;
  const hours = Math.round((minutes / 60) * 10) / 10;
  return (
    <Section
      title="Stalled downloads"
      subtitle="When a download makes no progress for this long, Arrmada looks for another release. The stalled one is only removed once a replacement has been grabbed — with nothing else available it's left to keep trying. Time spent paused, queued by the client, rechecking or held by the disk guard doesn't count."
    >
      <Field label="Give up on a download with no progress after (hours)">
        <input
          type="number"
          min={0}
          max={168}
          step={0.5}
          value={hours}
          onChange={(e) => {
            const h = Math.min(168, Math.max(0, Number(e.target.value) || 0));
            patch({ downloads_stall_minutes: Math.round(h * 60) });
          }}
          className={input}
          style={inputStyle}
        />
        <span className="text-[10.5px] text-ink-faint">0 = never. Quality profiles can set their own time, or turn it off.</span>
      </Field>
    </Section>
  );
}

function Note({ tone, children }: { tone: "warn" | "info"; children: React.ReactNode }) {
  const color = tone === "warn" ? "var(--avoid)" : "var(--line)";
  return (
    <div
      className="rounded-lg p-3 text-[11.5px] leading-relaxed"
      style={{ background: "var(--panel-2)", border: `1px solid ${color}`, color: "var(--ink-dim)" }}
    >
      {children}
    </div>
  );
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

// OverseerrImport is the one-time migration: pull an existing Overseerr/Jellyseerr
// request history into Arrmada's Requests. Runs in the background on the server.
function OverseerrImport() {
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const run = async () => {
    if (!url.trim() || !key.trim()) return;
    setBusy(true);
    setMsg(null);
    try {
      const r = await api.importOverseerr(url.trim(), key.trim());
      setMsg({ ok: true, text: `Found ${r.found} request${r.found === 1 ? "" : "s"} — importing in the background. Approved titles are added to your library and searched; they'll appear on the Requests page as they process.` });
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section title="Import from Overseerr / Jellyseerr" subtitle="Migrating in? Pull your existing request history into Arrmada's Requests, then retire the old container. Approved/available titles are added to the library and searched; pending ones become pending requests here. Each request is attributed to its Plex requester — a Plex-linked account is created for them, so when they Sign in with Plex they see their own history. Safe to run more than once — anything already requested is skipped.">
      <Field label="Overseerr / Jellyseerr URL">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.50.247:5055" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <Field label="API key">
        <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder="from Overseerr → Settings → General → API Key" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <div className="flex items-center gap-3">
        <button onClick={run} disabled={busy || !url.trim() || !key.trim()} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
          {busy ? "Connecting…" : "Import requests"}
        </button>
        {msg && <span className="text-[11.5px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</span>}
      </div>
    </Section>
  );
}

// TautulliImport backfills Insights with an existing Tautulli watch history so stats aren't blank.
function TautulliImport() {
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const run = async () => {
    if (!url.trim() || !key.trim()) return;
    setBusy(true);
    setMsg(null);
    try {
      await api.importTautulli(url.trim(), key.trim());
      setMsg({ ok: true, text: "Connected — importing your watch history in the background. It'll fill in the Insights graphs as it processes (large histories take a few minutes)." });
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section title="Import from Tautulli" subtitle="Backfill Insights with your existing Tautulli watch history so your stats and graphs aren't empty on day one. Sessions are attributed to their Plex account (matching Sign in with Plex). Safe to run more than once — sessions already imported are skipped.">
      <Field label="Tautulli URL">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.50.247:8181" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <Field label="API key">
        <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder="from Tautulli → Settings → Web Interface → API Key" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <div className="flex items-center gap-3">
        <button onClick={run} disabled={busy || !url.trim() || !key.trim()} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
          {busy ? "Connecting…" : "Import history"}
        </button>
        {msg && <span className="text-[11.5px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</span>}
      </div>
    </Section>
  );
}
