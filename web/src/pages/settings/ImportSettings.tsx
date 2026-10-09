import { useState } from "react";
import { Link } from "react-router-dom";
import { Field, Section, input, inputStyle } from "../../components/settings/ui";
import { api } from "../../lib/api";

// Settings → Import (admin only): one-time migrations from the apps Arrmada replaces.
export function ImportSettings() {
  return (
    <div className="flex flex-col gap-6">
      <OverseerrImport />
      <TautulliImport />
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
      setMsg({ ok: true, text: `Found ${r.found} request${r.found === 1 ? "" : "s"} — importing in the background, quietly: nobody is notified about their old requests. Approved titles are added to your library and the regular searches pick them up. They appear on the Requests page as they process.` });
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section id="overseerr-import" title="Import from Overseerr / Jellyseerr" subtitle="Migrating in? Pull your existing request history into Arrmada's Requests, then retire the old container. Approved/available titles are added to the library for the regular searches to pick up; pending ones become pending requests here. It's quiet: nobody gets a notification for an old request. Each request is attributed to its Plex requester — a Plex-linked account is created for them, so when they Sign in with Plex they see their own history. Safe to run more than once — anything already requested is skipped.">
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
        {msg?.ok && <Link to="/requests" className="flex-none text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>Open Requests →</Link>}
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
    <Section id="tautulli-import" title="Import from Tautulli" subtitle="Backfill Insights with your existing Tautulli watch history so your stats and graphs aren't empty on day one. Sessions are attributed to their Plex account (matching Sign in with Plex). Safe to run more than once — sessions already imported are skipped.">
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
