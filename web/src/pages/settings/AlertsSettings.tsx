import { useState } from "react";
import { Section, input, inputStyle } from "../../components/settings/ui";
import { api, type AlertCatalog, type AlertEvent, type NotificationConn, type NotificationInput } from "../../lib/api";
import { isAdmin, useMe } from "../../lib/me";
import { useQuery, invalidate } from "../../lib/query";
import { Button, useConfirm, useToast } from "../../ui";

// Settings → Alerts: where the owner's own alerts go (a Discord channel, a phone, an
// inbox). Requesters' "your request is ready" messages are separate — each person sets
// those up from the bell on Discover.
//
// A saved link is never sent back to the browser (it usually holds a token or a
// password): the field shows a hint, and leaving it blank keeps what's saved, like the
// Plex token. Managers see the list; only admins change it.

export function AlertsSettings() {
  const { user, booksEnabled, musicEnabled } = useMe();
  const admin = isAdmin(user);
  const { data: conns, error } = useQuery("notifications:list", api.notifications);
  const { data: catalog, error: catalogError } = useQuery("notifications:catalog", api.alertCatalog, { staleMs: 5 * 60_000 });
  const [adding, setAdding] = useState(false);
  const reload = () => invalidate("notifications:list");

  // Events of a module that's switched off are hidden (a connection keeps them ticked).
  const shown = catalog && {
    ...catalog,
    events: catalog.events.filter((e) => (e.module !== "music" || musicEnabled) && (e.module !== "books" || booksEnabled)),
  };
  const blank = (): NotificationConn => ({ name: "", kind: "", enabled: true, events: (catalog?.events ?? []).filter((e) => e.default_on).map((e) => e.key) });
  const failed = error ?? catalogError;

  return (
    <div className="flex flex-col gap-6">
      <Section id="alerts" title="Alerts" subtitle="Get a message when something needs you or when new things arrive. Each connection is one place a message goes, with its own choice of events.">
        {!admin && <p className="text-[11.5px] text-ink-faint">Only an admin can add or change alert connections.</p>}
        {failed && !(conns && shown) ? (
          <p className="text-[12px]" style={{ color: "var(--reject)" }}>Couldn’t load alert connections — {failed.message}</p>
        ) : !conns || !shown ? (
          <p className="text-[12px] text-ink-dim">Loading…</p>
        ) : (
          <>
            {conns.length === 0 && !adding && <p className="text-[12px] text-ink-dim">No alert connections yet.</p>}
            {conns.map((c) => <ConnCard key={c.id} conn={c} catalog={shown} readOnly={!admin} onChange={reload} />)}
            {admin && (adding ? (
              <ConnCard conn={blank()} catalog={shown} isNew onChange={() => { setAdding(false); reload(); }} onCancel={() => setAdding(false)} />
            ) : (
              <Button className="self-start" onClick={() => setAdding(true)}>+ Add connection</Button>
            ))}
          </>
        )}
      </Section>
    </div>
  );
}

