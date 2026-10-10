import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Note, Section, Toggle, inputStyle } from "../../components/settings/ui";
import { api, type PlexConfig, type PlexTestResult } from "../../lib/api";
import { isAdmin, useMe } from "../../lib/me";
import { plexApi, type PlexConnectResult } from "../../lib/plexApi";
import { usePlexPinSignIn, type PlexFlow } from "../../lib/plexSignIn";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";
import { useToast } from "../../ui";
import { requesterPages } from "./roles";
import { AutoApproveChecks, typesFromCSV, typesToCSV } from "./UsersSettings";

// Settings → Plex: everything about the Plex server in one place — the connection Insights
// records from, and (admins) who may sign in with Plex. Insights' old Settings tab and the
// Plex card that used to sit under Users both live here now.
export function PlexSettings() {
  const { user } = useMe();
  const admin = isAdmin(user);
  return (
    <div className="flex flex-col gap-6">
      <PlexConnection />
      {/* PLEX-04 (Plex scans): the path mapping and last-scan status card goes here,
          as <Section id="plex-scans" …>; register it in sections.ts' search index. */}
      {admin && <PlexSignIn />}
    </div>
  );
}

// PlexConnection points Arrmada at the Plex Media Server: Sign in with Plex (finds the
// server itself), or a URL and token typed in. The token never comes back to the browser.
function PlexConnection() {
  const toast = useToast();
  const [cfg, setCfg] = useState<PlexConfig | null>(null);
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [poll, setPoll] = useState("5");
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [test, setTest] = useState<PlexTestResult | null>(null);
  const [, setParams] = useSearchParams();

  const load = useCallback(() => api.insightsConfig().then(setCfg).catch(() => toast("Could not load Plex settings", { tone: "error" })), [toast]);
  useEffect(() => { void load(); }, [load]);
  const [choices, setChoices] = useState<NonNullable<PlexConnectResult["choices"]>>([]);
  // The connected server's name: from the saved config, refreshed by a passing Test of it.
  const [serverName, setServerName] = useState("");
  useEffect(() => {
    if (!cfg) return;
    setUrl(cfg.url);
    setServerName(cfg.server_name ?? "");
    setPoll(String(cfg.poll_seconds || 5));
    // Until someone chooses, monitoring starts ticked: connecting Plex is for recording.
    setEnabled(cfg.enabled_set ? cfg.enabled : true);
  }, [cfg]);

  // Sign in with Plex: a popup on a computer, the whole page on a phone (plex.tv sends it
  // back here with ?plexpin=). Once approved the server stores the token and finds the
  // owner's server: one that answers is saved; several that answer are offered here.
  const flow = useMemo<PlexFlow<PlexConnectResult>>(() => ({
    kind: "connect",
    start: plexApi.connectStart,
    poll: async (id) => { const r = await plexApi.connectPoll(id); return r.authorized ? r : null; },
  }), []);
  const plex = usePlexPinSignIn({
    flow,
    onDone: (r) => {
      setTest(null);
      setChoices(r.choices ?? []);
      void load();
      if (r.server_name) toast(`Signed in with Plex — connected to ${r.server_name}`, { tone: "good" });
      else if (r.choices?.length) toast("Signed in with Plex — pick your server below", { tone: "good" });
      else toast("Signed in with Plex, but none of your servers answered — enter its URL below.", { tone: "error" });
    },
    onError: (m) => toast(m, { tone: "error" }),
    resumeParam: "plexpin",
    strip: () => setParams((p) => { p.delete("plexpin"); return p; }, { replace: true }),
  });
  const signingIn = plex.phase !== "idle";

  const body = (u = url) => ({ url: u.trim(), token: token.trim() || undefined, enabled, poll_seconds: Number(poll) || 5 });
  const save = async (u?: string) => {
    setBusy("save");
    try { setCfg(await api.updateInsightsConfig(body(u))); setToken(""); setChoices([]); toast("Plex settings saved", { tone: "good" }); }
    catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
  };
  const runTest = async () => {
    setBusy("test"); setTest(null);
    // A passing test of the saved connection refreshes the server name shown above.
    try { const t = await api.testInsights({ url: url.trim() || undefined, token: token.trim() || undefined }); setTest(t); if (t.ok && url.trim() === cfg?.url && !token.trim()) setServerName(t.server_name ?? ""); }
    catch (e) { setTest({ ok: false, error: (e as Error).message }); } finally { setBusy(null); }
  };

  return (
    <Section id="plex-connection" title="Plex connection" subtitle="Point Arrmada at your Plex Media Server. Insights records what's watched from it, and Plex sign-in checks people against it. Your token stays on this server and is never shown back in full.">
      {serverName && cfg?.url && (
        <div className="text-[12.5px]">Connected to <b className="font-semibold">{serverName}</b> <span className="font-mono text-[11px] text-ink-faint">{cfg.url}</span></div>
      )}
      {choices.length > 1 && (
        <div role="group" aria-label="Choose your Plex server" className="flex flex-col gap-2 rounded-lg p-3" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)" }}>
          <div className="text-[12.5px] font-semibold">More than one of your servers answered. Which one should Arrmada use?</div>
          {choices.map((c) => (
            <button key={c.machine_id} onClick={() => { setUrl(c.url); void save(c.url); }} disabled={busy !== null} className="flex flex-wrap items-baseline justify-between gap-2 rounded-lg px-3 py-2 text-left text-[12.5px]" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
              <span className="font-semibold">{c.name}</span>
              <span className="font-mono text-[11px] text-ink-faint">{c.url}</span>
            </button>
          ))}
        </div>
      )}
      <div className="flex flex-col gap-2">
        <button onClick={() => { setTest(null); plex.begin(); }} disabled={signingIn} className="flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-[13px] font-semibold disabled:opacity-60" style={{ background: "#e5a00d", color: "#1f1200" }}>
          {plex.phase === "finishing" ? "Finishing Plex sign-in…" : signingIn ? "Waiting for Plex…" : (cfg?.token_set ? "Re-sign in with Plex" : "Sign in with Plex")}
        </button>
        {(plex.phase === "waiting" || plex.phase === "slow") && (
          <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11.5px]">
            {plex.phase === "slow" && <button onClick={plex.continueHere} className="font-semibold" style={{ color: "var(--accent)" }}>Plex window didn't open? Continue in this tab</button>}
            <button onClick={plex.cancel} className="text-ink-faint underline">Cancel</button>
          </div>
        )}
      </div>
      <div className="flex items-center gap-2 text-[10.5px] text-ink-faint">
        <span className="h-px flex-1" style={{ background: "var(--line)" }} /> or enter manually <span className="h-px flex-1" style={{ background: "var(--line)" }} />
      </div>

      <label className="block">
        <span className="mb-1 block text-[12px] font-semibold">Server URL</span>
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.1.10:32400" className="w-full rounded-lg px-3 py-2 text-[13px]" style={inputStyle} />
      </label>
      <label className="block">
        <span className="mb-1 block text-[12px] font-semibold">X-Plex-Token</span>
        <input value={token} onChange={(e) => setToken(e.target.value)} type="password" autoComplete="off" placeholder={cfg?.token_set ? "•••••••••• (saved — leave blank to keep)" : "paste your token"} className="w-full rounded-lg px-3 py-2 text-[13px]" style={inputStyle} />
        <span className="mt-1 block text-[10.5px] text-ink-faint">In Plex web: play an item → ⋯ → Get Info → View XML — the URL ends with <code>X-Plex-Token=…</code></span>
      </label>

      <div className="flex flex-wrap items-center gap-4">
        <label className="block">
          <span className="mb-1 block text-[12px] font-semibold">Poll interval</span>
          <span className="flex items-center gap-1.5"><input value={poll} onChange={(e) => setPoll(e.target.value)} type="number" min="2" max="60" className="w-[70px] rounded-lg px-2.5 py-1.5 text-[12px]" style={inputStyle} /><span className="text-[11px] text-ink-faint">seconds</span></span>
        </label>
        <label className="flex cursor-pointer items-center gap-2 pt-4 text-[12px]">
          <input type="checkbox" aria-label="Enable monitoring" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span><b className="font-semibold">Enable monitoring</b><span className="block text-[10.5px] text-ink-faint">record activity in the background</span></span>
        </label>
      </div>

      <div className="flex items-center gap-2">
        <button onClick={runTest} disabled={busy !== null || !url.trim()} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{busy === "test" ? "Testing…" : "Test connection"}</button>
        <button onClick={() => void save()} disabled={busy !== null || !url.trim()} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy === "save" ? "Saving…" : "Save"}</button>
      </div>

      {test && (
        <div className="rounded-lg p-3 text-[12px]" style={{ border: `1px solid ${test.ok ? "var(--good)" : "var(--reject)"}`, background: test.ok ? "var(--good-soft)" : "var(--reject-soft)", color: test.ok ? "var(--good)" : "var(--reject)" }}>
          {test.ok ? (
            <div>
              <div className="font-semibold">✓ Connected{test.version ? ` · Plex ${test.version}` : ""}</div>
              {test.libraries && test.libraries.length > 0 && (
                <div className="mt-1 text-ink-dim">Libraries: {test.libraries.map((l) => l.title).join(", ")}</div>
              )}
            </div>
          ) : (
            <div className="font-semibold">✕ {test.error || "Connection failed"}</div>
          )}
        </div>
      )}
    </Section>
  );
}

