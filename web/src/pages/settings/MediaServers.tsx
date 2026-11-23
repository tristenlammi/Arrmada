import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link, Navigate, useSearchParams } from "react-router-dom";
import { AutoNumber, AutoText, AutoToggle, SavedTick, wholeNumber } from "../../components/settings/auto";
import { Note, Section } from "../../components/settings/ui";
import { ServerTile } from "../../components/brand/ServerBadge";
import { SERVER_LABEL, type MediaServerKind } from "../../components/brand/servers";
import { type InsightsServerStatus, type Person, type PlexConfig, type PlexStatus } from "../../lib/api";
import { peopleApi } from "../../lib/peopleApi";
import { insightsApi } from "../../lib/insightsApi";
import { jellyfinApi, type JellyfinConfig } from "../../lib/jellyfinApi";
import { lanOnly } from "../../lib/lanAddress";
import { LINKS } from "../../lib/links";
import { isAdmin, useMe } from "../../lib/me";
import { setQueryData, useQuery } from "../../lib/query";
import { ago } from "../../lib/taskTime";
import { useAutoSave } from "../../lib/useAutoSave";
import { useSetting } from "../../lib/useSettings";
import { Button, useConfirm, useToast } from "../../ui";
import { JellyfinSignIn } from "./JellyfinSignInCard";
import { LibraryUpdates } from "./LibraryUpdates";
import { requesterPages } from "./roles";
import { AutoApproveChecks, ROLES, jellyfinUnlinkConfirm, plexUnlinkConfirm, typesFromCSV, typesToCSV } from "./People";

// Settings → Media servers: how Arrmada uses each media server, one group per server —
// Plex, then Jellyfin. Each group says which server it is, records what's watched (watch
// monitoring), tells the server what changed (library updates) and, for admins, who may
// sign in with it and which accounts are linked to it. The connections themselves (address,
// token or API key, Test) are the server cards on Settings → Connections; each group's
// server card links to its own. It was the Plex section (/settings/plex): the old addresses
// redirect here, and the cards kept their anchors.
export function MediaServersSettings() {
  const { user } = useMe();
  const admin = isAdmin(user);
  // plex.tv used to send a redirect-mode "Sign in with Plex" back to /settings/plex. One
  // that was under way across an update still finishes: the connection editor picks the
  // PIN up.
  const [params] = useSearchParams();
  const pin = params.get("plexpin");
  if (pin) return <Navigate to={`/settings/connections?plexpin=${encodeURIComponent(pin)}`} replace />;
  return <MediaServersPage admin={admin} />;
}

// The Jellyfin connection's cache key, shared with its card on Connections.
const JELLYFIN_KEY = "jellyfin";

function MediaServersPage({ admin }: { admin: boolean }) {
  // One read of the Plex settings for the server and monitoring cards; a monitoring save
  // answers with the whole config, so the server card's status line follows it.
  const [cfg, setCfg] = useState<PlexConfig | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => { insightsApi.insightsConfig().then(setCfg).catch((e: Error) => setErr(e.message)); }, []);
  // Jellyfin: its connection, and how its monitoring is doing (Insights' server list).
  const jf = useQuery(JELLYFIN_KEY, jellyfinApi.get, { staleMs: 0 });
  const [jfStatus, setJFStatus] = useState<InsightsServerStatus | null>(null);
  const loadJFStatus = useCallback(() => {
    insightsApi.insightsServers().then((ss) => setJFStatus(ss.find((s) => s.server === "jellyfin") ?? null)).catch(() => setJFStatus(null));
  }, []);
  useEffect(() => { loadJFStatus(); }, [loadJFStatus]);

  return (
    <div className="flex flex-col gap-8">
      <nav aria-label="Media servers on this page" className="flex flex-wrap items-center gap-2 text-sm text-ink-dim">
        <span>Jump to</span>
        <a href="#plex-group" className="font-semibold" style={{ color: "var(--accent-text)" }}>Plex</a>
        <span aria-hidden="true">·</span>
        <a href="#jellyfin-group" className="font-semibold" style={{ color: "var(--accent-text)" }}>Jellyfin</a>
        <span className="text-xs text-ink-faint">— connect either, or both, in <Link to={LINKS.mediaServerCards} style={{ color: "var(--accent-text)" }}>Settings → Connections</Link>.</span>
      </nav>

      <ServerGroup server="plex">
        <PlexServer cfg={cfg} err={err} admin={admin} />
        {cfg && <PlexMonitoring cfg={cfg} setCfg={setCfg} />}
        {/* Path mapping and the last scan; its own load and Save. */}
        <LibraryUpdates server="plex" />
        {admin && <PlexSignIn />}
        {admin && <LinkedAccounts server="plex" />}
        {admin && <PlexTautulli />}
      </ServerGroup>

      <ServerGroup server="jellyfin">
        <JellyfinServer
          cfg={jf.data ?? null}
          err={jf.error?.message ?? null}
          status={jfStatus}
          admin={admin}
          onDisconnected={(next) => { setQueryData(JELLYFIN_KEY, next); loadJFStatus(); }}
        />
        {jf.data && <JellyfinWatchLinks cfg={jf.data} admin={admin} />}
        {jf.data && <JellyfinMonitoring cfg={jf.data} onSaved={loadJFStatus} />}
        <LibraryUpdates server="jellyfin" />
        {admin && <JellyfinSignIn />}
        {admin && <LinkedAccounts server="jellyfin" />}
      </ServerGroup>
    </div>
  );
}

