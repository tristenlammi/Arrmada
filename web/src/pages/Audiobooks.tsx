import { useEffect, useMemo, useState } from "react";
import {
  api,
  type AudioHistoryEntry,
  type AudioImportPreview,
  type AudioImportResult,
  type AudioListening,
  type AudioPlace,
  type AudioServerAdmin,
  type MyAudio,
} from "../lib/api";
import { useMe } from "../lib/me";
import { posterThumb } from "../lib/img";
import { PageHeader } from "../components/PageHeader";

// Audiobooks is the audiobook server's home: Arrmada serving audiobooks to listening
// apps (Lissen, or anything that speaks Audiobookshelf). Everyone gets "You": how to
// connect, their audiobook password, where they're up to, their listening and devices.
// Admins also get the server switch, who may connect, everyone's listening (how much and
// when — never what) and the Audiobookshelf import. Nobody sees anyone else's places.

type Tab = "you" | "server" | "people" | "listening" | "import";

export function Audiobooks({ chrome = true }: { chrome?: boolean }) {
  const { user } = useMe();
  const admin = user?.role === "admin";
  const [tab, setTab] = useState<Tab>("you");
  const tabs: { key: Tab; label: string }[] = [
    { key: "you", label: "You" },
    { key: "server", label: "Server" },
    { key: "people", label: "People" },
    { key: "listening", label: "Listening" },
    { key: "import", label: "Import" },
  ];
  return (
    <>
      {chrome && <PageHeader title="Audiobooks" crumb="Services / Audiobooks" />}
      <div className="mx-auto w-full max-w-[980px] px-4 py-6 sm:px-6">
        {!chrome && <h1 className="m-0 mb-1 text-[18px] font-bold">Audiobooks</h1>}
        <p className="m-0 mb-4 max-w-[70ch] text-[12.5px] text-ink-dim">
          Listen to the library's audiobooks in a listening app like Lissen. Your place is kept in Arrmada, so it follows you between devices and a glitch can't lose it.
        </p>
        {admin && (
          <div className="mb-5 flex gap-0.5 overflow-x-auto overflow-y-hidden border-b sm:gap-1" style={{ borderColor: "var(--line)" }}>
            {tabs.map((t) => {
              const active = tab === t.key;
              return (
                <button key={t.key} onClick={() => setTab(t.key)} className="relative flex-none px-3 py-2.5 text-[13.5px] font-semibold transition-colors sm:px-4" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                  {t.label}
                  {active && <span className="absolute inset-x-2 bottom-0 h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
                </button>
              );
            })}
          </div>
        )}
        {tab === "you" && <YouView />}
        {admin && tab === "server" && <ServerView />}
        {admin && tab === "people" && <PeopleView />}
        {admin && tab === "listening" && <AllListeningView />}
        {admin && tab === "import" && <ImportView />}
      </div>
    </>
  );
}

// --- formatting ---------------------------------------------------------------

function fmtClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = s % 60;
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${String(ss).padStart(2, "0")}` : `${m}:${String(ss).padStart(2, "0")}`;
}

function fmtHours(sec: number): string {
  if (sec < 60) return sec > 0 ? "<1m" : "0m";
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function fmtAgo(ms: number): string {
  if (!ms) return "never";
  const d = (Date.now() - ms) / 1000;
  if (d < 90) return "just now";
  if (d < 3600) return `${Math.round(d / 60)} min ago`;
  if (d < 86400) return `${Math.round(d / 3600)} h ago`;
  if (d < 86400 * 14) return `${Math.round(d / 86400)} d ago`;
  return new Date(ms).toLocaleDateString();
}

function fmtWhen(ms: number): string {
  return new Date(ms).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
}

// --- shared bits ----------------------------------------------------------------

const card = { background: "var(--panel)", border: "1px solid var(--line)" } as const;
const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;
const inputStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
const danger = { border: "1px solid var(--reject)", color: "var(--reject)" } as const;
const primary = { background: "var(--accent)", color: "var(--accent-ink)" } as const;

function Card({ title, note, children, right }: { title: string; note?: string; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <section className="rounded-xl p-4" style={card}>
      <div className="mb-2 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="m-0 text-[14px] font-bold">{title}</h2>
          {note && <p className="m-0 mt-0.5 text-[11.5px] text-ink-faint">{note}</p>}
        </div>
        {right}
      </div>
      {children}
    </section>
  );
}

function Loading({ error }: { error?: string | null }) {
  if (error) return <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  return <div className="text-[12.5px] text-ink-dim">Loading…</div>;
}

function Field({ label, value, note }: { label: string; value: string; note?: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => { navigator.clipboard?.writeText(value).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }).catch(() => {}); };
  return (
    <div className="mb-2">
      <div className="text-[11px] font-semibold text-ink-faint">{label}{note ? ` · ${note}` : ""}</div>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-lg px-2.5 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>{value}</code>
        <button onClick={copy} className="flex-none rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={ghost}>{copied ? "Copied" : "Copy"}</button>
      </div>
    </div>
  );
}

// The addresses to type into an app. The home one only means something on the home
// network, so from outside only the public address is shown.
function addresses(a: { public_url: string; host_port: string }, external: boolean): { label: string; value: string; note?: string }[] {
  const out: { label: string; value: string; note?: string }[] = [];
  if (!external) out.push({ label: "Server address", value: `http://${window.location.hostname}:${a.host_port}`, note: a.public_url ? "At home" : undefined });
  if (a.public_url) out.push({ label: "Server address", value: a.public_url, note: external ? undefined : "Away from home" });
  return out;
}

