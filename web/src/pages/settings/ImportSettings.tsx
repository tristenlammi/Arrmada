import { useState } from "react";
import { Link } from "react-router-dom";
import { Section, input, inputStyle } from "../../components/settings/ui";
import { Field } from "../../ui/Field";

import { importApi } from "../../lib/importApi";
import { AudiobookshelfImport } from "./AudiobookshelfImport";
import { ArrImport } from "./ArrImport";
import { TautulliImport } from "./TautulliImport";

// Settings → Import & migrate (admin only): every one-time migration from the apps Arrmada
// replaces, in one place — the library from Radarr and Sonarr, request history from
// Overseerr, watch history from Tautulli and listening places from Audiobookshelf. Each is
// safe to run again. The library comes first: requests then find their titles already here.
export function ImportSettings() {
  return (
    <div className="flex flex-col gap-6">
      <ArrImport />
      <OverseerrImport />
      <TautulliImport />
      <AudiobookshelfImport />
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
      const r = await importApi.importOverseerr(url.trim(), key.trim());
      setMsg({ ok: true, text: `Found ${r.found} request${r.found === 1 ? "" : "s"} — importing in the background, quietly: nobody is notified about their old requests. Approved titles still on their way are added to your library and searched a few at a time; deleted, declined and finished ones you don’t have are left out. They appear on the Requests page as they process.` });
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section id="overseerr-import" title="Import from Overseerr / Jellyseerr" subtitle="Migrating in? Pull your existing request history into Arrmada's Requests, then retire the old container. Approved titles still on their way are added to the library and searched a few at a time; pending ones become pending requests here. Finished requests come in as ready only for titles already in your library (import your Radarr and Sonarr library first), and declined requests and ones whose media was deleted from the server are left out — nothing finished or deleted is downloaded again. It's quiet: nobody gets a notification for an old request. Each request is attributed to its Plex requester — a Plex-linked account is created for them, so when they Sign in with Plex they see their own history. Safe to run more than once — anything already requested is skipped.">
      <Field label="Overseerr / Jellyseerr URL">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.50.247:5055" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <Field label="API key">
        <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder="from Overseerr → Settings → General → API Key" className={input} style={inputStyle} autoComplete="off" />
      </Field>
      <div className="flex items-center gap-3">
        <button onClick={run} disabled={busy || !url.trim() || !key.trim()} className="rounded-lg px-4 py-2 text-base font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
          {busy ? "Connecting…" : "Import requests"}
        </button>
        {msg && <span className="text-sm" style={{ color: msg.ok ? "var(--good-text)" : "var(--reject-text)" }}>{msg.text}</span>}
        {msg?.ok && <Link to="/requests" className="flex-none text-sm font-semibold" style={{ color: "var(--accent-text)" }}>Open Requests →</Link>}
      </div>
    </Section>
  );
}
