import { useEffect, useState } from "react";
import { api, type AudioImportPreview, type AudioImportResult, type AudioListening, type AudioServerAdmin } from "../lib/api";
import { useMe } from "../lib/me";
import { ListenOnPhone, fmtAgo, fmtHours, serverAddress } from "./ListenOnPhone";

// AudioServerPanel is the Books page's "Audiobook server" window. Admins switch the
// server on, choose who may connect, see devices, see how much people listen (never what),
// and import places from Audiobookshelf. Everyone gets their own "My phone" card.

type Tab = "server" | "people" | "listening" | "import" | "mine";

export function AudioServerPanel({ onClose }: { onClose: () => void }) {
  const { user } = useMe();
  const admin = user?.role === "admin";
  const [tab, setTab] = useState<Tab>(admin ? "server" : "mine");
  const tabs: { key: Tab; label: string; admin?: boolean }[] = [
    { key: "server", label: "Server", admin: true },
    { key: "people", label: "People & devices", admin: true },
    { key: "listening", label: "Listening", admin: true },
    { key: "import", label: "Import", admin: true },
    { key: "mine", label: "My phone" },
  ];
  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-10 w-full max-w-[760px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-start justify-between gap-3">
          <div>
            <h2 className="m-0 text-[16px] font-bold">Audiobook server</h2>
            <p className="m-0 mt-0.5 text-[12px] text-ink-dim">Serve your audiobooks to listening apps like Lissen, with syncing that keeps everyone's place.</p>
          </div>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <div className="mb-4 flex flex-wrap gap-1.5 border-b pb-3" style={{ borderColor: "var(--line)" }}>
          {tabs.filter((t) => admin || !t.admin).map((t) => (
            <button key={t.key} onClick={() => setTab(t.key)} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ background: tab === t.key ? "var(--accent)" : "var(--panel-2)", color: tab === t.key ? "var(--accent-ink)" : "var(--ink-dim)", border: "1px solid var(--line)" }}>{t.label}</button>
          ))}
        </div>
        {tab === "server" && <ServerTab />}
        {tab === "people" && <PeopleTab />}
        {tab === "listening" && <ListeningTab />}
        {tab === "import" && <ImportTab />}
        {tab === "mine" && <ListenOnPhone />}
      </div>
    </div>
  );
}

const box = { background: "var(--panel-2)", border: "1px solid var(--line)" } as const;

function useAdmin() {
  const [data, setData] = useState<AudioServerAdmin | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = () => api.audioServer().then(setData).catch((e) => setError((e as Error).message));
  useEffect(() => { load(); }, []);
  return { data, setData, error, load };
}

function ServerTab() {
  const { data, setData, error } = useAdmin();
  const [url, setUrl] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (error) return <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  if (!data) return <div className="text-[12.5px] text-ink-dim">Loading…</div>;
  const addr = serverAddress(data);
  const toggle = async () => { setBusy(true); try { setData(await api.setAudioServer({ enabled: !data.enabled })); } finally { setBusy(false); } };
  const saveURL = async () => { setBusy(true); try { setData(await api.setAudioServer({ public_url: url ?? "" })); setUrl(null); } finally { setBusy(false); } };
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3 rounded-xl p-4" style={box}>
        <div>
          <div className="text-[13.5px] font-bold">{data.enabled ? (data.running ? "Running" : "Switched on, not running") : "Switched off"}</div>
          <div className="text-[12px] text-ink-dim">
            {data.running ? `${data.items} audiobook${data.items === 1 ? "" : "s"} served${data.items_ready < data.items ? ` · reading chapters for ${data.items - data.items_ready}` : ""}.` : data.error ? data.error : "Listening apps can't connect while it's off."}
          </div>
        </div>
        <button onClick={toggle} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={data.enabled ? { border: "1px solid var(--line)", color: "var(--ink)" } : { background: "var(--accent)", color: "var(--accent-ink)" }}>{data.enabled ? "Switch off" : "Switch on"}</button>
      </div>
      <div className="rounded-xl p-4" style={box}>
        <div className="mb-1 text-[13px] font-bold">Address</div>
        <div className="text-[12px] text-ink-dim">At home, apps connect to <code>{addr.home}</code> (port {data.host_port}, set by <code>ARRMADA_AUDIOBOOK_PORT</code> in .env).</div>
        <div className="mt-2 text-[12px] text-ink-dim">
          To listen away from home, expose port {data.host_port} — for example a Cloudflare Tunnel hostname pointing at it — and put that address here. Only the audiobook server is reachable on that port, never the rest of Arrmada.
        </div>
        <div className="mt-2 flex gap-2">
          <input value={url ?? data.public_url} onChange={(e) => setUrl(e.target.value)} placeholder="https://books.example.com" className="min-w-0 flex-1 rounded-lg px-3 py-1.5 font-mono text-[12px]" style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <button onClick={saveURL} disabled={busy || url === null} className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Save</button>
        </div>
      </div>
      <div className="rounded-xl p-4 text-[12px] text-ink-dim" style={box}>
        <div className="mb-1 text-[13px] font-bold text-[var(--ink)]">How places are kept</div>
        Play sessions are saved immediately and survive restarts. Moving forward is saved straight away; a big jump backwards only becomes the saved place once playback carries on from there for a minute, so a glitch can't reset anyone to the start. Older offline listening never replaces a newer place, and everyone can put back an earlier place from their "My phone" card.
      </div>
    </div>
  );
}

