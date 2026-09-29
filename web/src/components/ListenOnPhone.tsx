import { useEffect, useState } from "react";
import { api, type AudioAppPassword, type AudioHistoryEntry, type AudioPlace, type MyAudio } from "../lib/api";

// ListenOnPhone is each person's own connection to the audiobook server: the address
// and username to type into a listening app (Lissen), app passwords, the devices signed
// in, and their places in each audiobook — with earlier places they can put back.

export function fmtClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = s % 60;
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${String(ss).padStart(2, "0")}` : `${m}:${String(ss).padStart(2, "0")}`;
}

export function fmtHours(sec: number): string {
  if (sec < 60) return sec > 0 ? "<1m" : "0m";
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

export function fmtAgo(ms: number): string {
  if (!ms) return "never";
  const d = (Date.now() - ms) / 1000;
  if (d < 90) return "just now";
  if (d < 3600) return `${Math.round(d / 60)} min ago`;
  if (d < 86400) return `${Math.round(d / 3600)} h ago`;
  if (d < 86400 * 14) return `${Math.round(d / 86400)} d ago`;
  return new Date(ms).toLocaleDateString();
}

export function serverAddress(a: { public_url: string; host_port: string }): { home: string; away: string } {
  return { home: `http://${window.location.hostname}:${a.host_port}`, away: a.public_url };
}

const card = { background: "var(--panel)", border: "1px solid var(--line)" } as const;
const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

