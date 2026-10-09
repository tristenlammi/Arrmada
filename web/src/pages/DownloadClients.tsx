import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type DownloadCategories, type DownloadClient } from "../lib/api";
import { hhmm } from "../lib/indexerStatus";
import { useQuery } from "../lib/query";
import { ErrorState, Skeleton, StaleBanner, useConfirm } from "../ui";

const NO_CLIENTS: DownloadClient[] = [];

type TestState = { loading?: boolean; ok?: boolean; error?: string };

export function DownloadClients() {
  const q = useQuery("download-clients", () => api.downloadClientList(), { staleMs: 0 });
  const list = q.data?.clients ?? NO_CLIENTS;
  const categories = q.data?.categories;
  const error = q.error?.message ?? null;
  const [tests, setTests] = useState<Record<number, TestState>>({});
  const [ports, setPorts] = useState<Record<number, number>>({});
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [rowErr, setRowErr] = useState<Record<number, string>>({});
  const [toggling, setToggling] = useState<number | null>(null);
  const [restoring, setRestoring] = useState(false);
  const [restoreErr, setRestoreErr] = useState<string | null>(null);
  const confirm = useConfirm();

  const refresh = q.refetch;
  const setErr = (id: number, msg: string | null) =>
    setRowErr((d) => { const n = { ...d }; if (msg) n[id] = msg; else delete n[id]; return n; });

  // Fetch each torrent client's incoming port so we can tell the user what to forward.
  // A switched-off client may not be running at all, so it isn't asked.
  useEffect(() => {
    for (const c of q.data?.clients ?? []) {
      if (c.kind === "qbittorrent" && c.enabled) {
        api.downloadClientStatus(c.id).then((s) => setPorts((p) => ({ ...p, [c.id]: s.listen_port }))).catch(() => {});
      }
    }
  }, [q.data]);

  const runTest = async (id: number) => {
    setTests((t) => ({ ...t, [id]: { loading: true } }));
    try {
      const res = await api.testDownloadClient(id);
      setTests((t) => ({ ...t, [id]: { ok: res.ok, error: res.error } }));
    } catch (e) {
      setTests((t) => ({ ...t, [id]: { ok: false, error: (e as Error).message } }));
    }
  };

  // Removing asks first and says what it does and doesn't touch; a refusal from the
  // server shows on the card instead of vanishing.
  const remove = async (dc: DownloadClient) => {
    const ok = await confirm({
      title: `Remove ${dc.name}?`,
      body: dc.bundled
        ? "Arrmada stops sending downloads to it. Torrents already there are untouched. It stays removed after a restart; you can restore it from this page."
        : "Arrmada stops sending downloads to it. Torrents already there are untouched.",
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setErr(dc.id, null);
    try {
      await api.deleteDownloadClient(dc.id);
      refresh();
    } catch (e) {
      setErr(dc.id, `Couldn't remove: ${(e as Error).message}`);
    }
  };

  // The switch saves straight away; the password field is left blank, which keeps it.
  const toggleEnabled = async (dc: DownloadClient) => {
    setToggling(dc.id);
    setErr(dc.id, null);
    try {
      await api.updateDownloadClient(dc.id, { name: dc.name, kind: dc.kind, url: dc.url, username: dc.username ?? "", password: "", enabled: !dc.enabled });
      await refresh();
    } catch (e) {
      setErr(dc.id, `Couldn't ${dc.enabled ? "disable" : "enable"} it: ${(e as Error).message}`);
    } finally {
      setToggling(null);
    }
  };

  // The bundled qBittorrent stays removed once deleted; this is the way back.
  const restoreBundled = async () => {
    setRestoring(true);
    setRestoreErr(null);
    try {
      await api.restoreBundledClient();
      await refresh();
    } catch (e) {
      setRestoreErr(`Couldn't restore it: ${(e as Error).message}`);
    } finally {
      setRestoring(false);
    }
  };

  // After any save, test the client at once, so a wrong URL or password shows up now
  // instead of at the first grab.
  const saved = (c: DownloadClient) => {
    setShowForm(false);
    setEditingId(null);
    refresh();
    runTest(c.id);
  };

  return (
    <>
      <PageHeader title="Download clients" />
      <div className="mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between gap-3">
          <p className="m-0 text-[12.5px] text-ink-dim">
            Where grabbed releases are sent to download. qBittorrent for torrents.
          </p>
          <button
            onClick={() => setShowForm((s) => !s)}
            className="flex-none rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            {showForm ? "Cancel" : "+ Add client"}
          </button>
        </div>

        {showForm && <ClientForm onSaved={saved} />}

        {q.data?.can_restore_bundled && (
          <div className="mb-4 flex flex-wrap items-center gap-3 rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
            <p className="m-0 min-w-0 flex-1 basis-[220px] text-[12px] text-ink-dim">
              The qBittorrent that comes with Arrmada was removed, so it gets no downloads.
            </p>
            <button
              onClick={restoreBundled}
              disabled={restoring}
              className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50"
              style={{ border: "1px solid var(--line)", color: "var(--accent)" }}
            >
              {restoring ? "Restoring…" : "Restore bundled qBittorrent"}
            </button>
            {restoreErr && <div className="basis-full text-[12px]" style={{ color: "var(--reject)" }}>{restoreErr}</div>}
          </div>
        )}

        {q.data && error && <StaleBanner message={error} onRetry={refresh} />}

        {!q.data ? (
          error ? <ErrorState what="download clients" message={error} onRetry={refresh} busy={q.loading} /> : <Skeleton variant="list" count={2} />
        ) : list.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No download clients yet. Add qBittorrent to start downloading.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {list.map((dc) => {
              const t = tests[dc.id];
              return (
                <div key={dc.id} className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                  <div className="flex flex-wrap items-center gap-3">
                    <div className="min-w-0 flex-1 basis-[180px]" style={{ opacity: dc.enabled ? 1 : 0.55 }}>
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-[13.5px] font-semibold">{dc.name}</span>
                        <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>
                          {dc.kind}
                        </span>
                        {dc.bundled && (
                          <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px]" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>
                            bundled
                          </span>
                        )}
                        {list.length > 1 && (
                          <span className="font-mono text-[10.5px] text-ink-faint" title="New downloads go to the lowest number; the next is used only when it can't be reached.">
                            order {dc.priority}
                          </span>
                        )}
                      </div>
                      <div className="mt-1 truncate font-mono text-[11px] text-ink-faint">{dc.url}</div>
                      {categories && <CategoryLine c={categories} />}
                      {!dc.enabled && <div className="mt-1 text-[11px] text-ink-dim">Disabled: gets no new downloads.</div>}
                      {dc.enabled && dc.status?.failing_since && (
                        <div className="mt-1 text-[11px]" style={{ color: "var(--reject)" }} title={dc.status.last_error}>
                          Unreachable since {hhmm(dc.status.failing_since)}. While it’s down, stalled-download fail-over is paused for all clients; disable it if it’s gone.
                        </div>
                      )}
                      {dc.enabled && ports[dc.id] > 0 && (
                        <div className="mt-1.5 flex items-center gap-1.5 text-[11px]">
                          <span className="rounded px-1.5 py-0.5 font-mono text-[10px]" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
                            incoming port {ports[dc.id]}
                          </span>
                          <span className="text-ink-faint">forward TCP + UDP on your router to seed properly</span>
                        </div>
                      )}
                    </div>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={dc.enabled}
                      aria-label={`${dc.name} enabled`}
                      title={dc.enabled ? "Switch off: gets no new downloads" : "Switch on"}
                      disabled={toggling === dc.id}
                      onClick={() => toggleEnabled(dc)}
                      className="relative inline-block h-[22px] w-[38px] flex-none rounded-full transition-colors disabled:opacity-50"
                      style={{ background: dc.enabled ? "var(--accent)" : "var(--line)" }}
                    >
                      <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: dc.enabled ? "19px" : "3px" }} />
                    </button>
                    <button onClick={() => setEditingId(editingId === dc.id ? null : dc.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                      {editingId === dc.id ? "Close" : "Edit"}
                    </button>
                    <button onClick={() => runTest(dc.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                      {t?.loading ? "Testing…" : "Test"}
                    </button>
                    <button onClick={() => remove(dc)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>
                      Delete
                    </button>
                  </div>
                  {rowErr[dc.id] && (
                    <div className="mt-2.5 text-[12px]" style={{ color: "var(--reject)" }}>{rowErr[dc.id]}</div>
                  )}
                  {t && !t.loading && (
                    <div className="mt-2.5 font-mono text-[11px]" style={{ color: t.ok ? "var(--good)" : "var(--reject)" }}>
                      {t.ok ? "✓ Connected" : `✕ ${t.error ?? "failed"}`}
                    </div>
                  )}
                  {editingId === dc.id && <ClientForm editing={dc} onSaved={saved} />}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}

// ClientForm adds a client, or edits one in place when given `editing`. The stored
// password is never sent to the browser, so on an edit a blank password keeps it.
function ClientForm({ editing, onSaved }: { editing?: DownloadClient; onSaved: (c: DownloadClient) => void }) {
  const [name, setName] = useState(editing?.name ?? "qBittorrent");
  // No default URL: localhost:8080 inside the Arrmada container is Arrmada itself.
  const [url, setUrl] = useState(editing?.url ?? "");
  const [username, setUsername] = useState(editing?.username ?? "");
  const [password, setPassword] = useState("");
  const [priority, setPriority] = useState(String(editing?.priority ?? 25));
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const order = Number(priority);
    if (!Number.isInteger(order) || order < 1 || order > 99) {
      setError("Order must be a whole number from 1 to 99.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const c = editing
        ? await api.updateDownloadClient(editing.id, { name, kind: editing.kind, url, username, password, enabled: editing.enabled, priority: order })
        : await api.createDownloadClient({ name, kind: "qbittorrent", url, username, password, priority: order });
      onSaved(c);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const field = "w-full rounded-lg px-3 py-2 text-[13px]";
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

  return (
    <form
      onSubmit={submit}
      className={editing ? "mt-3 rounded-lg p-3.5" : "mb-4 rounded-xl p-4"}
      style={{ background: editing ? "var(--panel-2)" : "var(--panel)", border: "1px solid var(--line)" }}
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Labeled label="Name">
          <input className={field} style={fieldStyle} value={name} onChange={(e) => setName(e.target.value)} required />
        </Labeled>
        <Labeled label="WebUI URL">
          <input
            className={field}
            style={fieldStyle}
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="http://qbittorrent:8080 (as reached from the Arrmada container)"
            required
          />
        </Labeled>
        <Labeled label="Username">
          <input className={field} style={fieldStyle} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </Labeled>
        <Labeled label="Password">
          <input
            type="password"
            className={field}
            style={fieldStyle}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={editing ? "unchanged; leave blank to keep" : undefined}
            autoComplete="new-password"
          />
        </Labeled>
        <Labeled label="Order (1 = first)" hint="New downloads go to the lowest number. The next client is used only when this one can't be reached at all.">
          <input type="number" min={1} max={99} className={field} style={fieldStyle} value={priority} onChange={(e) => setPriority(e.target.value)} required />
        </Labeled>
      </div>
      <p className="mt-3 text-[11px] text-ink-faint">
        Points at your qBittorrent WebUI as the Arrmada container reaches it: its container name or your server’s IP. localhost here means the Arrmada container itself, not your server. Credentials are stored on your server.
      </p>
      {error && <div className="mt-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
      <button
        type="submit"
        disabled={saving}
        className="mt-3.5 rounded-lg px-4 py-2 text-[12.5px] font-semibold"
        style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: saving ? 0.6 : 1 }}
      >
        {saving ? "Saving…" : editing ? "Save changes" : "Save client"}
      </button>
    </form>
  );
}

// CategoryLine names the categories Arrmada files downloads under in this client. They're
// fixed: each importer reads only its own, so they aren't a setting.
function CategoryLine({ c }: { c: DownloadCategories }) {
  return (
    <div className="mt-1 text-[11px] text-ink-dim" title="Arrmada files every download under one of these, so each importer finds its own.">
      Categories: <span className="font-mono">{c.movies}</span> (movies) · <span className="font-mono">{c.tv}</span> · <span className="font-mono">{c.books}</span> · <span className="font-mono">{c.music}</span>
    </div>
  );
}

function Labeled({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{label}</span>
      {children}
      {hint && <span className="text-[10.5px] text-ink-faint">{hint}</span>}
    </label>
  );
}
