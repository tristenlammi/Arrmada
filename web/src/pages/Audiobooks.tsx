import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { ApiError, api, type AudioListening, type AudioPlace, type AudioRemoved, type AudioShelf, type MyAudio } from "../lib/api";
import { useMe } from "../lib/me";
import { posterThumb } from "../lib/img";
import { lazyPage } from "../lib/lazyPage";
import { useTabParam } from "../lib/useTabParam";
import { TabPanel, Tabs, type TabItem } from "../ui/Tabs";
import { useConfirm } from "../ui/Confirm";
import { PageHeader } from "../components/PageHeader";
import { ShelfRow } from "../components/audiobooks/ShelfRow";
import { AllAudiobooks } from "../components/audiobooks/AllAudiobooks";
import { BookSheet } from "../components/audiobooks/BookSheet";
import { Cover } from "../components/audiobooks/BookCard";
import { Card, DailyBars, DaysPicker, Field, Loading, Sessions, Totals, addresses, danger, fmtAgo, fmtClock, ghost, inputStyle, primary } from "./audiobooks/shared";

// The admin tabs are their own chunk: requesters never download them.
const AudiobooksAdmin = lazyPage(() => import("./AudiobooksAdmin"), "AudiobooksAdmin");

// Audiobooks is where people listen. Everyone gets Listen — their shelves, the whole
// library and a sheet for each book, played right here in the mini-player — and Apps &
// devices: how to connect a listening app (Lissen, or anything that speaks
// Audiobookshelf), their audiobook password and their devices. Admins also get the server
// switch, who may connect, everyone's listening (how much and when — never what) and the
// Audiobookshelf import. Nobody sees anyone else's places.

type Tab = "listen" | "apps" | "server" | "people" | "listening" | "import";
const TABS: TabItem<Tab>[] = [
  { key: "listen", label: "Listen" },
  { key: "apps", label: "Apps & devices" },
  { key: "server", label: "Server" },
  { key: "people", label: "People" },
  { key: "listening", label: "Listening" },
  { key: "import", label: "Import" },
];
const ALL_TABS = TABS.map((t) => t.key);
const EVERYONE: readonly Tab[] = ["listen", "apps"];

export function Audiobooks({ chrome = true }: { chrome?: boolean }) {
  const { user } = useMe();
  const admin = user?.role === "admin";
  // A link to an admin tab from anyone else lands on Listen.
  const allowed = admin ? ALL_TABS : EVERYONE;
  const [tab, setTab] = useTabParam(allowed, "listen");
  const tabs = TABS.filter((t) => allowed.includes(t.key));
  return (
    <>
      {chrome && <PageHeader title="Audiobooks" />}
      <div className="mx-auto w-full max-w-[980px] px-4 py-6 sm:px-6">
        {!chrome && <h1 className="m-0 mb-1 text-[18px] font-bold">Audiobooks</h1>}
        <p className="m-0 mb-4 max-w-[70ch] text-[12.5px] text-ink-dim">
          Listen right here, or in a listening app. Your place follows you between them.
        </p>
        <Tabs tabs={tabs} value={tab} onChange={setTab} idPrefix="audiobooks" label="Audiobooks sections" />
        <TabPanel idPrefix="audiobooks" value={tab}>
          {tab === "listen" ? <ListenView /> : tab === "apps" ? <AppsView /> : (
            <Suspense fallback={<Loading />}>
              <AudiobooksAdmin tab={tab} />
            </Suspense>
          )}
        </TabPanel>
      </div>
    </>
  );
}

// --- Listen -----------------------------------------------------------------------

