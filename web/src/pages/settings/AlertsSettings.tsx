import { useEffect, useState } from "react";
import { Section, input, inputStyle } from "../../components/settings/ui";
import { api, type AlertCatalog, type AlertEvent, type NotificationConn, type NotificationInput } from "../../lib/api";
import { ago, until } from "../../lib/taskTime";
import { isAdmin, useMe } from "../../lib/me";
import { useQuery, invalidate } from "../../lib/query";
import { pushSupported, subscribeThisDevice, thisDeviceEndpoint } from "../../lib/webpush";
import { Button, useConfirm, useToast } from "../../ui";

// Settings → Alerts: where the owner's own alerts go (a Discord channel, a phone, an
// inbox). Requesters' "your request is ready" messages are separate — each person sets
// those up from the bell on Discover.
//
// A saved link is never sent back to the browser (it usually holds a token or a
// password): the field shows a hint, and leaving it blank keeps what's saved, like the
// Plex token. Managers see the list; only admins change it.
//
// "This device" is the other kind of connection: Web Push to the admin's own browsers
// and phones, no outside service needed.

const PUSH_NAME = "This device";

export function AlertsSettings() {
  const { user, booksEnabled, musicEnabled } = useMe();
  const admin = isAdmin(user);
  const { data: conns, error } = useQuery("notifications:list", api.notifications);
  const { data: catalog, error: catalogError } = useQuery("notifications:catalog", api.alertCatalog, { staleMs: 5 * 60_000 });
  const [adding, setAdding] = useState(false);
  // The list and the delivery logs: a save, a Test or a delete changes both.
  const reload = () => { invalidate("notifications:list"); invalidate("notifications:deliveries"); };

  // Events of a module that's switched off are hidden (a connection keeps them ticked).
  const shown = catalog && {
    ...catalog,
    events: catalog.events.filter((e) => (e.module !== "music" || musicEnabled) && (e.module !== "books" || booksEnabled)),
  };
  const blank = (): NotificationConn => ({ name: "", kind: "", enabled: true, events: (catalog?.events ?? []).filter((e) => e.default_on).map((e) => e.key) });
  const failed = error ?? catalogError;
  const toast = useToast();
  const [pushBusy, setPushBusy] = useState(false);
  const myPush = conns?.find((c) => c.kind === "webpush" && c.config?.user_id === user?.id);

  // Turn on push here, then make the connection if this admin hasn't one yet (one
  // connection reaches every device they've turned push on for).
  const addPush = async () => {
    setPushBusy(true);
    try {
      await subscribeThisDevice();
      if (!myPush) {
        await api.createNotification({ name: PUSH_NAME, kind: "webpush", enabled: true, events: blank().events });
        toast("Push alerts are on for this device", { tone: "good" });
      } else {
        toast(`This device now gets “${myPush.name}” alerts`, { tone: "good" });
      }
      reload();
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setPushBusy(false); }
  };

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
              <div className="flex flex-wrap items-center gap-2">
                <Button onClick={() => setAdding(true)}>+ Add connection</Button>
                {pushSupported() && <Button onClick={addPush} busy={pushBusy} busyLabel="Turning on…">+ This device (push)</Button>}
              </div>
            ))}
            {admin && <p className="text-[10.5px] text-ink-faint">On iPhone, push works only when Arrmada is added to the Home Screen.</p>}
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
      const saved = !url.trim();
      const r = saved ? await api.testSavedNotification(c.id!) : await api.testNotification(body());
      toast(r.ok ? "Test sent" : `Test failed — ${r.error || "no reason given"}`, { tone: r.ok ? "good" : "error" });
      if (saved) onChange(); // the test is in the card's log and status now
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
        {!isNew && <StatusDot conn={conn} />}
        <input aria-label="Connection name" value={c.name} disabled={readOnly} onChange={(e) => set({ name: e.target.value })} placeholder="Name (e.g. My Discord)" className="min-w-[160px] flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={inputStyle} />
        <label className="flex cursor-pointer items-center gap-1.5 text-[11.5px]">
          <input type="checkbox" checked={c.enabled} disabled={readOnly} onChange={(e) => set({ enabled: e.target.checked })} /> Enabled
        </label>
      </div>
      {conn.kind === "webpush" ? (
        <PushDevices readOnly={readOnly} onChange={onChange} />
      ) : readOnly ? (
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
      {!isNew && c.id ? <DeliveryLog id={c.id} catalog={catalog} /> : null}
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

// PushDevices is a push connection's in place of a link: it reaches every device its
// admin turned push on for, and says whether this browser is one of them.
function PushDevices({ readOnly, onChange }: { readOnly?: boolean; onChange: () => void }) {
  const [here, setHere] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const endpoint = await thisDeviceEndpoint();
        const on = endpoint ? (await api.pushStatus(endpoint)).subscribed : false;
        if (alive) setHere(on);
      } catch { if (alive) setHere(false); }
    })();
    return () => { alive = false; };
  }, []);
  const turnOn = async () => {
    setBusy(true);
    try { await subscribeThisDevice(); setHere(true); onChange(); } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(false); }
  };
  return (
    <div className="mt-2 flex flex-wrap items-center gap-2 text-[11.5px] text-ink-dim">
      <span>Web Push to every device the account turned push on for.</span>
      {pushSupported() && here === false && (
        <>
          <span style={{ color: "var(--avoid)" }}>No devices subscribed on this browser.</span>
          {!readOnly && <Button size="sm" onClick={turnOn} busy={busy} busyLabel="Turning on…">Turn on here</Button>}
        </>
      )}
      {here === true && <span style={{ color: "var(--good)" }}>This device gets them.</span>}
    </div>
  );
}