// ServerGroup is one server's cards under its mark and name; its id is the jump link's.
function ServerGroup({ server, children }: { server: MediaServerKind; children: ReactNode }) {
  const name = SERVER_LABEL[server];
  return (
    <section id={`${server}-group`} aria-labelledby={`${server}-group-title`} className="flex scroll-mt-20 flex-col gap-4">
      <div className="flex items-center gap-2.5">
        <ServerTile server={server} size={28} />
        <h2 id={`${server}-group-title`} className="m-0 text-lg font-bold">{name}</h2>
      </div>
      {children}
    </section>
  );
}

const STATUS_LINE: Record<PlexStatus, { text: (name: string) => string; color: string }> = {
  unconfigured: { text: () => "Not connected", color: "var(--avoid-text)" },
  off: { text: () => "Connected · watch monitoring is off", color: "var(--avoid-text)" },
  recording: { text: () => "Connected · recording what's watched", color: "var(--good-text)" },
  unreachable: { text: (name) => `${name} isn't answering`, color: "var(--reject-text)" },
};

// StatusLine is a server card's health in one line, with the last error or answer.
function StatusLine({ name, status, lastError, lastPollAt }: { name: string; status: PlexStatus; lastError?: string; lastPollAt?: number }) {
  const line = STATUS_LINE[status] ?? STATUS_LINE.unconfigured;
  return (
    <div className="break-words text-sm" style={{ color: line.color }}>
      {line.text(name)}
      {status === "unreachable" && lastError ? `: ${lastError}` : ""}
      {status === "recording" && lastPollAt ? ` · last answer ${ago(lastPollAt)}` : ""}
    </div>
  );
}