// PlexSignIn (admin): whether family members may sign in with Plex, and what a new Plex
// sign-in may request without asking. Saved with the page's Save settings button.
function PlexSignIn() {
  const { booksEnabled } = useMe();
  const { s, patch } = useLoadedSettings();
  return (
    <>
      <Section id="plex-sign-in" title="Plex sign-in" subtitle={<>Let your Plex Home members and shared users sign in with Plex — no accounts to hand out. They get a Requester account ({requesterPages(booksEnabled)}), and only people with access to your Plex server get in. Needs the Plex connection above.</>}>
        <Toggle label="Allow Sign in with Plex" hint="Adds a 'Sign in with Plex' button to the login page." checked={s.plex_login_enabled} onChange={(v) => patch({ plex_login_enabled: v })} />
        <div className="flex flex-col gap-1.5">
          <div className="text-[12.5px] font-semibold">Auto-approve new Plex sign-ins' requests</div>
          <AutoApproveChecks
            value={typesFromCSV(s.plex_login_auto_approve_types ?? "movie")}
            onChange={(v) => patch({ plex_login_auto_approve_types: typesToCSV(v) })}
            books={booksEnabled}
            label="New Plex sign-ins auto-approve"
          />
          <p className="m-0 text-[11px] text-ink-faint">Ticked types download straight away for someone who signs in with Plex for the first time; the rest wait for you. A series request can pull every season of a long show, so it starts with movies only. Existing accounts keep their own settings — change them in Settings → Users.</p>
        </div>
        <Toggle
          label="Let staff sign in with Plex"
          hint="Sign in with Plex may open an admin or manager account linked to that Plex account. Your own first Plex sign-in then links to your admin account, if it's the only one. Off: staff sign in with their password."
          checked={!!s.plex_signin_staff}
          onChange={(v) => patch({ plex_signin_staff: v })}
        />
        {s.plex_signin_staff && (
          <Note tone="warn">Anyone who controls a linked Plex account gets that account — for an admin, everything in Arrmada. Keep the Plex account behind two-factor sign-in at plex.tv.</Note>
        )}
      </Section>
      <SaveBar />
    </>
  );
}
