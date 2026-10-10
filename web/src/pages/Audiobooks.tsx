import { Suspense, useEffect, useMemo, useState } from "react";
import { api, type AudioHistoryEntry, type AudioListening, type AudioPlace, type AudioRemoved, type MyAudio } from "../lib/api";
import { useMe } from "../lib/me";
import { posterThumb } from "../lib/img";
import { lazyPage } from "../lib/lazyPage";
import { useTabParam } from "../lib/useTabParam";
import { playAudiobook } from "../lib/playerStub";
import { TabPanel, Tabs, type TabItem } from "../ui/Tabs";
import { PageHeader } from "../components/PageHeader";
import { PlaceTimeline } from "../components/audiobooks/PlaceTimeline";
import { OfferBanner } from "../components/audiobooks/OfferBanner";
import { Card, DailyBars, DaysPicker, Field, Loading, Sessions, Totals, addresses, danger, fmtAgo, fmtClock, ghost, inputStyle, primary } from "./audiobooks/shared";

// The admin tabs are their own chunk: requesters only ever download "You".
const AudiobooksAdmin = lazyPage(() => import("./AudiobooksAdmin"), "AudiobooksAdmin");

// Audiobooks is the audiobook server's home: Arrmada serving audiobooks to listening
// apps (Lissen, or anything that speaks Audiobookshelf). Everyone gets "You": how to
// connect, their audiobook password, where they're up to, their listening and devices.
// Admins also get the server switch, who may connect, everyone's listening (how much and
// when — never what) and the Audiobookshelf import. Nobody sees anyone else's places.

type Tab = "you" | "server" | "people" | "listening" | "import";
const TABS: TabItem<Tab>[] = [
  { key: "you", label: "You" },
  { key: "server", label: "Server" },
  { key: "people", label: "People" },
  { key: "listening", label: "Listening" },
  { key: "import", label: "Import" },
];
const ALL_TABS = TABS.map((t) => t.key);
const YOU_ONLY: readonly Tab[] = ["you"];

export function Audiobooks({ chrome = true }: { chrome?: boolean }) {
  const { user } = useMe();
  const admin = user?.role === "admin";
  // Only admins have tabs; anyone else following a link to ?tab=server lands on You.
  const [tab, setTab] = useTabParam(admin ? ALL_TABS : YOU_ONLY, "you");
  return (
    <>
      {chrome && <PageHeader title="Audiobooks" />}
      <div className="mx-auto w-full max-w-[980px] px-4 py-6 sm:px-6">
        {!chrome && <h1 className="m-0 mb-1 text-[18px] font-bold">Audiobooks</h1>}
        <p className="m-0 mb-4 max-w-[70ch] text-[12.5px] text-ink-dim">
          Listen to the library's audiobooks in a listening app like Lissen. Your place is kept in Arrmada, so it follows you between devices and a glitch can't lose it.
        </p>
        {admin ? (
          <>
            <Tabs tabs={TABS} value={tab} onChange={setTab} idPrefix="audiobooks" label="Audiobooks sections" />
            <TabPanel idPrefix="audiobooks" value={tab}>
              {tab === "you" ? (
                <YouView />
              ) : (
                <Suspense fallback={<Loading />}>
                  <AudiobooksAdmin tab={tab} />
                </Suspense>
              )}
            </TabPanel>
          </>
        ) : (
          <YouView />
        )}
      </div>
    </>
  );
}

// --- You ------------------------------------------------------------------------