export function ListenOnPhone({ compact }: { compact?: boolean }) {
  const [data, setData] = useState<MyAudio | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = () => api.myAudio().then(setData).catch((e) => setError((e as Error).message));
  useEffect(() => { load(); }, []);

  if (error) return <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  if (!data) return <div className="text-[12.5px] text-ink-dim">Loading…</div>;
  if (!data.enabled) {
    return (
      <div className="rounded-xl p-4 text-[12.5px] text-ink-dim" style={card}>
        <b className="text-[var(--ink)]">Listen on your phone</b> — the audiobook server is switched off. An admin can switch it on under Books → Audiobook server.
      </div>
    );
  }
  if (!data.allowed) {
    return <div className="rounded-xl p-4 text-[12.5px] text-ink-dim" style={card}>Your account isn't set up to use the audiobook server. Ask an admin.</div>;
  }
  const addr = serverAddress(data);
  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-xl p-4" style={card}>
        <div className="mb-1 text-[14px] font-bold">Listen on your phone</div>
        <p className="m-0 mb-3 text-[12px] text-ink-dim">
          Use <a href="https://github.com/GrakovNe/lissen-android" target="_blank" rel="noreferrer" className="underline" style={{ color: "var(--accent)" }}>Lissen</a> (Android) or another Audiobookshelf app. Choose <b>Audiobookshelf</b> as the server type and enter:
        </p>
        <Field label="Server address" value={addr.home} note={addr.away ? "At home" : undefined} />
        {addr.away && <Field label="Server address" value={addr.away} note="Away from home" />}
        <Field label="Username" value={data.username} />
        <div className="text-[11.5px] text-ink-faint">Password: an app password from below (recommended), or your Arrmada password.</div>
        {!data.running && <div className="mt-2 text-[12px]" style={{ color: "var(--avoid)" }}>The server is switched on but not running{data.error ? `: ${data.error}` : ""}.</div>}
      </div>

      <AppPasswords list={data.app_passwords} onChange={load} />

      {data.devices.length > 0 && (
        <div className="rounded-xl p-4" style={card}>
          <div className="mb-2 text-[13px] font-bold">Signed-in devices</div>
          {data.devices.map((d) => (
            <div key={d.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <div className="min-w-0">
                <div className="truncate font-semibold">{d.device || d.client || "App"}{d.client && d.device && d.device !== d.client ? <span className="font-normal text-ink-faint"> · {d.client}</span> : null}</div>
                <div className="text-[11px] text-ink-faint">Last used {fmtAgo(d.last_used_at)}{d.app_password ? ` · app password “${d.app_password}”` : ""}</div>
              </div>
              <button onClick={() => api.revokeMyDevice(d.id).then(load)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Sign out</button>
            </div>
          ))}
        </div>
      )}

      {!compact && <Places places={data.places} onChange={load} />}
    </div>
  );
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

function AppPasswords({ list, onChange }: { list: AudioAppPassword[]; onChange: () => void }) {
  const [name, setName] = useState("");
  const [fresh, setFresh] = useState<AudioAppPassword | null>(null);
  const [busy, setBusy] = useState(false);
  const create = async () => {
    setBusy(true);
    try { const p = await api.createAppPassword(name.trim() || "Phone"); setFresh(p); setName(""); onChange(); } finally { setBusy(false); }
  };
  return (
    <div className="rounded-xl p-4" style={card}>
      <div className="mb-1 text-[13px] font-bold">App passwords</div>
      <p className="m-0 mb-2 text-[11.5px] text-ink-faint">A password just for a listening app. Your real password stays private, and you can remove one to sign that device out.</p>
      {fresh?.password && (
        <div className="mb-3 rounded-lg p-3" style={{ border: "1px solid var(--good)", background: "var(--panel-2)" }}>
          <div className="text-[11.5px]" style={{ color: "var(--good)" }}>“{fresh.name}” — type this into the app now. It won't be shown again.</div>
          <Field label="App password" value={fresh.password} />
        </div>
      )}
      {list.map((p) => (
        <div key={p.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
          <div className="min-w-0">
            <div className="truncate font-semibold">{p.name}</div>
            <div className="text-[11px] text-ink-faint">Created {fmtAgo(p.created_at)} · last used {fmtAgo(p.last_used_at)}</div>
          </div>
          <button onClick={() => api.deleteAppPassword(p.id).then(onChange)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Remove</button>
        </div>
      ))}
      <div className="mt-2 flex gap-2">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name, e.g. Pixel 8" maxLength={60} className="min-w-0 flex-1 rounded-lg px-3 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <button onClick={create} disabled={busy} className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-60" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Create</button>
      </div>
    </div>
  );
}

function Places({ places, onChange }: { places: AudioPlace[]; onChange: () => void }) {
  if (places.length === 0) return null;
  return (
    <div className="rounded-xl p-4" style={card}>
      <div className="mb-1 text-[13px] font-bold">Your places</div>
      <p className="m-0 mb-2 text-[11.5px] text-ink-faint">Where you are in each audiobook. If an app ever loses your place, put back an earlier one here.</p>
      {places.map((p) => <PlaceRow key={p.item_key} p={p} onChange={onChange} />)}
    </div>
  );
}

function PlaceRow({ p, onChange }: { p: AudioPlace; onChange: () => void }) {
  const [hist, setHist] = useState<AudioHistoryEntry[] | null>(null);
  const [open, setOpen] = useState(false);
  const toggle = () => {
    if (!open && hist === null) api.audioHistory(p.item_key).then((r) => setHist(r.history)).catch(() => setHist([]));
    setOpen(!open);
  };
  const pct = p.finished ? 100 : p.duration > 0 ? Math.round((p.position / p.duration) * 100) : 0;
  return (
    <div className="py-2" style={{ borderTop: "1px solid var(--line-soft)" }}>
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate text-[12.5px] font-semibold">{p.title}</div>
          <div className="text-[11px] text-ink-faint">
            {p.finished ? "Finished" : `${fmtClock(p.position)}${p.duration > 0 ? ` of ${fmtClock(p.duration)} · ${pct}%` : ""}`} · {fmtAgo(p.updated_at)}{p.device ? ` on ${p.device}` : ""}
          </div>
        </div>
        <button onClick={toggle} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>{open ? "Hide" : "Earlier places"}</button>
      </div>
      {open && (
        <div className="mt-1.5 flex flex-col gap-1 pl-2">
          {hist === null ? <span className="text-[11.5px] text-ink-faint">Loading…</span> : hist.length === 0 ? <span className="text-[11.5px] text-ink-faint">No earlier places yet.</span> : hist.slice(0, 20).map((h) => (
            <div key={h.id} className="flex items-center justify-between gap-2 text-[11.5px]">
              <span className="text-ink-dim">{fmtClock(h.position)} · {new Date(h.at).toLocaleString()}{h.device ? ` · ${h.device}` : ""}</span>
              <button onClick={() => api.restoreAudioPlace(p.item_key, h.id).then(onChange)} className="rounded px-2 py-0.5 font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Go back here</button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
