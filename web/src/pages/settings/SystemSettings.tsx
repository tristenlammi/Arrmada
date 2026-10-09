import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Section, Toggle } from "../../components/settings/ui";
import { api, type APIKeyStatus, type PendingRestart } from "../../lib/api";
import { useMe } from "../../lib/me";
import { busyLines, restartAppAndWait } from "../../lib/restart";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";
import { Backups } from "./Backups";

// Settings → System (admin only): which modules are on, the API keys, restarting Arrmada
// and the database backups.
export function SystemSettings() {
  const { s, patch, syncRegion } = useLoadedSettings();
  return (
    <div className="flex flex-col gap-6">
      <Section id="modules" title="Modules" subtitle="Turn modules on or off. Disabling hides a module from the navigation and from Discover — nothing is deleted, and it can be re-enabled anytime.">
        <Toggle label="Books" hint="Ebook and audiobook library, and the Books tab in Discover. Metadata comes from Hardcover when a key is set, otherwise Open Library." checked={s.books_enabled} onChange={(v) => patch({ books_enabled: v })} />
        <Toggle label="Music (preview)" hint="Artists and albums from MusicBrainz with automatic album downloads. Still being hardened. Turning it off hides Music and stops its searches and imports; finished downloads wait until it's back on. Nothing is deleted." checked={s.music_enabled} onChange={(v) => patch({ music_enabled: v })} />
      </Section>
      <SaveBar />
      <APIKeysSection onRegionSaved={syncRegion} />
      <RestartSection />
      <Backups />
    </div>
  );
}

// RestartSection restarts Arrmada on demand — the same restart the folder banner offers,
// for any change that only applies at startup — and points at the log.
function RestartSection() {
  const [st, setSt] = useState<PendingRestart | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = () => { api.pendingRestart().then(setSt).catch(() => setSt(null)); };
  useEffect(load, []);

  // Fresh numbers for the confirm: the page may have been open for hours.
  const openConfirm = () => { setErr(null); load(); setConfirming(true); };
  const restart = async () => {
    setRestarting(true); setErr(null);
    try { await restartAppAndWait(); } catch (e) { setErr((e as Error).message); setRestarting(false); }
  };
  const lines = st ? busyLines(st.busy) : [];

  return (
    <Section id="restart" title="Restart" subtitle="Restart Arrmada, for example after a change that only applies at startup. This page reloads once it's back, usually in a few seconds.">
      {st && !st.can_restart && (
        <p className="m-0 text-[12px] text-ink-dim">Arrmada can't restart itself here. Restart the Arrmada container the way you started it.</p>
      )}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        {st?.can_restart && (
          <button onClick={openConfirm} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Restart now</button>
        )}
        <Link to="/logs" className="text-[12px] font-semibold" style={{ color: "var(--accent)" }}>Logs →</Link>
      </div>
      {confirming && (
        <ConfirmDialog
          title="Restart Arrmada now?"
          tone="accent"
          body={
            <div className="flex flex-col gap-1.5">
              <span>This page reloads once Arrmada is back, usually in a few seconds.</span>
              {lines.length > 0
                ? lines.map((l) => <span key={l} style={{ color: "var(--avoid)" }}>{l}</span>)
                : <span>Nothing is converting or making subtitles right now.</span>}
            </div>
          }
          confirmLabel="Restart now"
          busyLabel="Restarting…"
          busy={restarting}
          error={err}
          onConfirm={restart}
          onCancel={() => setConfirming(false)}
        />
      )}
    </Section>
  );
}

// What stops working when a key is cleared and nothing takes its place, for the confirm.
const KEY_CLEAR_EFFECT: Record<string, string> = {
  tmdb: "Discover, Movies and TV stop finding anything.",
  tvdb: "Anime episode numbering goes back to TMDB's.",
  omdb: "IMDb, Rotten Tomatoes and Metacritic scores stop showing.",
  hardcover: "New book lookups go back to Open Library. Books already matched keep what they have.",
  opensubtitles_api: "Subtitle search stops.",
  opensubtitles_username: "Subtitle downloads stop.",
  opensubtitles_password: "Subtitle downloads stop.",
};

function APIKeysSection({ onRegionSaved }: { onRegionSaved: (region: string) => void }) {
  const { setMetadataReady } = useMe();
  const [keys, setKeys] = useState<APIKeyStatus[] | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  // The key whose Clear is waiting on the confirm, and the server's answer if it said no.
  const [clearing, setClearing] = useState<APIKeyStatus | null>(null);
  const [clearErr, setClearErr] = useState<string | null>(null);
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
    // A blank Save never reaches the server: clearing a key is Clear's job.
    const value = drafts[id]?.trim();
    if (!value) return;
    setBusy(id); setErr(null);
    try {
      const next = await api.setAPIKey(id, value);
      setKeys(next);
      // The key works the moment it's saved, so let Movies and Discover know without a reload.
      if (id === "tmdb") setMetadataReady(!!next.find((k) => k.id === "tmdb")?.configured);
      setDrafts((d) => { const n = { ...d }; delete n[id]; return n; }); // clear the field on success
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  // Clear goes straight to the DELETE, not through the draft, so a value typed in the
  // field is never saved by pressing Clear.
  const clearKey = async (k: APIKeyStatus) => {
    setBusy("clear:" + k.id); setClearErr(null);
    try {
      setKeys(await api.clearAPIKey(k.id));
      setDrafts((d) => { const n = { ...d }; delete n[k.id]; return n; });
      setTests((t) => { const n = { ...t }; delete n[k.id]; return n; });
      setClearing(null);
    } catch (e) {
      setClearErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  return (
    <Section id="api-keys" title="API keys" subtitle="External services Arrmada can use. A key entered here takes effect immediately — no restart — and overrides any set at install. The saved value is never shown back to you; only whether it's set.">
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
                disabled={busy !== null || !drafts[k.id]?.trim()}
                className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50"
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
                  onClick={() => { setClearErr(null); setClearing(k); }}
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
      <div id="discovery-region" className="flex scroll-mt-20 flex-col gap-1.5 rounded-lg p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
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
      {clearing && (
        <ConfirmDialog
          title={<>Clear the saved {clearing.label} key?</>}
          body={
            <p className="m-0">
              {clearing.env_set
                ? `The key from your install${clearing.env_hint ? ` (${clearing.env_hint})` : ""} will be used instead.`
                : KEY_CLEAR_EFFECT[clearing.id] ?? clearing.purpose}
            </p>
          }
          confirmLabel="Clear key"
          busyLabel="Clearing…"
          busy={busy === "clear:" + clearing.id}
          error={clearErr}
          onConfirm={() => clearKey(clearing)}
          onCancel={() => setClearing(null)}
        />
      )}
    </Section>
  );
}