// StatusDot is the card's last-delivery light: green when the last alert went through,
// red when it failed (amber while a retry is still due), grey before anything was sent.
// The reason is in the tooltip and spelled out beside it, so it works on a phone too.
function StatusDot({ conn }: { conn: NotificationConn }) {
  const s = conn.last_status ?? "";
  const color = s === "sent" ? "var(--good)" : s === "failed" ? "var(--reject)" : s === "retrying" ? "var(--avoid)" : "var(--ink-faint)";
  const text = s === "sent" ? `Last alert sent ${ago(conn.last_sent_at)}`
    : s === "failed" ? `Last alert failed: ${conn.last_error || "no reason given"}`
    : s === "retrying" ? `Last try failed, retrying: ${conn.last_error || "no reason given"}`
    : "Nothing sent yet";
  return (
    <span className="flex w-full min-w-0 items-center gap-1.5 text-[10.5px] text-ink-faint" title={text}>
      <span aria-hidden className="h-2 w-2 flex-none rounded-full" style={{ background: color }} />
      <span className="truncate" style={s === "failed" ? { color: "var(--reject)" } : undefined}>{text}</span>
    </span>
  );
}

// DeliveryLog is the "Recent deliveries" expander: what was sent, when, and how it went.
// It loads only when opened.
function DeliveryLog({ id, catalog }: { id: number; catalog: AlertCatalog }) {
  const [open, setOpen] = useState(false);
  const { data, error } = useQuery(`notifications:deliveries:${id}`, () => api.alertDeliveries(id), { enabled: open, staleMs: 0 });
  const label = (key: string) => key === "test" ? "Test" : catalog.events.find((e) => e.key === key)?.label ?? key;
  return (
    <div className="mt-3">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="text-[11px] font-semibold" style={{ color: "var(--accent)" }}>
        {open ? "Hide recent deliveries" : "Recent deliveries"}
      </button>
      {open && (
        <div className="mt-1.5 flex flex-col gap-1">
          {error && !data ? <p className="text-[11px]" style={{ color: "var(--reject)" }}>Couldn’t load deliveries — {error.message}</p>
            : !data ? <p className="text-[11px] text-ink-faint">Loading…</p>
            : data.length === 0 ? <p className="text-[11px] text-ink-faint">Nothing sent yet.</p>
            : data.map((d) => (
              <div key={d.id} className="flex flex-wrap items-baseline gap-x-2 text-[11px]">
                <span className="w-[72px] flex-none text-ink-faint">{ago(d.sent_at || d.created_at)}</span>
                <span className="font-semibold">{label(d.event_key)}</span>
                <span className="min-w-0 flex-1 truncate text-ink-dim">{d.body}</span>
                <span style={{ color: d.status === "sent" ? "var(--good)" : d.status === "failed" ? "var(--reject)" : "var(--ink-faint)" }}>
                  {d.status === "queued" && d.attempts > 0 ? `retry ${until(d.next_attempt_at)}` : d.status}
                </span>
                {d.last_error && d.status !== "sent" && <span className="w-full pl-[80px] text-[10.5px]" style={{ color: "var(--reject)" }}>{d.last_error}</span>}
              </div>
            ))}
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