// DailyBars draws one bar per day from `since` for `days` days.
function DailyBars({ since, days, perDay }: { since: string; days: number; perDay: Map<string, number> }) {
  const list = useMemo(() => {
    const [y, m, d] = since.split("-").map(Number);
    return Array.from({ length: days }, (_, i) => {
      const dt = new Date(y, m - 1, d + i);
      const key = `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, "0")}-${String(dt.getDate()).padStart(2, "0")}`;
      return { key, dt, secs: perDay.get(key) ?? 0 };
    });
  }, [since, days, perDay]);
  const max = Math.max(...list.map((x) => x.secs), 0);
  if (max === 0) return <div className="py-6 text-center text-[12px] text-ink-faint">No listening in this period.</div>;
  const label = (dt: Date) => dt.toLocaleDateString([], { day: "numeric", month: "short" });
  return (
    <div>
      <div className="mb-1 text-right text-[10.5px] text-ink-faint">top {fmtHours(max)}</div>
      <div className="flex h-[110px] items-end gap-[3px]">
        {list.map((x) => (
          <div key={x.key} className="group relative flex h-full flex-1 items-end" title={`${x.dt.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" })} · ${fmtHours(x.secs)}`}>
            <div className="w-full rounded-t-[3px]" style={{ height: x.secs > 0 ? `${Math.max(4, (x.secs / max) * 100)}%` : "2px", background: x.secs > 0 ? "var(--accent)" : "var(--line)", opacity: x.secs > 0 ? 0.9 : 1 }} />
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-[10.5px] text-ink-faint">
        <span>{label(list[0].dt)}</span>
        <span>Today</span>
      </div>
    </div>
  );
}

function Totals({ t }: { t?: { today: number; week: number; month: number; all_time: number } }) {
  const cells = [
    ["Today", t?.today ?? 0],
    ["7 days", t?.week ?? 0],
    ["30 days", t?.month ?? 0],
    ["All time", t?.all_time ?? 0],
  ] as const;
  return (
    <div className="mb-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
      {cells.map(([k, v]) => (
        <div key={k} className="rounded-lg px-3 py-2" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
          <div className="text-[10.5px] font-semibold uppercase tracking-wide text-ink-faint">{k}</div>
          <div className="text-[16px] font-bold">{fmtHours(v)}</div>
        </div>
      ))}
    </div>
  );
}

function Sessions({ list, showUser }: { list: AudioListening["sessions"]; showUser?: boolean }) {
  if (list.length === 0) return <div className="text-[12px] text-ink-faint">No listening sessions in this period.</div>;
  return (
    <div className="thin-scroll max-h-[320px] overflow-y-auto">
      {list.map((s, i) => (
        <div key={i} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: i ? "1px solid var(--line-soft)" : undefined }}>
          <span className="min-w-0 truncate">
            {showUser && <b>{s.username} · </b>}
            <span className={showUser ? "text-ink-faint" : ""}>{s.device || s.client || "App"}</span>
          </span>
          <span className="flex-none text-ink-dim">{fmtWhen(s.started_at)} · {fmtHours(s.seconds)}</span>
        </div>
      ))}
    </div>
  );
}

function DaysPicker({ days, setDays }: { days: number; setDays: (d: number) => void }) {
  return (
    <select value={days} onChange={(e) => setDays(Number(e.target.value))} className="rounded-lg px-2 py-1 text-[12px]" style={inputStyle}>
      <option value={14}>14 days</option><option value={30}>30 days</option><option value={90}>90 days</option>
    </select>
  );
}

// --- You ------------------------------------------------------------------------

function YouView() {
  const { external } = useMe();
  const [data, setData] = useState<MyAudio | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = () => api.myAudio().then(setData).catch((e) => setError((e as Error).message));
  useEffect(() => { load(); }, []);
  if (!data) return <Loading error={error} />;

  if (!data.allowed) {
    return <Card title="Not available">
      <p className="m-0 text-[12.5px] text-ink-dim">Your account isn't set up to use the audiobook server. Ask an admin.</p>
    </Card>;
  }
  return (
    <div className="flex flex-col gap-4">
      <Card title="Connect an app" note={data.enabled ? undefined : "The audiobook server is switched off at the moment, so apps can't connect yet."}>
        <p className="m-0 mb-3 text-[12px] text-ink-dim">
          Use <a href="https://github.com/GrakovNe/lissen-android" target="_blank" rel="noreferrer" className="underline" style={{ color: "var(--accent)" }}>Lissen</a> (Android) or another Audiobookshelf app. Choose <b>Audiobookshelf</b> as the server type and enter:
        </p>
        {addresses(data, external).map((a, i) => <Field key={i} label={a.label} value={a.value} note={a.note} />)}
        {external && !data.public_url && <div className="mb-2 text-[12px] text-ink-dim">The server address works on the home network. Open this page at home to see it.</div>}
        <Field label="Username" value={data.username} />
        <div className="text-[11.5px] text-ink-faint">Password: your audiobook password below — not your Arrmada password.</div>
        {data.enabled && !data.running && <div className="mt-2 text-[12px]" style={{ color: "var(--avoid)" }}>The server is switched on but not running{data.error ? `: ${data.error}` : ""}.</div>}
      </Card>

      <PasswordCard data={data} onSaved={setData} />
      <PlacesCard places={data.places} onChange={load} />
      <MyListeningCard />
      {data.devices.length > 0 && (
        <Card title="Your devices" note="Apps signed in as you. Sign one out if you lose the phone or stop using it.">
          {data.devices.map((d) => (
            <div key={d.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <div className="min-w-0">
                <div className="truncate font-semibold">{d.device || d.client || "App"}{d.client && d.device && d.device !== d.client ? <span className="font-normal text-ink-faint"> · {d.client}</span> : null}</div>
                <div className="text-[11px] text-ink-faint">Signed in {fmtAgo(d.created_at)} · last used {fmtAgo(d.last_used_at)}</div>
              </div>
              <button onClick={() => api.revokeMyDevice(d.id).then(load)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={danger}>Sign out</button>
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}

function PasswordCard({ data, onSaved }: { data: MyAudio; onSaved: (d: MyAudio) => void }) {
  const [open, setOpen] = useState(!data.has_password);
  const [pw, setPw] = useState("");
  const [again, setAgain] = useState("");
  const [signOut, setSignOut] = useState(true);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const min = data.min_password_length || 8;
  const problem = pw.length > 0 && pw.length < min ? `At least ${min} characters.` : again.length > 0 && pw !== again ? "The passwords don't match." : null;
  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      const d = await api.setAudioPassword(pw, data.has_password && signOut);
      onSaved(d); setPw(""); setAgain(""); setOpen(false);
      setMsg({ ok: true, text: data.has_password ? "Password changed." : "Password set — you can sign in from an app now." });
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!window.confirm("Remove your audiobook password? Your apps will be signed out and can't connect until you set a new one.")) return;
    setBusy(true); setMsg(null);
    try { onSaved(await api.removeAudioPassword()); setOpen(true); } catch (e) { setMsg({ ok: false, text: (e as Error).message }); } finally { setBusy(false); }
  };
  return (
    <Card
      title="Audiobook password"
      note={data.has_password ? "Set. Apps sign in with your username and this password." : "Not set yet — set one to connect an app. It's separate from your Arrmada password."}
      right={data.has_password && !open ? (
        <div className="flex flex-none gap-2">
          <button onClick={() => setOpen(true)} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>Change</button>
          <button onClick={remove} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={danger}>Remove</button>
        </div>
      ) : undefined}
    >
      {open && (
        <form onSubmit={(e) => { e.preventDefault(); if (!problem && pw && pw === again) save(); }} className="flex max-w-[420px] flex-col gap-2">
          <input type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} placeholder={data.has_password ? "New password" : "Password"} className="rounded-lg px-3 py-1.5 text-[12.5px]" style={inputStyle} />
          <input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} placeholder="Type it again" className="rounded-lg px-3 py-1.5 text-[12.5px]" style={inputStyle} />
          {data.has_password && (
            <label className="flex items-center gap-2 text-[12px] text-ink-dim">
              <input type="checkbox" checked={signOut} onChange={(e) => setSignOut(e.target.checked)} />
              Sign out my apps (they'll need the new password)
            </label>
          )}
          {problem && <div className="text-[12px]" style={{ color: "var(--avoid)" }}>{problem}</div>}
          <div className="flex gap-2">
            <button type="submit" disabled={busy || !!problem || !pw || pw !== again} className="rounded-lg px-3.5 py-1.5 text-[12.5px] font-semibold disabled:opacity-50" style={primary}>{data.has_password ? "Change password" : "Set password"}</button>
            {data.has_password && <button type="button" onClick={() => { setOpen(false); setPw(""); setAgain(""); }} className="rounded-lg px-3 py-1.5 text-[12.5px] font-semibold" style={ghost}>Cancel</button>}
          </div>
        </form>
      )}
      {msg && <div className="mt-2 text-[12px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</div>}
    </Card>
  );
}

function PlacesCard({ places, onChange }: { places: AudioPlace[]; onChange: () => void }) {
  const [showFinished, setShowFinished] = useState(false);
  const going = places.filter((p) => !p.finished);
  const done = places.filter((p) => p.finished);
  return (
    <Card title="Where you're up to" note="Your place in each audiobook. If an app ever loses it, put back an earlier one.">
      {places.length === 0 && <div className="text-[12px] text-ink-faint">Nothing yet — start an audiobook in your app and it shows up here.</div>}
      {going.map((p) => <PlaceRow key={p.item_key} p={p} onChange={onChange} />)}
      {done.length > 0 && (
        <button onClick={() => setShowFinished(!showFinished)} className="mt-2 text-[12px] font-semibold" style={{ color: "var(--accent)" }}>
          {showFinished ? "Hide finished" : `Finished (${done.length})`}
        </button>
      )}
      {showFinished && done.map((p) => <PlaceRow key={p.item_key} p={p} onChange={onChange} />)}
    </Card>
  );
}

function PlaceRow({ p, onChange }: { p: AudioPlace; onChange: () => void }) {
  const [hist, setHist] = useState<AudioHistoryEntry[] | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const toggle = () => {
    if (!open && hist === null) api.audioHistory(p.item_key).then((r) => setHist(r.history)).catch(() => setHist([]));
    setOpen(!open);
  };
  const act = async (f: () => Promise<unknown>) => { setBusy(true); try { await f(); setHist(null); setOpen(false); onChange(); } finally { setBusy(false); } };
  const pct = p.finished ? 100 : p.duration > 0 ? Math.min(100, (p.position / p.duration) * 100) : 0;
  const pending = p.pending_position ?? null;
  return (
    <div className="py-2.5" style={{ borderTop: "1px solid var(--line-soft)" }}>
      <div className="flex items-center gap-3">
        <div className="h-[52px] w-[52px] flex-none overflow-hidden rounded-md" style={{ background: "var(--panel-2)" }}>
          {p.cover_url && <img src={posterThumb(p.cover_url)} alt="" className="h-full w-full object-cover" loading="lazy" />}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold">{p.title}</div>
          {p.author && <div className="truncate text-[11.5px] text-ink-dim">{p.author}</div>}
          <div className="mt-1 h-[4px] overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: p.finished ? "var(--good)" : "var(--accent)" }} />
          </div>
          <div className="mt-0.5 text-[11px] text-ink-faint">
            {p.finished ? "Finished" : `${fmtClock(p.position)}${p.duration > 0 ? ` of ${fmtClock(p.duration)} · ${Math.round(pct)}%` : ""}`} · {fmtAgo(p.updated_at)}{p.device ? ` on ${p.device}` : ""}
          </div>
        </div>
        <button onClick={toggle} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>{open ? "Hide" : "Earlier places"}</button>
      </div>
      {pending !== null && (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2 text-[12px]" style={{ background: "var(--avoid-soft)", border: "1px solid var(--avoid)" }}>
          <span>An app jumped back to <b>{fmtClock(pending)}</b>. It's kept once you listen on from there for a bit — or use it now if that was you.</span>
          <button onClick={() => act(() => api.acceptAudioJump(p.item_key))} disabled={busy} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>Use this spot</button>
        </div>
      )}
      {open && (
        <div className="mt-2 flex flex-col gap-1 pl-[64px]">
          {hist === null ? <span className="text-[11.5px] text-ink-faint">Loading…</span> : hist.length === 0 ? <span className="text-[11.5px] text-ink-faint">No earlier places yet.</span> : hist.slice(0, 20).map((h) => (
            <div key={h.id} className="flex items-center justify-between gap-2 text-[11.5px]">
              <span className="text-ink-dim">{fmtClock(h.position)} · {new Date(h.at).toLocaleString()}{h.device ? ` · ${h.device}` : ""}</span>
              <button onClick={() => act(() => api.restoreAudioPlace(p.item_key, h.id))} disabled={busy} className="flex-none rounded px-2 py-0.5 font-semibold disabled:opacity-60" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Go back here</button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function MyListeningCard() {
  const [days, setDays] = useState(30);
  const [data, setData] = useState<AudioListening | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.myAudioListening(days).then(setData).catch((e) => setError((e as Error).message)); }, [days]);
  const perDay = useMemo(() => new Map((data?.daily ?? []).map((d) => [d.day, d.seconds])), [data]);
  return (
    <Card title="Your listening" right={<DaysPicker days={days} setDays={setDays} />}>
      {!data ? <Loading error={error} /> : (
        <>
          <Totals t={data.totals[0]} />
          <DailyBars since={data.since} days={data.days} perDay={perDay} />
          <div className="mb-1 mt-4 text-[12.5px] font-bold">Sessions</div>
          <Sessions list={data.sessions} />
        </>
      )}
    </Card>
  );
}

// --- admin ----------------------------------------------------------------------

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
      <Card title="How places are kept">
        <p className="m-0 text-[12px] text-ink-dim">
          Play sessions are saved as they happen and survive restarts. Moving forward is saved straight away. A big jump backwards is held until playback carries on from there for 30 seconds, so a glitch can't reset anyone to the start — and the person can confirm it sooner on their Audiobooks page. Older offline listening never replaces a newer place, and everyone can put back an earlier place.
        </p>
      </Card>
    </div>
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
