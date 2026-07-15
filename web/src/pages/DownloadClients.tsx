import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type DownloadClient } from "../lib/api";

type TestState = { loading?: boolean; ok?: boolean; error?: string };

export function DownloadClients() {
  const [list, setList] = useState<DownloadClient[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [tests, setTests] = useState<Record<number, TestState>>({});
  const [ports, setPorts] = useState<Record<number, number>>({});
  const [showForm, setShowForm] = useState(false);

  const refresh = () =>
    api
      .downloadClients()
      .then((l) => {
        setList(l);
        setError(null);
        // Fetch each torrent client's incoming port so we can tell the user what to forward.
        for (const c of l) {
          if (c.kind === "qbittorrent") {
            api.downloadClientStatus(c.id).then((s) => setPorts((p) => ({ ...p, [c.id]: s.listen_port }))).catch(() => {});
          }
        }
      })
      .catch((e: Error) => setError(e.message));

  useEffect(() => {
    refresh();
  }, []);

  const runTest = async (id: number) => {
    setTests((t) => ({ ...t, [id]: { loading: true } }));
    try {
      const res = await api.testDownloadClient(id);
      setTests((t) => ({ ...t, [id]: { ok: res.ok, error: res.error } }));
    } catch (e) {
      setTests((t) => ({ ...t, [id]: { ok: false, error: (e as Error).message } }));
    }
  };

  const remove = async (id: number) => {
    await api.deleteDownloadClient(id);
    refresh();
  };

  return (
    <>
      <PageHeader title="Download clients" crumb="System / Download clients" />
      <div className="mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between">
          <p className="m-0 text-[12.5px] text-ink-dim">
            Where grabbed releases are sent to download. qBittorrent for torrents.
          </p>
          <button
            onClick={() => setShowForm((s) => !s)}
            className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            {showForm ? "Cancel" : "+ Add client"}
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

        {error && (
          <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>
            {error}
          </div>
        )}

        {list.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No download clients yet. Add qBittorrent to start downloading.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {list.map((dc) => {
              const t = tests[dc.id];
              return (
                <div key={dc.id} className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                  <div className="flex items-center gap-3">
                    <div className="flex-1">
                      <div className="flex items-center gap-2">
                        <span className="text-[13.5px] font-semibold">{dc.name}</span>
                        <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>
                          {dc.kind}
                        </span>
                        {dc.category && (
                          <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px]" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
                            {dc.category}
                          </span>
                        )}
                      </div>
                      <div className="mt-1 truncate font-mono text-[11px] text-ink-faint">{dc.url}</div>
                      {ports[dc.id] > 0 && (
                        <div className="mt-1.5 flex items-center gap-1.5 text-[11px]">
                          <span className="rounded px-1.5 py-0.5 font-mono text-[10px]" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
                            incoming port {ports[dc.id]}
                          </span>
                          <span className="text-ink-faint">forward TCP + UDP on your router to seed properly</span>
                        </div>
                      )}
                    </div>
                    <button onClick={() => runTest(dc.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                      {t?.loading ? "Testing…" : "Test"}
                    </button>
                    <button onClick={() => remove(dc.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>
                      Delete
                    </button>
                  </div>
                  {t && !t.loading && (
                    <div className="mt-2.5 font-mono text-[11px]" style={{ color: t.ok ? "var(--good)" : "var(--reject)" }}>
                      {t.ok ? "✓ Connected" : `✕ ${t.error ?? "failed"}`}
                    </div>
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

function AddForm({ onAdded }: { onAdded: () => void }) {
  const [name, setName] = useState("qBittorrent");
  const [url, setUrl] = useState("http://localhost:8080");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [category, setCategory] = useState("arrmada");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      await api.createDownloadClient({ name, kind: "qbittorrent", url, username, password, category });
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
      <div className="grid grid-cols-2 gap-3">
        <Labeled label="Name">
          <input className={field} style={fieldStyle} value={name} onChange={(e) => setName(e.target.value)} required />
        </Labeled>
        <Labeled label="WebUI URL">
          <input className={field} style={fieldStyle} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://localhost:8080" required />
        </Labeled>
        <Labeled label="Username">
          <input className={field} style={fieldStyle} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </Labeled>
        <Labeled label="Password">
          <input type="password" className={field} style={fieldStyle} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
        </Labeled>
        <Labeled label="Category" span2>
          <input className={field} style={fieldStyle} value={category} onChange={(e) => setCategory(e.target.value)} placeholder="arrmada" />
        </Labeled>
      </div>
      <p className="mt-3 text-[11px] text-ink-faint">
        Points at your qBittorrent WebUI. The category keeps Arrmada’s downloads separate. Credentials are stored on your server.
      </p>
      {error && <div className="mt-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
      <button
        type="submit"
        disabled={saving}
        className="mt-3.5 rounded-lg px-4 py-2 text-[12.5px] font-semibold"
        style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: saving ? 0.6 : 1 }}
      >
        {saving ? "Saving…" : "Save client"}
      </button>
    </form>
  );
}

function Labeled({ label, span2, children }: { label: string; span2?: boolean; children: React.ReactNode }) {
  return (
    <label className={`flex flex-col gap-1.5 ${span2 ? "col-span-2" : ""}`}>
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{label}</span>
      {children}
    </label>
  );
}