function ConnCard({ conn, catalog, isNew, readOnly, onChange, onCancel }: { conn: NotificationConn; catalog: AlertCatalog; isNew?: boolean; readOnly?: boolean; onChange: () => void; onCancel?: () => void }) {
  const [c, setC] = useState<NotificationConn>(conn);
  // Only what's typed here; blank on a saved card means "keep the saved link".
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const toast = useToast();
  const confirm = useConfirm();
  const set = (patch: Partial<NotificationConn>) => setC((p) => ({ ...p, ...patch }));
  const body = (): NotificationInput => ({
    name: c.name, kind: c.kind, enabled: c.enabled, events: c.events,
    ...(url.trim() ? { url: url.trim() } : {}),
  });

  const save = async () => {
    if (!c.name.trim() || (isNew && !url.trim())) { toast("A name and a link are both needed", { tone: "error" }); return; }
    setBusy("save");
    try {
      if (isNew) await api.createNotification(body());
      else await api.updateNotification(c.id!, body());
      setUrl("");
      toast("Saved", { tone: "good" });
      onChange();
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
  };
  // A link typed into the field is tried as typed (before saving); otherwise a saved card
  // is tested through the link stored on the server.
  const test = async () => {
    if (!url.trim() && (isNew || !c.id)) { toast("Enter a link first", { tone: "error" }); return; }
    setBusy("test");
    try {
      const r = url.trim() ? await api.testNotification(body()) : await api.testSavedNotification(c.id!);
      toast(r.ok ? "Test sent" : `Test failed — ${r.error || "no reason given"}`, { tone: r.ok ? "good" : "error" });
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
  };
  const del = async () => {
    if (!c.id) return;
    const ok = await confirm({ title: `Delete “${c.name}”?`, body: "Alerts stop going there. You can add it again later.", confirmLabel: "Delete", tone: "danger" });
    if (!ok) return;
    try { await api.deleteNotification(c.id); onChange(); } catch (e) { toast((e as Error).message, { tone: "error" }); }
  };

  const placeholder = !isNew && conn.url_set ? `${conn.url_hint ?? "saved"} — saved, leave blank to keep` : "discord://webhook_id/token";

  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex flex-wrap items-center gap-2">
        <input aria-label="Connection name" value={c.name} disabled={readOnly} onChange={(e) => set({ name: e.target.value })} placeholder="Name (e.g. My Discord)" className="min-w-[160px] flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={inputStyle} />
        <label className="flex cursor-pointer items-center gap-1.5 text-[11.5px]">
          <input type="checkbox" checked={c.enabled} disabled={readOnly} onChange={(e) => set({ enabled: e.target.checked })} /> Enabled
        </label>
      </div>
      {readOnly ? (
        <div className={`mt-2 ${input}`} style={inputStyle}>{conn.url_hint || "—"}</div>
      ) : (
        <>
          <input aria-label="Link" type="password" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={placeholder} className={`mt-2 ${input}`} style={inputStyle} autoComplete="off" />
          <p className="mt-1 text-[10.5px] text-ink-faint">
            An <a href="https://github.com/caronc/apprise/wiki" target="_blank" rel="noreferrer" style={{ color: "var(--accent)" }}>Apprise link</a> — e.g. <code>discord://id/token</code>, <code>tgram://token/chatid</code>, <code>ntfy://topic</code>, <code>mailtos://user:pass@host</code>.
          </p>
        </>
      )}
      {conn.invalid_reason && (
        <p className="mt-1 text-[11px]" style={{ color: "var(--avoid)" }}>The saved link no longer passes the check ({conn.invalid_reason}). It still sends; enter a corrected link to edit this connection’s link.</p>
      )}
      <EventPicker catalog={catalog} value={c.events} readOnly={readOnly} onChange={(events) => set({ events })} />
      {!readOnly && (
        <div className="mt-3 flex items-center gap-2">
          <Button variant="primary" size="sm" onClick={save} busy={busy === "save"} busyLabel="Saving…" disabled={busy !== null}>{isNew ? "Add" : "Save"}</Button>
          <Button size="sm" onClick={test} busy={busy === "test"} busyLabel="Testing…" disabled={busy !== null}>Test</Button>
          <span className="flex-1" />
          {isNew ? <Button variant="ghost" size="sm" onClick={onCancel}>Cancel</Button> : <Button variant="ghost" size="sm" onClick={del} style={{ color: "var(--reject)" }}>Delete</Button>}
        </div>
      )}
    </div>
  );
}

// EventPicker is the grouped checklist of catalog events, with a tick-all per group.
// Groups with no events (yet) aren't shown.
function EventPicker({ catalog, value, readOnly, onChange }: { catalog: AlertCatalog; value: string[]; readOnly?: boolean; onChange: (events: string[]) => void }) {
  const on = new Set(value);
  const toggle = (keys: string[], tick: boolean) => {
    const next = new Set(on);
    for (const k of keys) { if (tick) next.add(k); else next.delete(k); }
    onChange([...next].sort());
  };
  const byGroup = catalog.groups
    .map((g) => ({ ...g, events: catalog.events.filter((e) => e.group === g.key) }))
    .filter((g) => g.events.length > 0);

  return (
    <div className="mt-3 grid gap-3 sm:grid-cols-2">
      {byGroup.map((g) => {
        const keys = g.events.map((e) => e.key);
        const all = keys.every((k) => on.has(k));
        return (
          <fieldset key={g.key} className="min-w-0">
            <legend className="mb-1 flex w-full items-center justify-between gap-2">
              <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{g.label}</span>
              {!readOnly && keys.length > 1 && (
                <button type="button" onClick={() => toggle(keys, !all)} className="text-[10.5px]" style={{ color: "var(--accent)" }}>{all ? "None" : "All"}</button>
              )}
            </legend>
            <div className="flex flex-col gap-1">
              {g.events.map((ev: AlertEvent) => (
                <label key={ev.key} className="flex cursor-pointer items-start gap-1.5 text-[11.5px]" title={ev.hint}>
                  <input type="checkbox" aria-label={ev.label} className="mt-0.5" checked={on.has(ev.key)} disabled={readOnly} onChange={(e) => toggle([ev.key], e.target.checked)} />
                  <span className="min-w-0">
                    <span className="block">{ev.label}</span>
                    <span className="block text-[10.5px] text-ink-faint">{ev.hint}</span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
        );
      })}
    </div>
  );
}