function YouView() {
  const { external } = useMe();
  const [data, setData] = useState<MyAudio | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = () => api.myAudio().then(setData).catch((e) => setError((e as Error).message));
  useEffect(() => { load(); }, []);
  if (!data) return <Loading error={error} />;

  if (!data.allowed) {
    return <Card title="Not available">
      <p className="m-0 text-[12.5px] text-ink-dim">Your account isn't set up to use the audiobook server. Ask an admin.</p>
    </Card>;
  }
  return (
    <div className="flex flex-col gap-4">
      <Card title="Connect an app" note={data.enabled ? undefined : "The audiobook server is switched off at the moment, so apps can't connect yet."}>
        <p className="m-0 mb-3 text-[12px] text-ink-dim">
          Use <a href="https://github.com/GrakovNe/lissen-android" target="_blank" rel="noreferrer" className="underline" style={{ color: "var(--accent)" }}>Lissen</a> (Android) or another Audiobookshelf app. Choose <b>Audiobookshelf</b> as the server type and enter:
        </p>
        {addresses(data, external).map((a, i) => <Field key={i} label={a.label} value={a.value} note={a.note} />)}
        {external && !data.public_url && <div className="mb-2 text-[12px] text-ink-dim">The server address works on the home network. Open this page at home to see it.</div>}
        <Field label="Username" value={data.username} />
        <div className="text-[11.5px] text-ink-faint">Password: your audiobook password below — not your Arrmada password.</div>
        {data.enabled && !data.running && <div className="mt-2 text-[12px]" style={{ color: "var(--avoid)" }}>The server is switched on but not running{data.error ? `: ${data.error}` : ""}.</div>}
      </Card>

      <PasswordCard data={data} onSaved={setData} />
      <PlacesCard places={data.places} onChange={load} />
      {data.removed?.length > 0 && <RemovedCard removed={data.removed} onChange={load} />}
      <MyListeningCard />
      {data.devices.length > 0 && (
        <Card title="Your devices" note="Apps signed in as you. Sign one out if you lose the phone or stop using it.">
          {data.devices.map((d) => (
            <div key={d.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <div className="min-w-0">
                <div className="truncate font-semibold">{d.device || d.client || "App"}{d.client && d.device && d.device !== d.client ? <span className="font-normal text-ink-faint"> · {d.client}</span> : null}</div>
                <div className="text-[11px] text-ink-faint">Signed in {fmtAgo(d.created_at)} · last used {fmtAgo(d.last_used_at)}</div>
              </div>
              <button onClick={() => api.revokeMyDevice(d.id).then(load)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={danger}>Sign out</button>
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}

function PasswordCard({ data, onSaved }: { data: MyAudio; onSaved: (d: MyAudio) => void }) {
  const [open, setOpen] = useState(!data.has_password);
  const [pw, setPw] = useState("");
  const [again, setAgain] = useState("");
  const [signOut, setSignOut] = useState(true);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const min = data.min_password_length || 8;
  const problem = pw.length > 0 && pw.length < min ? `At least ${min} characters.` : again.length > 0 && pw !== again ? "The passwords don't match." : null;
  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      const d = await api.setAudioPassword(pw, data.has_password && signOut);
      onSaved(d); setPw(""); setAgain(""); setOpen(false);
      setMsg({ ok: true, text: data.has_password ? "Password changed." : "Password set — you can sign in from an app now." });
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!window.confirm("Remove your audiobook password? Your apps will be signed out and can't connect until you set a new one.")) return;
    setBusy(true); setMsg(null);
    try { onSaved(await api.removeAudioPassword()); setOpen(true); } catch (e) { setMsg({ ok: false, text: (e as Error).message }); } finally { setBusy(false); }
  };
  return (
    <Card
      title="Audiobook password"
      note={data.has_password ? "Set. Apps sign in with your username and this password." : "Not set yet — set one to connect an app. It's separate from your Arrmada password."}
      right={data.has_password && !open ? (
        <div className="flex flex-none gap-2">
          <button onClick={() => setOpen(true)} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>Change</button>
          <button onClick={remove} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={danger}>Remove</button>
        </div>
      ) : undefined}
    >
      {open && (
        <form onSubmit={(e) => { e.preventDefault(); if (!problem && pw && pw === again) save(); }} className="flex max-w-[420px] flex-col gap-2">
          <input type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} placeholder={data.has_password ? "New password" : "Password"} className="rounded-lg px-3 py-1.5 text-[12.5px]" style={inputStyle} />
          <input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} placeholder="Type it again" className="rounded-lg px-3 py-1.5 text-[12.5px]" style={inputStyle} />
          {data.has_password && (
            <label className="flex items-center gap-2 text-[12px] text-ink-dim">
              <input type="checkbox" checked={signOut} onChange={(e) => setSignOut(e.target.checked)} />
              Sign out my apps (they'll need the new password)
            </label>
          )}
          {problem && <div className="text-[12px]" style={{ color: "var(--avoid)" }}>{problem}</div>}
          <div className="flex gap-2">
            <button type="submit" disabled={busy || !!problem || !pw || pw !== again} className="rounded-lg px-3.5 py-1.5 text-[12.5px] font-semibold disabled:opacity-50" style={primary}>{data.has_password ? "Change password" : "Set password"}</button>
            {data.has_password && <button type="button" onClick={() => { setOpen(false); setPw(""); setAgain(""); }} className="rounded-lg px-3 py-1.5 text-[12.5px] font-semibold" style={ghost}>Cancel</button>}
          </div>
        </form>
      )}
      {msg && <div className="mt-2 text-[12px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</div>}
    </Card>
  );
}

function PlacesCard({ places, onChange }: { places: AudioPlace[]; onChange: () => void }) {
  const [showFinished, setShowFinished] = useState(false);
  const going = places.filter((p) => !p.finished);
  const done = places.filter((p) => p.finished);
  return (
    <Card title="Where you're up to" note="Your place in each audiobook. If an app ever loses it, put back an earlier one.">
      {places.length === 0 && <div className="text-[12px] text-ink-faint">Nothing yet — start an audiobook in your app and it shows up here.</div>}
      {going.map((p) => <PlaceRow key={p.item_key} p={p} onChange={onChange} />)}
      {done.length > 0 && (
        <button onClick={() => setShowFinished(!showFinished)} className="mt-2 text-[12px] font-semibold" style={{ color: "var(--accent)" }}>
          {showFinished ? "Hide finished" : `Finished (${done.length})`}
        </button>
      )}
      {showFinished && done.map((p) => <PlaceRow key={p.item_key} p={p} onChange={onChange} />)}
    </Card>
  );
}

function PlaceRow({ p, onChange }: { p: AudioPlace; onChange: () => void }) {
  const [hist, setHist] = useState<AudioHistoryEntry[] | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const toggle = () => {
    if (!open && hist === null) api.audioHistory(p.item_key).then((r) => setHist(r.history)).catch(() => setHist([]));
    setOpen(!open);
  };
  const act = async (f: () => Promise<unknown>) => { setBusy(true); try { await f(); setHist(null); setOpen(false); onChange(); } finally { setBusy(false); } };
  const pct = p.finished ? 100 : p.duration > 0 ? Math.min(100, (p.position / p.duration) * 100) : 0;
  const pending = p.pending_position ?? null;
  const restore = (h: AudioHistoryEntry) => act(() => api.restoreAudioPlace(p.item_key, h.id));
  return (
    <div className="py-2.5" style={{ borderTop: "1px solid var(--line-soft)" }}>
      <div className="flex items-center gap-3">
        <div className="h-[52px] w-[52px] flex-none overflow-hidden rounded-md" style={{ background: "var(--panel-2)" }}>
          {p.cover_url && <img src={posterThumb(p.cover_url)} alt="" className="h-full w-full object-cover" loading="lazy" />}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold">{p.title}</div>
          {p.author && <div className="truncate text-[11.5px] text-ink-dim">{p.author}</div>}
          <div className="mt-1 h-[4px] overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: p.finished ? "var(--good)" : "var(--accent)" }} />
          </div>
          <div className="mt-0.5 text-[11px] text-ink-faint">
            {p.finished ? "Finished" : `${fmtClock(p.position)}${p.duration > 0 ? ` of ${fmtClock(p.duration)} · ${Math.round(pct)}%` : ""}`} · {fmtAgo(p.updated_at)}{p.device ? ` on ${p.device}` : ""}
          </div>
        </div>
        <div className="flex flex-none flex-col gap-1.5 sm:flex-row">
          <button onClick={() => playAudiobook(p.item_key, { meta: { title: p.title, author: p.author, cover: p.cover_url } })} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={primary}>{p.finished ? "Listen again" : "Play here"}</button>
          <button onClick={toggle} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={ghost}>{open ? "Hide" : "Earlier places"}</button>
        </div>
      </div>
      {p.offer && (
        <OfferBanner offer={p.offer} busy={busy}
          onUse={() => act(() => api.restoreAudioPlace(p.item_key, p.offer!.history_id))}
          onDismiss={() => act(() => api.dismissAudioOffer(p.item_key, p.offer!.history_id))} />
      )}
      {pending !== null && p.finished && (
        // A finished book opened again starts from 0:00 and is held until listening
        // carries on from there — that's "Listen again", not a glitch, so no warning.
        <div className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
          <span>{pending === 0 ? "Listening again from the start" : <>Listening again from <b>{fmtClock(pending)}</b></>} — saved once you've listened for a moment.</span>
          <button onClick={() => act(() => api.acceptAudioJump(p.item_key))} disabled={busy} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>{pending === 0 ? "Start over now" : "Use this spot"}</button>
        </div>
      )}
      {pending !== null && !p.finished && (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2 text-[12px]" style={{ background: "var(--avoid-soft)", border: "1px solid var(--avoid)" }}>
          {pending > p.position
            ? <span>An app jumped ahead to <b>{fmtClock(pending)}</b>, near the end. It's kept once you listen on from there — or use it now.</span>
            : <span>An app jumped back to <b>{fmtClock(pending)}</b>. It's kept once you listen on from there for a bit — or use it now if that was you.</span>}
          <button onClick={() => act(() => api.acceptAudioJump(p.item_key))} disabled={busy} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>Use this spot</button>
        </div>
      )}
      {open && (
        <div className="mt-2 sm:pl-[64px]">
          <PlaceTimeline history={hist} busy={busy} onRestore={restore} />
        </div>
      )}
    </div>
  );
}

// RemovedCard lists places an app removed ("discard progress" in the app). They're kept
// for 90 days, and Restore puts one back exactly where it was.
function RemovedCard({ removed, onChange }: { removed: AudioRemoved[]; onChange: () => void }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const restore = async (key: string) => {
    setBusy(key); setError(null);
    try { await api.undiscardAudio(key); onChange(); } catch (e) { setError((e as Error).message); } finally { setBusy(null); }
  };
  return (
    <Card title="Recently removed" note="Places an app removed in the last 90 days. Restore one to put it back where it was.">
      {removed.map((r) => (
        <div key={r.item_key} className="flex items-center gap-3 py-2" style={{ borderTop: "1px solid var(--line-soft)" }}>
          <div className="h-[40px] w-[40px] flex-none overflow-hidden rounded-md" style={{ background: "var(--panel-2)" }}>
            {r.cover_url && <img src={posterThumb(r.cover_url)} alt="" className="h-full w-full object-cover" loading="lazy" />}
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13px] font-semibold">{r.title}</div>
            <div className="text-[11px] text-ink-faint">{r.finished ? "Finished" : fmtClock(r.position)} · removed {fmtAgo(r.discarded_at)}{r.device ? ` in ${r.device}` : ""}</div>
          </div>
          <button onClick={() => restore(r.item_key)} disabled={busy !== null} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold disabled:opacity-60" style={primary}>{busy === r.item_key ? "Restoring…" : "Restore"}</button>
        </div>
      ))}
      {error && <div className="mt-1 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
    </Card>
  );
}

function MyListeningCard() {
  const [days, setDays] = useState(30);
  const [data, setData] = useState<AudioListening | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { api.myAudioListening(days).then(setData).catch((e) => setError((e as Error).message)); }, [days]);
  const perDay = useMemo(() => new Map((data?.daily ?? []).map((d) => [d.day, d.seconds])), [data]);
  return (
    <Card title="Your listening" right={<DaysPicker days={days} setDays={setDays} />}>
      {!data ? <Loading error={error} /> : (
        <>
          <Totals t={data.totals[0]} />
          <DailyBars since={data.since} days={data.days} perDay={perDay} />
          <div className="mb-1 mt-4 text-[12.5px] font-bold">Sessions</div>
          <Sessions list={data.sessions} />
        </>
      )}
    </Card>
  );
}

