import { useEffect, useMemo, useState } from "react";
import { api, type AudioImportPreview, type AudioImportResult, type AudioListening, type AudioServerAdmin } from "../lib/api";
import { useMe } from "../lib/me";
import { Card, DailyBars, DaysPicker, Loading, Sessions, card, danger, fmtAgo, fmtHours, ghost, inputStyle, primary } from "./audiobooks/shared";

// The admin half of Audiobooks: the server switch, who may connect, everyone's listening
// (how much and when, never what) and the Audiobookshelf import. It is its own chunk, loaded
// only when an admin opens one of these tabs, so requesters never download it.

export type AdminTab = "server" | "people" | "listening" | "import";

export function AudiobooksAdmin({ tab }: { tab: AdminTab }) {
  if (tab === "server") return <ServerView />;
  if (tab === "people") return <PeopleView />;
  if (tab === "listening") return <AllListeningView />;
  return <ImportView />;
}

function useAdmin() {
  const [data, setData] = useState<AudioServerAdmin | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = () => api.audioServer().then(setData).catch((e) => setError((e as Error).message));
  useEffect(() => { load(); }, []);
  return { data, setData, error, load };
}

function ServerView() {
  const { external } = useMe();
  const { data, setData, error } = useAdmin();
  const [url, setUrl] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (!data) return <Loading error={error} />;
  const toggle = async () => { setBusy(true); try { setData(await api.setAudioServer({ enabled: !data.enabled })); } finally { setBusy(false); } };
  const saveURL = async () => { setBusy(true); try { setData(await api.setAudioServer({ public_url: url ?? "" })); setUrl(null); } finally { setBusy(false); } };
  const state = data.enabled ? (data.running ? "Running" : "Switched on, not running") : "Switched off";
  const dot = data.enabled ? (data.running ? "var(--good)" : "var(--reject)") : "var(--ink-faint)";
  return (
    <div className="flex flex-col gap-4">
      <section className="flex items-center justify-between gap-3 rounded-xl p-4" style={card}>
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-[14px] font-bold"><span className="h-2 w-2 rounded-full" style={{ background: dot }} />{state}</div>
          <div className="text-[12px] text-ink-dim">
            {data.running ? `${data.items} audiobook${data.items === 1 ? "" : "s"} served${data.items_ready < data.items ? ` · reading chapters for ${data.items - data.items_ready}` : ""}.` : data.error ? data.error : "Listening apps can't connect while it's off."}
            {" "}Switching it off also turns off listening in Arrmada.
          </div>
        </div>
        <button onClick={toggle} disabled={busy} className="flex-none rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={data.enabled ? ghost : primary}>{data.enabled ? "Switch off" : "Switch on"}</button>
      </section>
      <Card title="Address">
        {!external && <div className="text-[12px] text-ink-dim">At home, apps connect to <code>http://{window.location.hostname}:{data.host_port}</code> (port {data.host_port}, set by <code>ARRMADA_AUDIOBOOK_PORT</code> in .env).</div>}
        <div className="mt-2 text-[12px] text-ink-dim">
          To listen away from home, point a Cloudflare Tunnel hostname (or another reverse proxy) at port {data.host_port} and put that address here. Only the audiobook server answers on that port, never the rest of Arrmada.
        </div>
        <div className="mt-2 flex gap-2">
          <input value={url ?? data.public_url} onChange={(e) => setUrl(e.target.value)} placeholder="https://books.example.com" className="min-w-0 flex-1 rounded-lg px-3 py-1.5 font-mono text-[12px]" style={inputStyle} />
          <button onClick={saveURL} disabled={busy || url === null} className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50" style={primary}>Save</button>
        </div>
      </Card>
      <TraceCard data={data} setData={setData} />
      <Card title="How places are kept">
        <p className="m-0 text-[12px] text-ink-dim">
          Play sessions are saved as they happen and survive restarts. Moving forward is saved straight away. A big jump backwards is held until playback carries on from there for 30 seconds, so a glitch can't reset anyone to the start — and the person can confirm it sooner (Keep it now in the player, or Use this spot on the book). A jump to the very end of a book needs the same proof, so one bad report can't mark it finished. Older offline listening never replaces a newer place, and everyone can put back an earlier place — or a later spot an app sent that wasn't used, or one an app removed. A place an app sets without playing, and Arrmada's own web player, follow the same rules.
        </p>
      </Card>
    </div>
  );
}

// TraceCard switches on a day of logging every request listening apps make — how a new
// app that fails quietly gets debugged. Lines name the kind of call, never the book.
function TraceCard({ data, setData }: { data: AudioServerAdmin; setData: (d: AudioServerAdmin) => void }) {
  const [busy, setBusy] = useState(false);
  const set = async (hours: number) => { setBusy(true); try { setData(await api.setAudioServer({ trace_hours: hours })); } finally { setBusy(false); } };
  const on = data.trace_until > 0; // the server answers 0 once it has run out
  const until = on ? new Date(data.trace_until).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
  return (
    <Card title="Trace app requests">
      <div className="flex items-center justify-between gap-3">
        <p className="m-0 min-w-0 text-[12px] text-ink-dim">
          {on
            ? <>Tracing until {until} — every request an app makes is logged (routes only, never which book).</>
            : "When an app connects but something doesn't work, switch this on and try again: for the next 24 hours every request apps make is logged on the Logs page (routes only, never which book). It switches itself off."}
        </p>
        <button onClick={() => set(on ? 0 : 24)} disabled={busy} className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-60" style={on ? ghost : primary}>{on ? "Stop" : "Trace for 24 hours"}</button>
      </div>
    </Card>
  );
}

function PeopleView() {
  const { data, setData, error, load } = useAdmin();
  if (!data) return <Loading error={error} />;
  return (
    <div className="flex flex-col gap-4">
      <Card title="Who can connect" note="Each person also needs to set an audiobook password on their Audiobooks page.">
        {data.users.map((u) => (
          <div key={u.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
            <span className="min-w-0 truncate"><b>{u.username}</b> <span className="text-ink-faint">· {u.role}</span></span>
            {u.eligible ? (
              <span className="flex flex-none items-center gap-3">
                <span className="text-[11.5px]" style={{ color: u.has_password ? "var(--good)" : "var(--ink-faint)" }}>{u.has_password ? "Password set" : "No password yet"}</span>
                <label className="flex items-center gap-2 text-[12px]">
                  <input type="checkbox" checked={u.allowed} onChange={async (e) => setData(await api.setAudioUser(u.id, e.target.checked))} />
                  Allowed
                </label>
              </span>
            ) : (
              <span className="text-[11.5px] text-ink-faint">{u.disabled ? "Disabled account" : "Read-only accounts can't connect"}</span>
            )}
          </div>
        ))}
      </Card>
      <Card title="Signed-in devices">
        {data.devices.length === 0 ? <div className="text-[12px] text-ink-faint">No devices yet.</div> : data.devices.map((d) => (
          <div key={d.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
            <div className="min-w-0">
              <div className="truncate"><b>{d.username}</b> · {d.device || d.client || "App"}</div>
              <div className="text-[11px] text-ink-faint">Signed in {fmtAgo(d.created_at)} · last used {fmtAgo(d.last_used_at)}</div>
            </div>
            <button onClick={() => api.revokeAudioDevice(d.id).then(load)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={danger}>Sign out</button>
          </div>
        ))}
      </Card>
    </div>
  );
}

function AllListeningView() {
  const [days, setDays] = useState(30);
  const [who, setWho] = useState(0);
  const [data, setData] = useState<AudioListening | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.audioListening(days).then(setData).catch((e) => setError((e as Error).message)); }, [days]);
  const perDay = useMemo(() => {
    const m = new Map<string, number>();
    for (const d of data?.daily ?? []) if (!who || d.user_id === who) m.set(d.day, (m.get(d.day) ?? 0) + d.seconds);
    return m;
  }, [data, who]);
  if (!data) return <Loading error={error} />;
  const sessions = who ? data.sessions.filter((s) => s.user_id === who) : data.sessions;
  return (
    <div className="flex flex-col gap-4">
      <p className="m-0 text-[11.5px] text-ink-faint">How much and when people listen — never what. Nothing in Arrmada, logs included, records which book anyone plays.</p>
      <div className="overflow-x-auto rounded-xl" style={card}>
        <table className="w-full text-[12.5px]">
          <thead><tr className="text-left text-[11px] text-ink-faint"><th className="p-2.5">Person</th><th className="p-2.5">Today</th><th className="p-2.5">7 days</th><th className="p-2.5">30 days</th><th className="p-2.5">All time</th><th className="p-2.5">Last listened</th></tr></thead>
          <tbody>
            {data.totals.length === 0 ? <tr><td colSpan={6} className="p-3 text-ink-faint">No listening yet.</td></tr> : data.totals.map((t) => (
              <tr key={t.user_id} onClick={() => setWho(who === t.user_id ? 0 : t.user_id)} className="cursor-pointer" style={{ borderTop: "1px solid var(--line-soft)", background: who === t.user_id ? "var(--accent-soft)" : undefined }}>
                <td className="p-2.5 font-semibold">{t.username || `#${t.user_id}`}</td><td className="p-2.5">{fmtHours(t.today)}</td><td className="p-2.5">{fmtHours(t.week)}</td>
                <td className="p-2.5">{fmtHours(t.month)}</td><td className="p-2.5">{fmtHours(t.all_time)}</td><td className="p-2.5 text-ink-dim">{fmtAgo(t.last_listen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Card
        title="Listening per day"
        right={
          <div className="flex flex-none gap-2">
            <select value={who} onChange={(e) => setWho(Number(e.target.value))} className="rounded-lg px-2 py-1 text-[12px]" style={inputStyle}>
              <option value={0}>Everyone</option>
              {data.totals.map((t) => <option key={t.user_id} value={t.user_id}>{t.username || `#${t.user_id}`}</option>)}
            </select>
            <DaysPicker days={days} setDays={setDays} />
          </div>
        }
      >
        <DailyBars since={data.since} days={data.days} perDay={perDay} />
        <div className="mb-1 mt-4 text-[12.5px] font-bold">Sessions</div>
        <Sessions list={sessions} showUser={!who} />
      </Card>
    </div>
  );
}

function ImportView() {
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
    <div className="flex flex-col gap-4">
      <Card title="Bring places over from Audiobookshelf">
        <p className="m-0 text-[12px] text-ink-dim">
          Upload Audiobookshelf's database — <code>absdatabase.sqlite</code> in its config folder (copy it while Audiobookshelf is stopped, or use a recent backup). People are matched by username and books by their folder, then title. Nothing is saved until you press Import, and a place that's newer in Arrmada is never replaced.
        </p>
        <div className="mt-3">
          <input type="file" accept=".sqlite,.db,application/octet-stream" disabled={busy} onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} className="text-[12px]" />
        </div>
      </Card>
      {error && <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>}
      {busy && <div className="text-[12.5px] text-ink-dim">Working…</div>}
      {preview && (
        <section className="rounded-xl p-4" style={card}>
          <div className="mb-2 text-[12.5px]">
            {preview.progress} saved place{preview.progress === 1 ? "" : "s"}, {preview.matched} on books Arrmada has · {preview.bookmarks} bookmark{preview.bookmarks === 1 ? "" : "s"}.
          </div>
          <div className="mb-1 text-[12px] font-semibold">People</div>
          {preview.users.map((u) => (
            <div key={u.abs_id} className="flex items-center justify-between gap-3 py-1 text-[12.5px]">
              <span>{u.abs_username} <span className="text-ink-faint">· {u.progress_rows} place{u.progress_rows === 1 ? "" : "s"}</span></span>
              <select value={map[u.abs_id] ?? 0} onChange={(e) => setMap({ ...map, [u.abs_id]: Number(e.target.value) })} className="rounded-lg px-2 py-1 text-[12px]" style={inputStyle}>
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
            <button onClick={apply} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={primary}>Import</button>
          </div>
        </section>
      )}
      {result && (
        <div className="rounded-xl p-4 text-[12.5px]" style={{ ...card, borderColor: "var(--good)" }}>
          Imported {result.imported} place{result.imported === 1 ? "" : "s"} and {result.bookmarks} bookmark{result.bookmarks === 1 ? "" : "s"}.
          {result.kept > 0 && ` Kept ${result.kept} place${result.kept === 1 ? "" : "s"} that were newer in Arrmada.`}
          {result.skipped > 0 && ` Skipped ${result.skipped} (person or book not matched).`}
        </div>
      )}
    </div>
  );
}