const connectButton = "inline-block rounded-lg px-3.5 py-2 text-base font-semibold";
const connectStyle = { background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" };

// PlexServer says which server Arrmada is connected to and how it's doing, and sends an
// admin to the Plex card on Connections to connect or change it (the connection is the
// admin's: its server decides who may sign in with Plex).
function PlexServer({ cfg, err, admin }: { cfg: PlexConfig | null; err: string | null; admin: boolean }) {
  const connected = !!cfg && cfg.status !== "unconfigured";
  return (
    <Section id="plex-server" title="Plex server" subtitle="Insights records what's watched from it, library updates go to it, and Plex sign-in checks people against it. The connection itself — Sign in with Plex, the address and token, Test — is the Plex card in Settings → Connections.">
      {!cfg ? (
        err ? <p role="alert" className="m-0 text-sm" style={{ color: "var(--reject-text)" }}>Couldn't load the Plex connection: {err}</p> : <p className="m-0 text-sm text-ink-dim">Loading…</p>
      ) : (
        <div className="flex flex-col gap-2">
          {connected && (
            <div className="text-base">
              Connected to <b className="font-semibold">{cfg.server_name || "your Plex server"}</b>{" "}
              <span className="break-all font-mono text-xs text-ink-faint">{cfg.url}</span>
            </div>
          )}
          <StatusLine name="Plex" status={cfg.status} lastError={cfg.last_error} lastPollAt={cfg.last_poll_at} />
          <div>
            {!admin ? (
              <span className="text-xs text-ink-faint">An admin {connected ? "changes" : "connects"} the Plex server.</span>
            ) : connected ? (
              <Link to={LINKS.plexConnection} className="text-sm font-semibold" style={{ color: "var(--accent-text)" }}>Change the connection →</Link>
            ) : (
              <Link to={LINKS.plexConnection} className={connectButton} style={connectStyle}>Connect your Plex server →</Link>
            )}
          </div>
        </div>
      )}
    </Section>
  );
}

// PlexMonitoring is Insights' recorder: on or off, and how often it asks Plex what's
// playing. Each saves itself and sends only its own field, so it can't put back an
// address or token changed meanwhile on Connections.
function PlexMonitoring({ cfg, setCfg }: { cfg: PlexConfig; setCfg: (c: PlexConfig) => void }) {
  const connected = cfg.status !== "unconfigured";
  // Until someone chooses, the server switches monitoring on when Plex is first connected,
  // so an unconnected install shows it on; a connected one that never chose isn't recording.
  const on = useAutoSave(cfg.enabled_set ? cfg.enabled : !connected, (v) => insightsApi.updatePlexMonitoring({ enabled: v }).then(setCfg));
  const poll = useAutoSave(String(cfg.poll_seconds || 5), (v) => insightsApi.updatePlexMonitoring({ poll_seconds: Number(v) }).then(setCfg), {
    validate: wholeNumber(2, 60, "The interval"),
  });
  return (
    <Section id="plex-monitoring" title="Plex watch monitoring" subtitle="Insights' Activity, History, People and Graphs come from this: Arrmada asks Plex what's playing every few seconds and records each play. It switches on by itself when Plex is first connected.">
      <AutoToggle
        label="Record what's watched on Plex"
        hint={connected ? "Off keeps everything already recorded and stops adding to it." : "Starts once your Plex server is connected."}
        save={on}
      />
      <AutoNumber label="Ask Plex every (seconds)" save={poll} min={2} max={60} className="w-[90px] rounded-lg px-3 py-2 text-base">
        <span className="text-xs text-ink-faint">2 to 60. Shorter catches short plays and pauses more exactly; 5 is plenty for most homes.</span>
      </AutoNumber>
    </Section>
  );
}

// JellyfinServer says which Jellyfin server Arrmada is connected to and how it's doing,
// links to its card on Connections, and (admins) disconnects it.
function JellyfinServer({ cfg, err, status, admin, onDisconnected }: {
  cfg: JellyfinConfig | null;
  err: string | null;
  status: InsightsServerStatus | null;
  admin: boolean;
  onDisconnected: (cfg: JellyfinConfig) => void;
}) {
  const confirm = useConfirm();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const connected = !!cfg?.url && cfg.key_set;

  const disconnect = async () => {
    const ok = await confirm({
      title: `Disconnect ${cfg?.server_name || SERVER_LABEL.jellyfin}?`,
      body: `Arrmada forgets its address and API key: Watch links, library updates, watch monitoring and Sign in with ${SERVER_LABEL.jellyfin} stop until you connect it again. Plays already recorded, linked accounts and these settings stay.`,
      confirmLabel: "Disconnect",
      tone: "danger",
    });
    if (!ok) return;
    setBusy(true);
    try {
      onDisconnected(await jellyfinApi.disconnect());
      toast(`${SERVER_LABEL.jellyfin} disconnected`, { tone: "good" });
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section id="jellyfin-server" title={`${SERVER_LABEL.jellyfin} server`} subtitle={`Watch links open it, library updates go to it, Insights can record what's watched on it, and ${SERVER_LABEL.jellyfin} sign-in checks people against it. The connection itself — the address, the API key, Test — is the ${SERVER_LABEL.jellyfin} card in Settings → Connections.`}>
      {!cfg ? (
        err ? <p role="alert" className="m-0 text-sm" style={{ color: "var(--reject-text)" }}>Couldn't load the {SERVER_LABEL.jellyfin} connection: {err}</p> : <p className="m-0 text-sm text-ink-dim">Loading…</p>
      ) : (
        <div className="flex flex-col gap-2">
          {connected && (
            <div className="text-base">
              Connected to <b className="font-semibold">{cfg.server_name || `your ${SERVER_LABEL.jellyfin} server`}</b>{" "}
              <span className="break-all font-mono text-xs text-ink-faint">{cfg.url}</span>
              {cfg.version && <span className="text-xs text-ink-faint"> · {SERVER_LABEL.jellyfin} {cfg.version}</span>}
            </div>
          )}
          <StatusLine
            name={SERVER_LABEL.jellyfin}
            status={connected ? (status?.status ?? (cfg.monitoring_enabled ? "recording" : "off")) : "unconfigured"}
            lastError={status?.last_error}
            lastPollAt={status?.last_poll_at}
          />
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
            {!admin ? (
              <span className="text-xs text-ink-faint">An admin {connected ? "changes" : "connects"} the {SERVER_LABEL.jellyfin} server.</span>
            ) : connected ? (
              <Link to={LINKS.jellyfinConnection} className="text-sm font-semibold" style={{ color: "var(--accent-text)" }}>Change the connection →</Link>
            ) : (
              <Link to={LINKS.jellyfinConnection} className={connectButton} style={connectStyle}>Connect your {SERVER_LABEL.jellyfin} server →</Link>
            )}
            {admin && (connected || !!cfg.url) && (
              <Button size="sm" variant="ghost" busy={busy} busyLabel="Disconnecting…" onClick={() => void disconnect()}>Disconnect Jellyfin</Button>
            )}
          </div>
        </div>
      )}
    </Section>
  );
}

// JellyfinWatchLinks is the address every requester's Jellyfin button opens: the
// connection URL unless an outside address is set. Admins change it here (it saves
// itself); managers see it. Plex needs no such setting: its Watch links go through
// app.plex.tv, which finds the server from anywhere.
function JellyfinWatchLinks({ cfg, admin }: { cfg: JellyfinConfig; admin: boolean }) {
  const shown = cfg.public_url || cfg.url;
  const save = useAutoSave(shown, async (v) => {
    const next = await jellyfinApi.save({ url: cfg.url, public_url: v.trim() });
    setQueryData(JELLYFIN_KEY, next);
  }, { validate: (v) => (v.trim() && !/^https?:\/\/\S+$/i.test(v.trim()) ? "Use a web address like https://jellyfin.example.com." : null) });
  const homeOnly = lanOnly(save.value || cfg.url);
  const help = "What every requester's Jellyfin button opens, on their phone as much as at home. Blank uses the server URL.";
  return (
    <Section id="jellyfin-watch-links" title="Jellyfin Watch links" subtitle="When a request is ready, the requester's notice and the title's page get a Jellyfin button that opens the title in Jellyfin's web app at this address.">
      {!cfg.url ? (
        <p className="m-0 text-sm text-ink-dim">Connect your Jellyfin server first; the address starts as its URL.</p>
      ) : admin ? (
        <AutoText label="Public address for Watch links" save={save} placeholder={cfg.url}><span className="text-xs text-ink-faint">{help}</span></AutoText>
      ) : (
        <div className="flex flex-col gap-1 text-sm">
          <span className="font-semibold">Public address for Watch links</span>
          <span className="break-all font-mono text-ink-dim">{shown}</span>
          <span className="text-xs text-ink-faint">{help} An admin changes it.</span>
        </div>
      )}
      {cfg.url && homeOnly && (
        <Note tone="warn">
          This address only works on your home network, so Watch links won't open on a phone away from home.
          Use the address you reach Jellyfin on from outside, like https://jellyfin.example.com.
        </Note>
      )}
    </Section>
  );
}

// JellyfinMonitoring is Insights' recorder for Jellyfin: on or off, and how often it asks
// what's playing. Off until someone switches it on: Jellyfin keeps no history of its own,
// so Insights' Jellyfin history starts then.
function JellyfinMonitoring({ cfg, onSaved }: { cfg: JellyfinConfig; onSaved: () => void }) {
  const connected = !!cfg.url && cfg.key_set;
  const store = (next: unknown) => {
    // The answer is the whole connection; it keeps the server card and the editor in step.
    if (next && typeof next === "object" && "url" in next) setQueryData(JELLYFIN_KEY, next as JellyfinConfig);
    onSaved();
  };
  const on = useAutoSave(cfg.monitoring_enabled, (v) => insightsApi.updateJellyfinMonitoring({ monitoring_enabled: v }).then(store));
  const poll = useAutoSave(String(cfg.poll_seconds || 10), (v) => insightsApi.updateJellyfinMonitoring({ poll_seconds: Number(v) }).then(store), {
    validate: wholeNumber(2, 60, "The interval"),
  });
  return (
    <Section id="jellyfin-monitoring" title="Jellyfin watch monitoring" subtitle="Arrmada asks Jellyfin what's playing every few seconds and records each play in Insights, next to Plex's. Jellyfin keeps no watch history Arrmada can read, so its history starts when this is switched on. Jellyfin doesn't report buffering, so it's left out of Reliability.">
      <AutoToggle
        label="Record what's watched on Jellyfin"
        hint={connected ? "Off keeps everything already recorded and stops adding to it." : "Starts once your Jellyfin server is connected."}
        save={on}
      />
      <AutoNumber label="Ask Jellyfin every (seconds)" save={poll} min={2} max={60} className="w-[90px] rounded-lg px-3 py-2 text-base">
        <span className="text-xs text-ink-faint">2 to 60. 10 is plenty for most homes.</span>
      </AutoNumber>
    </Section>
  );
}

const LINK_FIELDS = {
  plex: {
    linked: (p: Person) => !!p.plex_linked,
    username: (p: Person) => p.plex_username,
    blocked: (p: Person) => !!p.plex_blocked,
    confirm: plexUnlinkConfirm,
    unlink: { plex_unlink: true },
    subtitle: "Who signs in with which Plex account. A Plex sign-in opens the Arrmada account linked to it; Discover also uses that person's Plex watch history.",
    none: "No account is linked to Plex yet. Someone's first Sign in with Plex links one, and anyone can link their own Plex account themselves.",
    own: "Your own Plex link is in the sidebar and on your account page.",
  },
  jellyfin: {
    linked: (p: Person) => !!p.jellyfin_linked,
    username: (p: Person) => p.jellyfin_username,
    blocked: (p: Person) => !!p.jellyfin_blocked,
    confirm: jellyfinUnlinkConfirm,
    unlink: { jellyfin_unlink: true },
    subtitle: "Who signs in with which Jellyfin user. A Jellyfin sign-in opens the Arrmada account linked to it, and that person's Watch links open Jellyfin first.",
    none: "No account is linked to Jellyfin yet. Someone's first Sign in with Jellyfin links one, and anyone can link their own Jellyfin user from their account page.",
    own: "Your own Jellyfin link is in the sidebar and on your account page.",
  },
} as const;

// LinkedAccounts (admin): which Arrmada accounts are linked to which Plex account or
// Jellyfin user, with Unlink. Blocking one and merging a duplicate sign-in stay under More
// on the person's row in People & access, where the rest of their access is.
function LinkedAccounts({ server }: { server: MediaServerKind }) {
  const { user } = useMe();
  const confirm = useConfirm();
  const toast = useToast();
  const name = SERVER_LABEL[server];
  const f = LINK_FIELDS[server];
  const [people, setPeople] = useState<Person[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const load = useCallback(() => peopleApi.people().then((ps) => { setPeople(ps); setErr(null); }).catch((e: Error) => setErr(e.message)), []);
  useEffect(() => { void load(); }, [load]);
  const what = server === "plex" ? "account" : "user";

  const unlink = async (p: Person) => {
    if (!(await confirm(f.confirm(p)))) return;
    setBusy(p.id);
    try {
      await peopleApi.updateUser(p.id, f.unlink);
      toast(`${p.username} is no longer linked to ${name}.`, { tone: "good" });
      await load();
    } catch (e) { toast((e as Error).message, { tone: "error" }); }
    finally { setBusy(null); }
  };

  const linked = (people ?? []).filter(f.linked);
  return (
    <Section id={`${server}-accounts`} title={`Linked ${name} accounts`} subtitle={f.subtitle}>
      {people === null ? (
        err ? <p role="alert" className="m-0 text-sm" style={{ color: "var(--reject-text)" }}>Couldn't load the accounts: {err}</p> : <p className="m-0 text-sm text-ink-dim">Loading…</p>
      ) : linked.length === 0 ? (
        <p className="m-0 text-sm text-ink-dim">{f.none}</p>
      ) : (
        <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
          {linked.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg px-3 py-2" style={{ background: "var(--panel-2)" }} data-testid={`${server}-account`}>
              <span className="min-w-[8rem] flex-1 truncate text-base font-semibold">
                {p.username}{p.id === user?.id && <span className="font-normal text-ink-faint"> (you)</span>}
              </span>
              <span className="min-w-0 truncate text-sm text-ink-dim">{name} · {f.username(p) || "linked"}</span>
              <span className="text-xs text-ink-faint">{ROLES.find((r) => r.value === p.role)?.label ?? p.role}</span>
              {f.blocked(p) && <span className="rounded-full px-2 py-0.5 text-xs" style={{ background: "var(--reject-soft)", color: "var(--reject-text)" }}>blocked</span>}
              {p.id !== user?.id && (
                <Button size="sm" busy={busy === p.id} busyLabel="Unlinking…" disabled={busy !== null} onClick={() => void unlink(p)} aria-label={`Unlink ${p.username}'s ${name} ${what}`}>Unlink</Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <p className="m-0 text-xs text-ink-faint">
        To block a {name} {what} from signing in, or merge a duplicate {name} sign-in into the right account, use More on their row in <Link to={LINKS.users} style={{ color: "var(--accent-text)" }}>Settings → People &amp; access</Link>. {f.own}
      </p>
    </Section>
  );
}

// PlexTautulli (admin) points at the Tautulli import, which lives with the other
// migrations in Settings → Import & migrate.
function PlexTautulli() {
  return (
    <Section id="plex-tautulli" title="Import from Tautulli" subtitle="Moving from Tautulli? Bring its watch history into Insights so the stats and graphs aren't empty on day one. The import, its past runs and Undo are in Settings → Import & migrate.">
      <div>
        <Link to={LINKS.tautulliImport} className="text-sm font-semibold" style={{ color: "var(--accent-text)" }}>Open the Tautulli import →</Link>
      </div>
    </Section>
  );
}

// PlexSignIn (admin): whether family members may sign in with Plex, and what a new Plex
// sign-in may request without asking. Each control saves itself.
function PlexSignIn() {
  const { booksEnabled } = useMe();
  const allow = useSetting("plex_login_enabled");
  const staff = useSetting("plex_signin_staff");
  const autoTypes = useSetting("plex_login_auto_approve_types", { keepOnError: false });
  return (
    <Section id="plex-sign-in" title="Plex sign-in" subtitle={<>Let your Plex Home members and shared users sign in with Plex — no accounts to hand out. They get a Requester account ({requesterPages(booksEnabled)}), and only people with access to your Plex server get in. Needs the Plex server above to be connected.</>}>
      <AutoToggle label="Allow Sign in with Plex" hint="Adds a 'Sign in with Plex' button to the login page." save={allow} />
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2 text-base font-semibold">Auto-approve new Plex sign-ins' requests <SavedTick save={autoTypes} /></div>
        <AutoApproveChecks
          value={typesFromCSV(autoTypes.value ?? "movie")}
          onChange={(v) => void autoTypes.commit(typesToCSV(v))}
          books={booksEnabled}
          label="New Plex sign-ins auto-approve"
        />
        <p className="m-0 text-xs text-ink-faint">Ticked types download straight away for someone who signs in with Plex for the first time; the rest wait for you. A series request can pull every season of a long show, so it starts with movies only. Existing accounts keep their own settings — change them in Settings → People & access.</p>
      </div>
      <AutoToggle
        label="Let staff sign in with Plex"
        hint="Sign in with Plex may open an admin or manager account linked to that Plex account. Your own first Plex sign-in then links to your admin account, if it's the only one. Off: staff sign in with their password."
        save={staff}
      />
      {staff.value && (
        <Note tone="warn">Anyone who controls a linked Plex account gets that account — for an admin, everything in Arrmada. Keep the Plex account behind two-factor sign-in at plex.tv.</Note>
      )}
    </Section>
  );
}
