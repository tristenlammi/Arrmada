import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type NotificationConn } from "../lib/api";

const BLANK: NotificationConn = { name: "", kind: "discord", url: "", on_grab: true, on_import: true, enabled: true };

export function Notifications() {
  const [list, setList] = useState<NotificationConn[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<NotificationConn | null>(null);

  const refresh = () =>
    api.notifications().then((l) => (setList(l), setError(null))).catch((e: Error) => setError(e.message));

  useEffect(() => {
    refresh();
  }, []);

  const remove = async (id?: number) => {
    if (id == null) return;
    await api.deleteNotification(id);
    refresh();
  };

  return (
    <>
      <PageHeader title="Notifications" crumb="System / Notifications" />
      <div className="mx-auto w-full max-w-[1100px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between">
          <p className="m-0 text-[12.5px] text-ink-dim">
            Get a ping when Arrmada grabs or imports a release. Discord webhooks or any generic webhook.
          </p>
          <button
            onClick={() => setEditing(editing ? null : { ...BLANK })}
            className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            {editing ? "Cancel" : "+ Add connection"}
          </button>
        </div>

        {editing && (
          <ConnForm
            initial={editing}
            onDone={() => {
              setEditing(null);
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
            No notification connections yet.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {list.map((n) => (
              <div key={n.id} className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                <div className="flex items-center gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[13.5px] font-semibold">{n.name}</span>
                      <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{n.kind}</span>
                      {!n.enabled && <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>disabled</span>}
                    </div>
                    <div className="mt-1 flex flex-wrap gap-1.5">
                      {n.on_grab && <Tag>on grab</Tag>}
                      {n.on_import && <Tag>on import</Tag>}
                    </div>
                    <div className="mt-1 truncate font-mono text-[11px] text-ink-faint">{n.url}</div>
                  </div>
                  <button onClick={() => setEditing(n)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Edit</button>
                  <button onClick={() => remove(n.id)} className="rounded-lg px-3 py-1.5 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete</button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function Tag({ children }: { children: React.ReactNode }) {
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px]" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{children}</span>;
}

function ConnForm({ initial, onDone }: { initial: NotificationConn; onDone: () => void }) {
  const [c, setC] = useState<NotificationConn>(initial);
  const [error, setError] = useState<string | null>(null);
  const [test, setTest] = useState<{ ok?: boolean; error?: string; loading?: boolean } | null>(null);
  const [saving, setSaving] = useState(false);
  const patch = (p: Partial<NotificationConn>) => setC((x) => ({ ...x, ...p }));

  const runTest = async () => {
    setTest({ loading: true });
    try {
      const r = await api.testNotification(c);
      setTest({ ok: r.ok, error: r.error });
    } catch (e) {
      setTest({ ok: false, error: (e as Error).message });
    }
  };

  const save = async () => {
    if (!c.name.trim() || !c.url.trim()) {
      setError("Name and URL are required.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (c.id != null) await api.updateNotification(c.id, c);
      else await api.createNotification(c);
      onDone();
    } catch (e) {
      setError((e as Error).message);
      setSaving(false);
    }
  };

  const field = "w-full rounded-lg px-3 py-2 text-[13px]";
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

  return (
    <div className="mb-4 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="grid grid-cols-2 gap-3">
        <label className="flex flex-col gap-1.5">
          <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Name</span>
          <input className={field} style={fieldStyle} value={c.name} onChange={(e) => patch({ name: e.target.value })} placeholder="My Discord" />
        </label>
        <label className="flex flex-col gap-1.5">
          <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Type</span>
          <select className={field} style={fieldStyle} value={c.kind} onChange={(e) => patch({ kind: e.target.value })}>
            <option value="discord">Discord webhook</option>
            <option value="webhook">Generic webhook</option>
          </select>
        </label>
        <label className="col-span-2 flex flex-col gap-1.5">
          <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Webhook URL</span>
          <input className={field} style={fieldStyle} value={c.url} onChange={(e) => patch({ url: e.target.value })} placeholder="https://discord.com/api/webhooks/…" />
        </label>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-4">
        <Check label="Notify on grab" checked={c.on_grab} onChange={(v) => patch({ on_grab: v })} />
        <Check label="Notify on import" checked={c.on_import} onChange={(v) => patch({ on_import: v })} />
        <Check label="Enabled" checked={c.enabled} onChange={(v) => patch({ enabled: v })} />
      </div>
      {test && !test.loading && (
        <div className="mt-3 font-mono text-[11px]" style={{ color: test.ok ? "var(--good)" : "var(--reject)" }}>
          {test.ok ? "✓ Test message sent" : `✕ ${test.error ?? "failed"}`}
        </div>
      )}
      {error && <div className="mt-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
      <div className="mt-3.5 flex gap-2.5">
        <button onClick={save} disabled={saving} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: saving ? 0.6 : 1 }}>
          {saving ? "Saving…" : "Save"}
        </button>
        <button onClick={runTest} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
          {test?.loading ? "Testing…" : "Send test"}
        </button>
      </div>
    </div>
  );
}

function Check({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center gap-2 text-[12px] text-ink-dim">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} style={{ accentColor: "var(--accent)" }} />
      {label}
    </label>
  );
}