function PeopleTab() {
  const { data, setData, error, load } = useAdmin();
  if (error) return <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  if (!data) return <div className="text-[12.5px] text-ink-dim">Loading…</div>;
  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-xl p-4" style={box}>
        <div className="mb-2 text-[13px] font-bold">Who can connect</div>
        {data.users.map((u) => (
          <div key={u.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
            <span className="min-w-0 truncate"><b>{u.username}</b> <span className="text-ink-faint">· {u.role}</span></span>
            {u.eligible ? (
              <label className="flex items-center gap-2 text-[12px]">
                <input type="checkbox" checked={u.allowed} onChange={async (e) => setData(await api.setAudioUser(u.id, e.target.checked))} />
                Allowed
              </label>
            ) : (
              <span className="text-[11.5px] text-ink-faint">{u.disabled ? "Disabled account" : "Read-only accounts can't connect"}</span>
            )}
          </div>
        ))}
      </div>
      <div className="rounded-xl p-4" style={box}>
        <div className="mb-2 text-[13px] font-bold">Signed-in devices</div>
        {data.devices.length === 0 ? <div className="text-[12px] text-ink-faint">No devices yet.</div> : data.devices.map((d) => (
          <div key={d.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
            <div className="min-w-0">
              <div className="truncate"><b>{d.username}</b> · {d.device || d.client || "App"}</div>
              <div className="text-[11px] text-ink-faint">Signed in {fmtAgo(d.created_at)} · last used {fmtAgo(d.last_used_at)}</div>
            </div>
            <button onClick={() => api.revokeAudioDevice(d.id).then(load)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Sign out</button>
          </div>
        ))}
      </div>
    </div>
  );
}

function ListeningTab() {
  const [days, setDays] = useState(14);
  const [data, setData] = useState<AudioListening | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.audioListening(days).then(setData).catch((e) => setError((e as Error).message)); }, [days]);
  if (error) return <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  if (!data) return <div className="text-[12.5px] text-ink-dim">Loading…</div>;
  return (
    <div className="flex flex-col gap-3">
      <p className="m-0 text-[11.5px] text-ink-faint">How much and when people listen — never what. The listening log doesn't record books.</p>
      <div className="overflow-x-auto rounded-xl" style={box}>
        <table className="w-full text-[12.5px]">
          <thead><tr className="text-left text-[11px] text-ink-faint"><th className="p-2.5">Person</th><th className="p-2.5">Today</th><th className="p-2.5">7 days</th><th className="p-2.5">30 days</th><th className="p-2.5">All time</th><th className="p-2.5">Last listened</th></tr></thead>
          <tbody>
            {data.totals.length === 0 ? <tr><td colSpan={6} className="p-3 text-ink-faint">No listening yet.</td></tr> : data.totals.map((t) => (
              <tr key={t.user_id} style={{ borderTop: "1px solid var(--line-soft)" }}>
                <td className="p-2.5 font-semibold">{t.username || `#${t.user_id}`}</td><td className="p-2.5">{fmtHours(t.today)}</td><td className="p-2.5">{fmtHours(t.week)}</td>
                <td className="p-2.5">{fmtHours(t.month)}</td><td className="p-2.5">{fmtHours(t.all_time)}</td><td className="p-2.5 text-ink-dim">{fmtAgo(t.last_listen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="flex items-center justify-between">
        <div className="text-[13px] font-bold">Listening sessions</div>
        <select value={days} onChange={(e) => setDays(Number(e.target.value))} className="rounded-lg px-2 py-1 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
          <option value={7}>Last 7 days</option><option value={14}>Last 14 days</option><option value={30}>Last 30 days</option>
        </select>
      </div>
      <div className="thin-scroll max-h-[40vh] overflow-y-auto rounded-xl" style={box}>
        {data.sessions.length === 0 ? <div className="p-3 text-[12px] text-ink-faint">None in this period.</div> : data.sessions.map((s, i) => (
          <div key={i} className="flex items-center justify-between gap-3 px-3 py-2 text-[12.5px]" style={{ borderTop: i ? "1px solid var(--line-soft)" : undefined }}>
            <span className="min-w-0 truncate"><b>{s.username}</b> <span className="text-ink-faint">· {s.device || s.client}</span></span>
            <span className="flex-none text-ink-dim">{new Date(s.started_at).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })} · {fmtHours(s.seconds)}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function ImportTab() {
  const [preview, setPreview] = useState<AudioImportPreview | null>(null);
  const [result, setResult] = useState<AudioImportResult | null>(null);
  const [map, setMap] = useState<Record<string, number>>({});
  const [users, setUsers] = useState<{ id: number; username: string }[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.audioServer().then((d) => setUsers(d.users.filter((u) => u.eligible))).catch(() => {}); }, []);
  const upload = async (f: File) => {
    setBusy(true); setError(null); setResult(null);
    try {
      const p = await api.audioImportUpload(f);
      setPreview(p);
      setMap(Object.fromEntries(p.users.map((u) => [u.abs_id, u.user_id])));
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const apply = async () => {
    setBusy(true); setError(null);
    try { setResult(await api.audioImportApply(map)); setPreview(null); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-xl p-4 text-[12px] text-ink-dim" style={box}>
        <div className="mb-1 text-[13px] font-bold text-[var(--ink)]">Bring places over from Audiobookshelf</div>
        Upload Audiobookshelf's database — <code>absdatabase.sqlite</code> in its config folder (copy it while Audiobookshelf is stopped, or use a recent backup). People are matched by username and books by their folder, then title. Nothing is saved until you press Import, and a place that's newer in Arrmada is never replaced.
        <div className="mt-3">
          <input type="file" accept=".sqlite,.db,application/octet-stream" disabled={busy} onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} className="text-[12px]" />
        </div>
      </div>
      {error && <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>}
      {busy && <div className="text-[12.5px] text-ink-dim">Working…</div>}
      {preview && (
        <div className="rounded-xl p-4" style={box}>
          <div className="mb-2 text-[12.5px]">
            {preview.progress} saved place{preview.progress === 1 ? "" : "s"}, {preview.matched} on books Arrmada has · {preview.bookmarks} bookmark{preview.bookmarks === 1 ? "" : "s"}.
          </div>
          <div className="mb-1 text-[12px] font-semibold">People</div>
          {preview.users.map((u) => (
            <div key={u.abs_id} className="flex items-center justify-between gap-3 py-1 text-[12.5px]">
              <span>{u.abs_username} <span className="text-ink-faint">· {u.progress_rows} place{u.progress_rows === 1 ? "" : "s"}</span></span>
              <select value={map[u.abs_id] ?? 0} onChange={(e) => setMap({ ...map, [u.abs_id]: Number(e.target.value) })} className="rounded-lg px-2 py-1 text-[12px]" style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                <option value={0}>Skip</option>
                {users.map((x) => <option key={x.id} value={x.id}>{x.username}</option>)}
              </select>
            </div>
          ))}
          {preview.unmatched_books && preview.unmatched_books.length > 0 && (
            <details className="mt-2 text-[12px] text-ink-dim">
              <summary className="cursor-pointer">{preview.unmatched_books.length} book{preview.unmatched_books.length === 1 ? "" : "s"} with progress that Arrmada doesn't have</summary>
              <div className="mt-1">{preview.unmatched_books.slice(0, 50).join(" · ")}</div>
            </details>
          )}
          <div className="mt-3 flex justify-end">
            <button onClick={apply} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Import</button>
          </div>
        </div>
      )}
      {result && (
        <div className="rounded-xl p-4 text-[12.5px]" style={{ ...box, borderColor: "var(--good)" }}>
          Imported {result.imported} place{result.imported === 1 ? "" : "s"} and {result.bookmarks} bookmark{result.bookmarks === 1 ? "" : "s"}.
          {result.kept > 0 && ` Kept ${result.kept} place${result.kept === 1 ? "" : "s"} that were newer in Arrmada.`}
          {result.skipped > 0 && ` Skipped ${result.skipped} (person or book not matched).`}
        </div>
      )}
    </div>
  );
}
