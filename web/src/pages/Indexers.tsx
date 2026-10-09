import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type Indexer } from "../lib/api";
import { useQuery } from "../lib/query";
import { ErrorState, Skeleton, StaleBanner, useConfirm } from "../ui";

const NO_INDEXERS: Indexer[] = [];

type TestState = { loading?: boolean; ok?: boolean; error?: string };

// Priority only orders results that are otherwise level, so say so wherever it shows.
const TIE_BREAK_HELP = "Only breaks ties between equally seeded results, such as the same release from two indexers. 1 = preferred.";

export function Indexers() {
  const q = useQuery("indexers", () => api.indexers(), { staleMs: 0 });
  const list = q.data ?? NO_INDEXERS;
  const error = q.error?.message ?? null;
  const [tests, setTests] = useState<Record<number, TestState>>({});
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [deleteErr, setDeleteErr] = useState<Record<number, string>>({});
  const confirm = useConfirm();

  const refresh = q.refetch;

  const runTest = async (id: number) => {
    setTests((t) => ({ ...t, [id]: { loading: true } }));
    try {
      const res = await api.testIndexer(id);
      setTests((t) => ({ ...t, [id]: { ok: res.ok, error: res.error } }));
    } catch (e) {
      setTests((t) => ({ ...t, [id]: { ok: false, error: (e as Error).message } }));
    }
  };

  // Deleting asks first and names what happens to downloads already grabbed through it;
  // a refusal from the server shows on the row instead of vanishing.
  const remove = async (idx: Indexer) => {
    const ok = await confirm({
      title: `Delete ${idx.name}?`,
      body: "Searches stop using it. Seed rules on downloads already grabbed through it fall back to the 14-day default.",
      confirmLabel: "Delete",
      tone: "danger",
    });
    if (!ok) return;
    setDeleteErr((d) => { const n = { ...d }; delete n[idx.id]; return n; });
    try {
      await api.deleteIndexer(idx.id);
      refresh();
    } catch (e) {
      setDeleteErr((d) => ({ ...d, [idx.id]: (e as Error).message }));
    }
  };

  return (
    <>
      <PageHeader title="Indexers" />
      <div className="mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between">
          <p className="m-0 text-[12.5px] text-ink-dim">
            Torznab (torrent) and Newznab (usenet) search sources. Every module searches through these.
          </p>
          <button
            onClick={() => setShowForm((s) => !s)}
            className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            {showForm ? "Cancel" : "+ Add indexer"}
          </button>
        </div>

        {showForm && (
          <AddForm
            onAdded={() => {
              setShowForm(false);
              refresh();
            }}
          />
        )}

        <ProwlarrSync onSynced={refresh} />

        {q.data && error && <StaleBanner message={error} onRetry={refresh} />}

        {!q.data ? (
          error ? <ErrorState what="indexers" message={error} onRetry={refresh} busy={q.loading} /> : <Skeleton variant="list" count={3} />
        ) : list.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No indexers yet. Add one to start searching.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {list.map((idx) => {
              const t = tests[idx.id];
              return (
                <div key={idx.id} className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                  <div className="flex flex-wrap items-center gap-3">
                    <div className="min-w-0 flex-1 basis-[180px]">
                      <div className="flex items-center gap-2">
                        <span className="text-[13.5px] font-semibold">{idx.name}</span>
                        <span
                          className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase"
                          style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}
                        >
                          {idx.kind}
                        </span>
                        {!idx.enabled && (
                          <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px]" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>
                            disabled
                          </span>
                        )}
                      </div>
                      <div className="mt-1 truncate font-mono text-[11px] text-ink-faint">
                        {idx.url || (idx.username ? `@${idx.username}` : "")}
                      </div>
                    </div>
                    <span className="font-mono text-[11px] text-ink-faint" title={TIE_BREAK_HELP}>tie-break {idx.priority}</span>
                    <button onClick={() => setEditingId(editingId === idx.id ? null : idx.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                      {editingId === idx.id ? "Close" : "Edit"}
                    </button>
                    <button onClick={() => runTest(idx.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                      {t?.loading ? "Testing…" : "Test"}
                    </button>
                    <button onClick={() => remove(idx)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>
                      Delete
                    </button>
                  </div>
                  {deleteErr[idx.id] && (
                    <div className="mt-2.5 text-[12px]" style={{ color: "var(--reject)" }}>Couldn't delete: {deleteErr[idx.id]}</div>
                  )}
                  <MediaPills idx={idx} onChange={refresh} />
                  {t && !t.loading && (
                    <div className="mt-2.5 font-mono text-[11px]" style={{ color: t.ok ? "var(--good)" : "var(--reject)" }}>
                      {t.ok ? "✓ Connected" : `✕ ${t.error ?? "failed"}`}
                    </div>
                  )}
                  {editingId === idx.id && (
                    <EditForm
                      idx={idx}
                      onSaved={() => {
                        setEditingId(null);
                        refresh();
                      }}
                    />
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}

function ProwlarrSync({ onSynced }: { onSynced: () => void }) {
  const [open, setOpen] = useState(false);
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [hasKey, setHasKey] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null);

  useEffect(() => {
    api.prowlarrInfo().then((i) => { setUrl(i.url); setHasKey(i.has_key); }).catch(() => {});
  }, []);

  const sync = async () => {
    setBusy(true);
    setResult(null);
    try {
      const r = await api.syncProwlarr({ url, api_key: apiKey });
      const fs = r.flaresolverr_ready ? " FlareSolverr is auto-configured for Cloudflare trackers." : "";
      setResult({ ok: true, msg: `Synced ${r.synced} indexer${r.synced === 1 ? "" : "s"} from Prowlarr.${fs}` });
      setHasKey(true);
      setApiKey("");
      onSynced();
    } catch (e) {
      setResult({ ok: false, msg: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  const field = "w-full rounded-lg px-3 py-2 text-[13px]";
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
  // Prowlarr's own UI — reachable from the user's browser (same host, port 9696),
  // not the internal service name Arrmada uses to talk to it.
  const prowlarrUI = `${window.location.protocol}//${window.location.hostname}:9696`;

  return (
    <div className="mb-4 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <button onClick={() => setOpen((o) => !o)} className="flex w-full items-center justify-between gap-3 text-left">
        <div>
          <div className="text-[13px] font-semibold">Sync from Prowlarr <span className="ml-1 rounded px-1.5 py-0.5 align-middle font-mono text-[9px] uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>fast</span></div>
          <div className="mt-0.5 text-[11.5px] text-ink-faint">Pull your Prowlarr indexers in as Torznab feeds — API search, no scraping. FlareSolverr (bundled) is wired into Prowlarr automatically. Add trackers in Prowlarr first.</div>
        </div>
        <span className="font-mono text-[16px] text-ink-faint">{open ? "−" : "+"}</span>
      </button>
      {open && (
        <div className="mt-3.5 border-t pt-3.5" style={{ borderColor: "var(--line)" }}>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Prowlarr URL</span>
              <input className={field} style={fieldStyle} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://arrmada-prowlarr:9696" />
            </label>
            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">API key {hasKey && <span className="text-[8.5px] normal-case" style={{ color: "var(--good)" }}>· saved</span>}</span>
              <input type="password" className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={hasKey ? "•••••••• (leave blank to reuse)" : "Prowlarr → Settings → General"} autoComplete="off" />
            </label>
          </div>
          <p className="mt-2.5 text-[10.5px] text-ink-faint">
            The URL must be reachable from the Arrmada server. With the bundled Prowlarr (<code className="font-mono">docker compose --profile prowlarr up -d</code>) the default above works.
          </p>

          <div className="mt-3 flex flex-col gap-1.5 rounded-lg p-3 text-[11.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            <div>
              <span className="text-ink-faint">Open Prowlarr to add trackers:</span>{" "}
              <a href={prowlarrUI} target="_blank" rel="noreferrer" className="font-mono font-semibold" style={{ color: "var(--accent)" }}>{prowlarrUI} ↗</a>
            </div>
            <div className="text-ink-dim">
              For a Cloudflare-protected tracker, FlareSolverr is already wired up — just add the{" "}
              <code className="rounded px-1 py-0.5 font-mono text-[10.5px]" style={{ background: "var(--panel)", color: "var(--accent)" }}>flaresolverr</code>{" "}
              tag to that tracker in Prowlarr. Public indexers don't need it.
            </div>
          </div>

          {result && (
            <div className="mt-2.5 text-[12px]" style={{ color: result.ok ? "var(--good)" : "var(--reject)" }}>
              {result.ok ? "✓ " : "✕ "}{result.msg}
            </div>
          )}
          <button onClick={sync} disabled={busy} className="mt-3 rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: busy ? 0.6 : 1 }}>
            {busy ? "Syncing…" : "Sync now"}
          </button>
        </div>
      )}
    </div>
  );
}

// The name a new indexer gets from its kind, until the user types their own.
const KIND_NAMES: Record<string, string> = {
  "1337x": "1337x",
  torrentleech: "TorrentLeech",
  myanonamouse: "MyAnonaMouse",
  torznab: "Torznab",
  newznab: "Newznab",
};

function AddForm({ onAdded }: { onAdded: () => void }) {
  const [name, setName] = useState(KIND_NAMES["1337x"]);
  const [nameTouched, setNameTouched] = useState(false);
  const [kind, setKind] = useState("1337x");
  const changeKind = (k: string) => {
    setKind(k);
    if (!nameTouched) setName(KIND_NAMES[k] ?? k);
  };
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [priority, setPriority] = useState(25);
  const [minSeeders, setMinSeeders] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const isTL = kind === "torrentleech";
  const is1337 = kind === "1337x";
  const isMAM = kind === "myanonamouse";

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      const body = isTL
        ? { name, kind, username, password, api_key: apiKey, priority, min_seeders: minSeeders }
        : isMAM
          ? { name, kind, api_key: apiKey, media_types: ["book"], priority, min_seeders: minSeeders }
          : is1337
            ? { name, kind, url, priority, min_seeders: minSeeders }
            : { name, kind, url, api_key: apiKey, priority, min_seeders: minSeeders };
      await api.createIndexer(body);
      onAdded();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const field = "w-full rounded-lg px-3 py-2 text-[13px]";
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

  return (
    <form onSubmit={submit} className="mb-4 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Labeled label="Name">
          <input className={field} style={fieldStyle} value={name} onChange={(e) => { setName(e.target.value); setNameTouched(true); }} required />
        </Labeled>
        <Labeled label="Kind">
          <select className={field} style={fieldStyle} value={kind} onChange={(e) => changeKind(e.target.value)}>
            <option value="1337x">1337x (public torrents)</option>
            <option value="torrentleech">TorrentLeech (native)</option>
            <option value="myanonamouse">MyAnonaMouse (native, books)</option>
            <option value="torznab">Torznab (torrent, via Prowlarr/Jackett)</option>
            <option value="newznab">Newznab (usenet)</option>
          </select>
        </Labeled>

        {isMAM ? (
          <Labeled label="mam_id session" span2>
            <input className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="paste the mam_id session string from MyAnonaMouse" autoComplete="off" required />
          </Labeled>
        ) : isTL ? (
          <>
            <Labeled label="Username">
              <input className={field} style={fieldStyle} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" required />
            </Labeled>
            <Labeled label="Password">
              <input type="password" className={field} style={fieldStyle} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
            </Labeled>
            <Labeled label="RSS key (optional)" span2>
              <input className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="optional; FlareSolverr handles auth now" />
            </Labeled>
          </>
        ) : is1337 ? (
          <Labeled label="Site URL (optional mirror)" span2>
            <input className={field} style={fieldStyle} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://1337x.to (leave blank for default)" />
          </Labeled>
        ) : (
          <>
            <Labeled label="API URL" span2>
              <input className={field} style={fieldStyle} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://tracker.example/api" required />
            </Labeled>
            <Labeled label="API key">
              <input className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="••••••••" />
            </Labeled>
          </>
        )}

        <Labeled label="Tie-break priority (1–50)" hint={TIE_BREAK_HELP}>
          <input type="number" min={1} max={50} className={field} style={fieldStyle} value={priority} onChange={(e) => setPriority(Number(e.target.value))} />
        </Labeled>
        <Labeled label="Min seeders (0 = off)">
          <input type="number" min={0} className={field} style={fieldStyle} value={minSeeders} onChange={(e) => setMinSeeders(Math.max(0, Number(e.target.value)))} />
        </Labeled>
      </div>

      {is1337 && (
        <p className="mt-3 text-[11px] text-ink-faint">
          Public tracker — no login, no ratio. Uses FlareSolverr to get past Cloudflare and hands qBittorrent a magnet, so there’s nothing to seed. Great for testing.
        </p>
      )}
      {isTL && (
        <p className="mt-3 text-[11px] text-ink-faint">
          Native — no Prowlarr/Jackett needed. Credentials stored on your server. Uses FlareSolverr for Cloudflare. 2FA not supported yet.
        </p>
      )}
      {isMAM && (
        <p className="mt-3 text-[11px] text-ink-faint">
          Native books/audiobooks integration. In MyAnonaMouse go to <b>Preferences → Security → Create session</b>, allow your server's IP (ASN-locked is fine), and paste the <span className="font-mono">mam_id</span> here. Pulls full metadata — narrator, author, series, language, format — straight from MAM's API. Scoped to Books automatically. If you already have MAM in Prowlarr, remove that entry to avoid duplicate results.
        </p>
      )}
      {error && <div className="mt-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
      <button
        type="submit"
        disabled={saving}
        className="mt-3.5 rounded-lg px-4 py-2 text-[12.5px] font-semibold"
        style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: saving ? 0.6 : 1 }}
      >
        {saving ? "Saving…" : "Save indexer"}
      </button>
    </form>
  );
}

function EditForm({ idx, onSaved }: { idx: Indexer; onSaved: () => void }) {
  const isTL = idx.kind === "torrentleech";
  const is1337 = idx.kind === "1337x";
  const isMAM = idx.kind === "myanonamouse";
  const [name, setName] = useState(idx.name);
  const [url, setUrl] = useState(idx.url ?? "");
  const [username, setUsername] = useState(idx.username ?? "");
  const [password, setPassword] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [priority, setPriority] = useState(idx.priority);
  const [minSeeders, setMinSeeders] = useState(idx.min_seeders ?? 0);
  const initHours = idx.seed_hours ?? 0;
  const initRatio = idx.seed_ratio ?? 0;
  const [seedEnabled, setSeedEnabled] = useState(idx.seed_enabled ?? true);
  const [seedRatio, setSeedRatio] = useState(initRatio || 0);
  const [seedTime, setSeedTime] = useState(initHours % 24 === 0 && initHours > 0 ? initHours / 24 : initHours);
  const [seedUnit, setSeedUnit] = useState<"hours" | "days">(initHours % 24 === 0 && initHours > 0 ? "days" : "hours");
  const [enabled, setEnabled] = useState(idx.enabled);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      await api.updateIndexer(idx.id, {
        name,
        kind: idx.kind,
        url: isTL ? undefined : url,
        username: isTL ? username : undefined,
        password: isTL ? password : undefined,
        api_key: apiKey, // blank = keep existing
        categories: idx.categories,
        media_types: idx.media_types, // scoping is edited via the row pills; preserve it here
        priority,
        min_seeders: minSeeders,
        seed_enabled: seedEnabled,
        seed_ratio: seedEnabled ? seedRatio : 0,
        seed_hours: seedEnabled ? (seedUnit === "days" ? seedTime * 24 : seedTime) : 0,
        enabled,
      });
      onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const field = "w-full rounded-lg px-3 py-2 text-[13px]";
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

  return (
    <form onSubmit={submit} className="mt-3 rounded-lg p-3.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Labeled label="Name">
          <input className={field} style={fieldStyle} value={name} onChange={(e) => setName(e.target.value)} required />
        </Labeled>
        <Labeled label="Tie-break priority (1–50)" hint={TIE_BREAK_HELP}>
          <input type="number" min={1} max={50} className={field} style={fieldStyle} value={priority} onChange={(e) => setPriority(Number(e.target.value))} />
        </Labeled>
        <Labeled label="Min seeders (0 = off)">
          <input type="number" min={0} className={field} style={fieldStyle} value={minSeeders} onChange={(e) => setMinSeeders(Math.max(0, Number(e.target.value)))} />
        </Labeled>
        {isTL ? (
          <>
            <Labeled label="Username">
              <input className={field} style={fieldStyle} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
            </Labeled>
            <Labeled label="Password (blank = keep)">
              <input type="password" className={field} style={fieldStyle} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
            </Labeled>
            <Labeled label="RSS key (blank = keep)" span2>
              <input className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="leave blank to keep current" />
            </Labeled>
          </>
        ) : is1337 ? (
          <Labeled label="Site URL (optional mirror)" span2>
            <input className={field} style={fieldStyle} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://1337x.to" />
          </Labeled>
        ) : isMAM ? (
          <Labeled label="mam_id session (blank = keep)" span2>
            <input className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="paste a fresh mam_id to replace the stored session" autoComplete="off" />
          </Labeled>
        ) : (
          <>
            <Labeled label="API URL" span2>
              <input className={field} style={fieldStyle} value={url} onChange={(e) => setUrl(e.target.value)} required />
            </Labeled>
            <Labeled label="API key (blank = keep)">
              <input className={field} style={fieldStyle} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="leave blank to keep current" />
            </Labeled>
          </>
        )}
      </div>

      <SeedingRules enabled={seedEnabled} ratio={seedRatio} time={seedTime} unit={seedUnit} onEnabled={setSeedEnabled} onRatio={setSeedRatio} onTime={setSeedTime} onUnit={setSeedUnit} />

      <label className="mt-3 flex items-center gap-2 text-[12.5px]">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Enabled
      </label>
      {error && <div className="mt-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
      <button
        type="submit"
        disabled={saving}
        className="mt-3 rounded-lg px-4 py-2 text-[12.5px] font-semibold"
        style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: saving ? 0.6 : 1 }}
      >
        {saving ? "Saving…" : "Save changes"}
      </button>
    </form>
  );
}

function ToggleSwitch({ checked, onChange }: { checked: boolean; onChange: () => void }) {
  return (
    <button type="button" role="switch" aria-checked={checked} onClick={onChange} className="relative inline-block h-[22px] w-[38px] flex-none rounded-full transition-colors" style={{ background: checked ? "var(--accent)" : "var(--line)" }}>
      <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: checked ? "19px" : "3px" }} />
    </button>
  );
}

function Segmented<T extends string>({ value, options, onChange }: { value: T; options: { v: T; l: string }[]; onChange: (v: T) => void }) {
  return (
    <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      {options.map((o) => (
        <button key={o.v} type="button" onClick={() => onChange(o.v)} className="rounded-md px-3 py-1 text-[11.5px] font-semibold transition-colors" style={{ background: value === o.v ? "var(--accent)" : "transparent", color: value === o.v ? "var(--accent-ink)" : "var(--ink-faint)" }}>
          {o.l}
        </button>
      ))}
    </div>
  );
}

function SeedingRules({ enabled, ratio, time, unit, onEnabled, onRatio, onTime, onUnit }: {
  enabled: boolean; ratio: number; time: number; unit: "hours" | "days";
  onEnabled: (v: boolean) => void; onRatio: (v: number) => void; onTime: (v: number) => void; onUnit: (v: "hours" | "days") => void;
}) {
  const field = "w-full rounded-lg px-3 py-2 text-[13px]";
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
  return (
    <div className="mt-3 rounded-lg p-3" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="flex items-center justify-between">
        <div>
          <div className="text-[12.5px] font-semibold">Seed rules</div>
          <div className="text-[10.5px] text-ink-faint">{enabled ? "Keep seeding until a ratio or time limit" : "Off — remove the download as soon as it's imported"}</div>
        </div>
        <ToggleSwitch checked={enabled} onChange={() => onEnabled(!enabled)} />
      </div>
      {enabled && (
        <>
          <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Labeled label="Seed ratio (0 = none)">
              <input type="number" min={0} step={0.1} className={field} style={fieldStyle} value={ratio} onChange={(e) => onRatio(Math.max(0, Number(e.target.value)))} placeholder="e.g. 2.5" />
            </Labeled>
            <Labeled label="Or after time (0 = none)">
              <div className="flex items-center gap-2">
                <input type="number" min={0} className={field} style={fieldStyle} value={time} onChange={(e) => onTime(Math.max(0, Number(e.target.value)))} />
                <Segmented value={unit} options={[{ v: "hours", l: "Hours" }, { v: "days", l: "Days" }]} onChange={onUnit} />
              </div>
            </Labeled>
          </div>
          <p className="mt-2 text-[10.5px] text-ink-faint">Removed when either limit is hit (both 0 = seed forever). The library keeps its own copy, so removing the torrent never touches your media.</p>
        </>
      )}
    </div>
  );
}

function Labeled({ label, hint, span2, children }: { label: string; hint?: string; span2?: boolean; children: React.ReactNode }) {
  return (
    <label className={`flex flex-col gap-1.5 ${span2 ? "sm:col-span-2" : ""}`}>
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{label}</span>
      {children}
      {hint && <span className="text-[10.5px] text-ink-faint">{hint}</span>}
    </label>
  );
}

const MEDIA_TYPES = [
  { key: "movie", label: "Movies" },
  { key: "series", label: "TV" },
  { key: "book", label: "Books" },
  { key: "music", label: "Music" },
] as const;
const ALL_MEDIA = MEDIA_TYPES.map((m) => m.key as string);

// MediaPills scopes an indexer to specific areas. A lit pill means "searched for
// this area": an unscoped indexer (empty media_types) is used for everything, so
// every pill is lit. Clicking a lit pill excludes that area; lighting them all
// again stores [] ("all areas"). The last lit pill can't be turned off — that
// would be an indexer used for nothing, which is what disabling it is for.
function MediaPills({ idx, onChange }: { idx: Indexer; onChange: () => void | Promise<void> }) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The optimistic choice while a save is out; null = show what the server has.
  const [pending, setPending] = useState<string[] | null>(null);
  const scoped = Boolean(idx.media_types && idx.media_types.length > 0);
  const saved = scoped ? ALL_MEDIA.filter((k) => (idx.media_types as string[]).includes(k)) : ALL_MEDIA;
  const lit = pending ?? saved;
  const allLit = lit.length === ALL_MEDIA.length;

  const toggle = async (key: string) => {
    if (lit.includes(key) && lit.length === 1) {
      setError("An indexer has to search for something. Disable the indexer instead.");
      return;
    }
    const next = lit.includes(key) ? lit.filter((k) => k !== key) : [...lit, key];
    const ordered = ALL_MEDIA.filter((k) => next.includes(k));
    const media_types = ordered.length === ALL_MEDIA.length ? [] : ordered; // every area = unscoped
    setPending(ordered);
    setError(null);
    setSaving(true);
    try {
      await api.updateIndexer(idx.id, {
        name: idx.name,
        kind: idx.kind,
        url: idx.url,
        username: idx.username,
        categories: idx.categories,
        media_types,
        priority: idx.priority,
        min_seeders: idx.min_seeders,
        seed_enabled: idx.seed_enabled,
        seed_ratio: idx.seed_ratio,
        seed_hours: idx.seed_hours,
        enabled: idx.enabled,
      });
      await onChange();
    } catch (e) {
      setError(`Couldn't save: ${(e as Error).message}`);
    } finally {
      // Back to the server's answer: the refreshed row after a save, the old one after a failure.
      setPending(null);
      setSaving(false);
    }
  };

  return (
    <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
      <span className="font-mono text-[9px] font-bold uppercase tracking-[0.1em] text-ink-faint">Used for</span>
      {MEDIA_TYPES.map((m) => {
        const active = lit.includes(m.key);
        return (
          <button
            key={m.key}
            type="button"
            disabled={saving}
            aria-pressed={active}
            title={active ? `Stop using it for ${m.label}` : `Use it for ${m.label} too`}
            onClick={() => toggle(m.key)}
            className="rounded-full px-2.5 py-0.5 text-[10.5px] font-semibold transition-colors disabled:opacity-50"
            style={{
              border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`,
              background: active ? "var(--accent-soft)" : "var(--panel-2)",
              color: active ? "var(--accent)" : "var(--ink-faint)",
            }}
          >
            {m.label}
          </button>
        );
      })}
      {allLit && <span className="text-[10px] text-ink-faint">· all areas</span>}
      {error && <span role="alert" className="basis-full text-[11px]" style={{ color: "var(--reject)" }}>{error}</span>}
    </div>
  );
}