function ListenView() {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const book = params.get("book") ?? "";
  const [data, setData] = useState<MyAudio | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [shelves, setShelves] = useState<AudioShelf[] | null>(null);
  const [blocked, setBlocked] = useState<string | null>(null);
  const loadMine = useCallback(() => api.myAudio().then((d) => { setData(d); setError(null); }).catch((e) => setError((e as Error).message)), []);
  const loadShelves = useCallback(() => api.audioShelves().then((r) => { setShelves(r.shelves); setBlocked(null); }).catch((e) => {
    // 403: switched off, or this account isn't allowed — said in words below.
    if (e instanceof ApiError && e.status === 403) setBlocked(e.message);
    else setShelves([]);
  }), []);
  useEffect(() => { void loadMine(); void loadShelves(); }, [loadMine, loadShelves]);

  // The sheet lives in the address (?book=), so Back closes it and a link opens it. Closing
  // one this page opened steps back over its entry, so Back afterwards leaves the page
  // rather than opening the sheet again.
  const openBook = (key: string) => setParams((p) => { const n = new URLSearchParams(p); n.set("book", key); return n; }, { state: { bookSheet: true } });
  const closeBook = () => {
    if ((location.state as { bookSheet?: boolean } | null)?.bookSheet) navigate(-1);
    else setParams((p) => { const n = new URLSearchParams(p); n.delete("book"); return n; }, { replace: true });
  };
  const changed = () => { void loadMine(); void loadShelves(); };

  if (!data) return <Loading error={error} />;
  if (!data.allowed) {
    return <Card title="Not available">
      <p className="m-0 text-[12.5px] text-ink-dim">Your account isn't set up for audiobooks. Ask an admin.</p>
    </Card>;
  }
  if (!data.enabled || blocked) {
    return <Card title="Switched off">
      <p className="m-0 text-[12.5px] text-ink-dim">Audiobooks are switched off at the moment, so there's nothing to listen to yet.</p>
    </Card>;
  }
  const places = new Map(data.places.map((p) => [p.item_key, p]));
  return (
    <div className="flex flex-col gap-6">
      <CheckPlaces places={data.places} onOpen={openBook} />
      {shelves === null ? <Loading /> : shelves.map((s) => <ShelfRow key={s.id} shelf={s} onOpen={openBook} />)}
      <AllAudiobooks onOpen={openBook} />
      {data.removed?.length > 0 && <RemovedCard removed={data.removed} onChange={changed} />}
      <MyListeningCard />
      {book && (
        <BookSheet key={book} itemKey={book} place={places.get(book)} onClose={closeBook} onOpenBook={openBook} onChanged={changed} />
      )}
    </div>
  );
}

// CheckPlaces lists books whose place needs a look: a later spot an app sent that wasn't
// used, or a jump that's held until listening carries on. The book sheet has the actions.
function CheckPlaces({ places, onOpen }: { places: AudioPlace[]; onOpen: (key: string) => void }) {
  const list = places.filter((p) => p.offer || (p.pending_position != null && !p.finished));
  if (list.length === 0) return null;
  return (
    <Card title="Check your place" note="Something sent a different spot for these. Open one to keep the right place.">
      {list.map((p) => (
        <button key={p.item_key} onClick={() => onOpen(p.item_key)} className="flex w-full items-center gap-3 py-2 text-left" style={{ borderTop: "1px solid var(--line-soft)" }}>
          <Cover src={p.cover_url ? posterThumb(p.cover_url) : undefined} title={p.title} className="w-[44px] flex-none" small />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[13px] font-semibold">{p.title}</span>
            <span className="block truncate text-[11.5px] text-ink-dim">
              {p.offer
                ? <>A later spot (<b>{fmtClock(p.offer.position)}</b>) wasn't used — use it?</>
                : <>A jump to <b>{fmtClock(p.pending_position ?? 0)}</b> is waiting to be kept</>}
            </span>
          </span>
          <span className="flex-none text-[12px] font-semibold" style={{ color: "var(--accent)" }}>Open</span>
        </button>
      ))}
    </Card>
  );
}

// --- Apps & devices -----------------------------------------------------------------

function AppsView() {
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
      <div className="rounded-xl px-4 py-2.5 text-[12px] text-ink-dim" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
        Your places moved to Listen: open a book there to see where you're up to and put back an earlier place.
      </div>
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
      {data.devices.length > 0 && <DevicesCard data={data} onChange={load} />}
    </div>
  );
}

function DevicesCard({ data, onChange }: { data: MyAudio; onChange: () => void }) {
  return (
    <Card title="Your devices" note="Apps signed in as you. Sign one out if you lose the phone or stop using it.">
      {data.devices.map((d) => (
        <div key={d.id} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line-soft)" }}>
          <div className="min-w-0">
            <div className="truncate font-semibold">{d.device || d.client || "App"}{d.client && d.device && d.device !== d.client ? <span className="font-normal text-ink-faint"> · {d.client}</span> : null}</div>
            <div className="text-[11px] text-ink-faint">Signed in {fmtAgo(d.created_at)} · last used {fmtAgo(d.last_used_at)}</div>
          </div>
          <button onClick={() => api.revokeMyDevice(d.id).then(onChange)} className="flex-none rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={danger}>Sign out</button>
        </div>
      ))}
    </Card>
  );
}

function PasswordCard({ data, onSaved }: { data: MyAudio; onSaved: (d: MyAudio) => void }) {
  const confirm = useConfirm();
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
    const ok = await confirm({ title: "Remove your audiobook password?", body: "Your apps will be signed out and can't connect until you set a new one.", confirmLabel: "Remove", tone: "danger" });
    if (!ok) return;
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
