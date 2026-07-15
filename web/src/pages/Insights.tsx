import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type PlexConfig, type PlexTestResult } from "../lib/api";

// Insights — Arrmada's Plex watch-monitoring module (Tautulli replacement). Built in slices
// (see INSIGHTS-PLAN.md). I0 (this): the Plex connection — configure + test. Activity, History,
// Users, Graphs and the Reliability/buffering view land in later slices and show as "coming soon".
type Tab = "activity" | "history" | "users" | "graphs" | "reliability" | "settings";
const TABS: { key: Tab; label: string; soon?: boolean }[] = [
  { key: "activity", label: "Activity", soon: true },
  { key: "history", label: "History", soon: true },
  { key: "users", label: "Users", soon: true },
  { key: "graphs", label: "Graphs", soon: true },
  { key: "reliability", label: "Reliability", soon: true },
  { key: "settings", label: "Settings" },
];

export function Insights() {
  const [tab, setTab] = useState<Tab>("settings");
  const [cfg, setCfg] = useState<PlexConfig | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  useEffect(() => { api.insightsConfig().then(setCfg).catch(() => flash("Could not load Plex settings")); }, []);
  const connected = cfg?.token_set && !!cfg?.url;

  return (
    <>
      <PageHeader title="Insights" crumb="Services / Insights" />
      <div className="mx-auto w-full max-w-[1240px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[64ch] text-[12.5px] text-ink-dim">Watch monitoring for your Plex server — who's streaming what, right now and historically, with stream quality, transcode diagnostics and buffering reliability. Connect your server in <b>Settings</b> to begin.</p>
          <span className="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-[12px] font-semibold" style={{ border: `1px solid ${connected ? "var(--good)" : "var(--avoid)"}`, background: connected ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--avoid-soft)" }}>
            <span className="h-2 w-2 rounded-full" style={{ background: connected ? "var(--good)" : "var(--avoid)" }} />
            {connected ? "Plex connected" : "Not connected"}
          </span>
        </div>

        {/* Tabs */}
        <div className="mb-5 flex gap-1 border-b" style={{ borderColor: "var(--line)" }}>
          {TABS.map((t) => {
            const active = tab === t.key;
            return (
              <button key={t.key} onClick={() => setTab(t.key)} className="relative px-4 py-2.5 text-[13.5px] font-semibold transition-colors" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                {t.label}
                {active && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
              </button>
            );
          })}
        </div>

        {tab === "settings" ? (
          <PlexSettings cfg={cfg} onSaved={setCfg} flash={flash} />
        ) : (
          <ComingSoon tab={tab} connected={!!connected} onConfigure={() => setTab("settings")} />
        )}
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

const NEXT: Record<string, string> = {
  activity: "Live now-playing — who's streaming what, on which device, with progress, transcode decision, bandwidth and geolocation.",
  history: "Every play recorded — a filterable table with stream-type, geolocated IP and a click-through deep-dive.",
  users: "Per-user activity — last seen, platform, total plays and watch time.",
  graphs: "Plays by day, hour, platform and user, plus bandwidth over time.",
  reliability: "The buffering view — see historically when and where streams choked, by user, platform and title.",
};

function ComingSoon({ tab, connected, onConfigure }: { tab: string; connected: boolean; onConfigure: () => void }) {
  return (
    <div className="rounded-xl p-10 text-center" style={{ border: "1px dashed var(--line)", background: "var(--panel)" }}>
      <div className="text-[13.5px] font-bold capitalize">{tab}</div>
      <p className="mx-auto mt-1.5 max-w-[52ch] text-[12px] text-ink-dim">{NEXT[tab]}</p>
      <div className="mt-3 inline-flex items-center gap-2 rounded-full px-3 py-1 font-mono text-[10px] font-bold uppercase tracking-wide" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>Coming soon</div>
      {!connected && <div className="mt-4"><button onClick={onConfigure} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Connect your Plex server →</button></div>}
    </div>
  );
}

const inp = "w-full rounded-lg px-3 py-2 text-[13px]";
const inpStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

function PlexSettings({ cfg, onSaved, flash }: { cfg: PlexConfig | null; onSaved: (c: PlexConfig) => void; flash: (m: string) => void }) {
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [poll, setPoll] = useState("5");
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [test, setTest] = useState<PlexTestResult | null>(null);

  useEffect(() => {
    if (!cfg) return;
    setUrl(cfg.url);
    setPoll(String(cfg.poll_seconds || 5));
    setEnabled(cfg.enabled);
  }, [cfg]);

  const body = () => ({ url: url.trim(), token: token.trim() || undefined, enabled, poll_seconds: Number(poll) || 5 });

  const save = async () => {
    setBusy("save");
    try { const c = await api.updateInsightsConfig(body()); onSaved(c); setToken(""); flash("Plex settings saved"); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const runTest = async () => {
    setBusy("test"); setTest(null);
    try { setTest(await api.testInsights({ url: url.trim() || undefined, token: token.trim() || undefined })); }
    catch (e) { setTest({ ok: false, error: (e as Error).message }); } finally { setBusy(null); }
  };

  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: "minmax(0,1fr)" }}>
      <div className="rounded-xl p-5" style={{ border: "1px solid var(--line)", background: "var(--panel)", maxWidth: 560 }}>
        <div className="text-[13.5px] font-bold">Plex connection</div>
        <div className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">Point Arrmada at your Plex Media Server. Your token stays on this server and is never shown back in full.</div>

        <label className="mb-3 block">
          <span className="mb-1 block text-[12px] font-semibold">Server URL</span>
          <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.1.10:32400" className={inp} style={inpStyle} />
        </label>

        <label className="mb-3 block">
          <span className="mb-1 block text-[12px] font-semibold">X-Plex-Token</span>
          <input value={token} onChange={(e) => setToken(e.target.value)} type="password" placeholder={cfg?.token_set ? "•••••••••• (saved — leave blank to keep)" : "paste your token"} className={inp} style={inpStyle} />
          <span className="mt-1 block text-[10.5px] text-ink-faint">In Plex web: play an item → ⋯ → Get Info → View XML — the URL ends with <code>X-Plex-Token=…</code></span>
        </label>

        <div className="mb-4 flex items-center gap-4">
          <label className="block">
            <span className="mb-1 block text-[12px] font-semibold">Poll interval</span>
            <span className="flex items-center gap-1.5"><input value={poll} onChange={(e) => setPoll(e.target.value)} type="number" min="2" max="60" className="w-[70px] rounded-lg px-2.5 py-1.5 text-[12px]" style={inpStyle} /><span className="text-[11px] text-ink-faint">seconds</span></span>
          </label>
          <label className="flex cursor-pointer items-center gap-2 pt-4 text-[12px]">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            <span><b className="font-semibold">Enable monitoring</b><span className="block text-[10.5px] text-ink-faint">record activity in the background</span></span>
          </label>
        </div>

        <div className="flex items-center gap-2">
          <button onClick={runTest} disabled={busy !== null || !url.trim()} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{busy === "test" ? "Testing…" : "Test connection"}</button>
          <button onClick={save} disabled={busy !== null || !url.trim()} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy === "save" ? "Saving…" : "Save"}</button>
        </div>

        {test && (
          <div className="mt-4 rounded-lg p-3 text-[12px]" style={{ border: `1px solid ${test.ok ? "var(--good)" : "var(--reject)"}`, background: test.ok ? "var(--good-soft, rgba(127,176,105,.12))" : "var(--reject-soft)", color: test.ok ? "var(--good)" : "var(--reject)" }}>
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
      </div>
    </div>
  );
}
