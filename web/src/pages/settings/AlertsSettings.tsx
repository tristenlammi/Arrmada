import { useState } from "react";
import { Section, input, inputStyle } from "../../components/settings/ui";
import { api, type NotificationConn } from "../../lib/api";
import { useQuery, invalidate } from "../../lib/query";
import { Button, useConfirm, useToast } from "../../ui";

// Settings → Alerts: where the owner's own alerts go (a Discord channel, a phone, an
// inbox). Requesters' "your request is ready" messages are separate — each person sets
// those up from the bell on Discover.

const EVENTS: { key: keyof NotificationConn; label: string }[] = [
  { key: "on_grab", label: "Grabbed" },
  { key: "on_import", label: "Imported" },
  { key: "on_stream", label: "Stream started" },
  { key: "on_buffering", label: "Buffering" },
];
const BLANK_CONN: NotificationConn = { name: "", kind: "", url: "", on_grab: false, on_import: true, on_stream: false, on_buffering: false, enabled: true };

export function AlertsSettings() {
  const { data: conns, error } = useQuery("notifications", api.notifications);
  const [adding, setAdding] = useState(false);
  const reload = () => invalidate("notifications");

  return (
    <div className="flex flex-col gap-6">
      <Section id="alerts" title="Alerts" subtitle="Get a message when something needs you or when new things arrive. Each connection is one place a message goes, with its own choice of events.">
        {error && !conns ? (
          <p className="text-[12px]" style={{ color: "var(--reject)" }}>Couldn’t load alert connections — {error.message}</p>
        ) : !conns ? (
          <p className="text-[12px] text-ink-dim">Loading…</p>
        ) : (
          <>
            {conns.length === 0 && !adding && <p className="text-[12px] text-ink-dim">No alert connections yet.</p>}
            {conns.map((c) => <ConnCard key={c.id} conn={c} onChange={reload} />)}
            {adding ? (
              <ConnCard conn={BLANK_CONN} isNew onChange={() => { setAdding(false); reload(); }} onCancel={() => setAdding(false)} />
            ) : (
              <Button className="self-start" onClick={() => setAdding(true)}>+ Add connection</Button>
            )}
          </>
        )}
      </Section>
    </div>
  );
}

function ConnCard({ conn, isNew, onChange, onCancel }: { conn: NotificationConn; isNew?: boolean; onChange: () => void; onCancel?: () => void }) {
  const [c, setC] = useState<NotificationConn>(conn);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const toast = useToast();
  const confirm = useConfirm();
  const set = (patch: Partial<NotificationConn>) => setC((p) => ({ ...p, ...patch }));

  const save = async () => {
    if (!c.name.trim() || !c.url.trim()) { toast("A name and a link are both needed", { tone: "error" }); return; }
    setBusy("save");
    try {
      if (isNew) await api.createNotification(c);
      else await api.updateNotification(c.id!, c);
      toast("Saved", { tone: "good" });
      onChange();
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
  };
  const test = async () => {
    if (!c.url.trim()) { toast("Enter a link first", { tone: "error" }); return; }
    setBusy("test");
    try {
      const r = await api.testNotification(c);
      toast(r.ok ? "Test sent" : `Test failed — ${r.error || "no reason given"}`, { tone: r.ok ? "good" : "error" });
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
  };
  const del = async () => {
    if (!c.id) return;
    const ok = await confirm({ title: `Delete “${c.name}”?`, body: "Alerts stop going there. You can add it again later.", confirmLabel: "Delete", tone: "danger" });
    if (!ok) return;
    try { await api.deleteNotification(c.id); onChange(); } catch (e) { toast((e as Error).message, { tone: "error" }); }
  };

  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex flex-wrap items-center gap-2">
        <input aria-label="Connection name" value={c.name} onChange={(e) => set({ name: e.target.value })} placeholder="Name (e.g. My Discord)" className="min-w-[160px] flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={inputStyle} />
        <label className="flex cursor-pointer items-center gap-1.5 text-[11.5px]">
          <input type="checkbox" checked={c.enabled} onChange={(e) => set({ enabled: e.target.checked })} /> Enabled
        </label>
      </div>
      <input aria-label="Link" value={c.url} onChange={(e) => set({ url: e.target.value })} placeholder="discord://webhook_id/token" className={`mt-2 ${input}`} style={inputStyle} autoComplete="off" />
      <p className="mt-1 text-[10.5px] text-ink-faint">
        An <a href="https://github.com/caronc/apprise/wiki" target="_blank" rel="noreferrer" style={{ color: "var(--accent)" }}>Apprise link</a> — e.g. <code>discord://id/token</code>, <code>tgram://token/chatid</code>, <code>ntfy://topic</code>, <code>mailtos://user:pass@host</code>.
      </p>
      <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1.5">
        {EVENTS.map((ev) => (
          <label key={ev.key} className="flex cursor-pointer items-center gap-1.5 text-[11.5px] text-ink-dim">
            <input type="checkbox" checked={!!c[ev.key]} onChange={(e) => set({ [ev.key]: e.target.checked } as Partial<NotificationConn>)} /> {ev.label}
          </label>
        ))}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Button variant="primary" size="sm" onClick={save} busy={busy === "save"} busyLabel="Saving…" disabled={busy !== null}>{isNew ? "Add" : "Save"}</Button>
        <Button size="sm" onClick={test} busy={busy === "test"} busyLabel="Testing…" disabled={busy !== null}>Test</Button>
        <span className="flex-1" />
        {isNew ? <Button variant="ghost" size="sm" onClick={onCancel}>Cancel</Button> : <Button variant="ghost" size="sm" onClick={del} style={{ color: "var(--reject)" }}>Delete</Button>}
      </div>
    </div>
  );
}
