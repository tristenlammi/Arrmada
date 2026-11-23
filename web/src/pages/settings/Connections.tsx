import { lazy, Suspense, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { CategoryLine, ClientEditor, ClientForm, clientBody } from "../../components/connections/ClientEditor";
import { ConnectionCard, type CardFix, type CardTest } from "../../components/connections/ConnectionCard";
import { ConnectionSection } from "../../components/connections/ConnectionSection";
import { IndexerEditor, indexerBody } from "../../components/connections/IndexerEditor";
import { MediaServerCard } from "../../components/connections/MediaServerCard";
import { MusicBrainzCard } from "../../components/connections/MusicBrainzCard";
import { DiscoveryRegion, KeyEditor, keyRowsFor, useAPIKeys } from "../../components/connections/KeyEditor";
import { ProwlarrPanel } from "../../components/connections/ProwlarrPanel";
import { type DownloadClient, type Indexer } from "../../lib/api";
import { downloadClientApi } from "../../lib/downloadClientApi";
import { indexerApi } from "../../lib/indexerApi";
import { connectionsApi, type ConnectionItem, type ConnectionSectionData, type ConnectionSectionId } from "../../lib/connectionsApi";
import { jellyfinApi } from "../../lib/jellyfinApi";
import { SERVER_LABEL } from "../../components/brand/servers";
import { isAdmin, useMe } from "../../lib/me";
import { hasUsenetClient, isUsenetClient } from "../../lib/network";
import { useQuery } from "../../lib/query";
import { useLiveRefresh } from "../../lib/useLiveQuery";
import { Button, ErrorState, Skeleton, StaleBanner, useConfirm, type MenuItem } from "../../ui";
import { JellyfinConnect } from "./JellyfinConnection";
import { PlexConnect } from "./PlexConnection";

// The add-indexer catalogue loads when it's opened.
const AddIndexerModal = lazy(() => import("../../components/connections/AddIndexerModal"));

const NO_SECTIONS: ConnectionSectionData[] = [];
// The Prowlarr card's id (and anchor): the catalogue's Prowlarr tile opens it.
const PROWLARR_CARD = "prowlarr";
// The media server cards' ids (and anchors): LINKS.plexConnection lands on Plex's. A link
// to one that isn't connected yet opens its editor.
const PLEX_CARD = "plex";
const JELLYFIN_CARD = "jellyfin";
const SERVER_CARDS = [PLEX_CARD, JELLYFIN_CARD];
const MEDIA_SERVERS_ANCHOR = "media-servers";

// Jellyfin's card before the server lists one (an older server, or none saved yet): not
// connected, with its Connect button.
const JELLYFIN_UNSET: ConnectionItem = {
  kind: "jellyfin", ref: "default", card: JELLYFIN_CARD, name: "Jellyfin",
  configured: false, enabled: false, state: "unknown", testable: false,
};
const isServer = (it: ConnectionItem) => it.kind === "plex" || it.kind === "jellyfin";

// What each section holds, under its title.
const SUBTITLES: Record<ConnectionSectionId, string> = {
  search: "Where releases are found: Prowlarr, FlareSolverr for Cloudflare-protected trackers, and every indexer. Every module searches through these.",
  downloads: "Where grabbed releases are sent to download. qBittorrent for torrents, SABnzbd for Usenet.",
  metadata: "The API keys titles, ratings, books and subtitles come from. A key saved here works at once, no restart, and replaces one set at install; it's never shown back, only whether it's set.",
  other: "Where alerts go, and the server listening apps connect to. Each is set up on its own page.",
};

// The old "other" section lost its media server to the section above; the server may
// still send the old title.
const TITLES: Partial<Record<ConnectionSectionId, string>> = { other: "Alerts and apps" };

// Each section's anchor. They differ from the API's section ids where those would clash
// with another card's anchor ("downloads" is a section, "metadata" a card under Naming);
// the metadata keys keep the API keys card's old anchor, so old links still land on them.
const SECTION_ANCHOR: Record<ConnectionSectionId, string> = {
  search: "search",
  downloads: "download-clients",
  metadata: "api-keys",
  other: "other-connections",
};

// Live refresh: a status change is announced on the tab's shared websocket; the list is read
// again a second after the first one, so a burst (three sweeps at once) is one read. A slow
// poll covers a dropped socket.
const SLOW_POLL_MS = 60_000;

// Settings → Connections: every integration Arrmada talks to as a card with a status dot,
// its last success or error, and a Test. The list comes from what the server already knows,
// so opening the page calls nothing outside Arrmada; Test asks the one connection. Indexers,
// download clients and Prowlarr are added, edited, switched off and deleted here (the old
// Indexers and Download clients pages redirect here); admins edit the metadata keys and the
// FlareSolverr URL inside their cards. The media servers (Plex and Jellyfin) sit side by
// side at the top and are connected inside their cards (how Arrmada uses them is Settings
// → Media servers); alerts and the audiobook server link to their own pages.
export function ConnectionsSettings() {
  const { user } = useMe();
  const admin = isAdmin(user);
  const confirm = useConfirm();
  const q = useQuery("connections", () => connectionsApi.list(), { staleMs: 0 });
  // The full rows the editors and the Disable/Delete actions need.
  const ix = useQuery("indexers", () => indexerApi.indexers(), { staleMs: 0 });
  const dl = useQuery("download-clients", () => downloadClientApi.downloadClientList(), { staleMs: 0 });
  const sections = q.data ?? NO_SECTIONS;
  // A usenet client opens the Newznab tile in the add-indexer catalogue.
  const usenet = hasUsenetClient(dl.data?.clients);
  const plexItem = sections.flatMap((s) => s.items).find((x) => x.kind === "plex");
  const jellyfinItem = sections.flatMap((s) => s.items).find((x) => x.kind === "jellyfin") ?? JELLYFIN_UNSET;
  // Which Jellyfin server it is (name and version) comes from its own settings, read once
  // it's connected; the card's status comes from the list like every other card.
  const jf = useQuery("jellyfin", jellyfinApi.get, { enabled: jellyfinItem.configured, staleMs: 60_000 });
  const error = q.error?.message ?? null;
  const [tests, setTests] = useState<Record<string, CardTest>>({});
  const [notices, setNotices] = useState<Record<string, string>>({});
  const { hash, search } = useLocation();
  // Back from plex.tv after a redirect-mode Sign in with Plex (?plexpin=): the Plex editor
  // opens at once so it can finish that sign-in.
  const [editing, setEditing] = useState<string | null>(() => (new URLSearchParams(search).has("plexpin") ? PLEX_CARD : null));
  const [adding, setAdding] = useState<"indexer" | "client" | null>(null);
  const [restoring, setRestoring] = useState(false);
  const [restoreErr, setRestoreErr] = useState<string | null>(null);

  const refetch = q.refetch;
  const refetchIx = ix.refetch;
  const refetchDl = dl.refetch;
  const refresh = useCallback(() => { void refetch(); }, [refetch]);
  const refreshAll = useCallback(async () => { await Promise.all([refetch(), refetchIx(), refetchDl()]); }, [refetch, refetchIx, refetchDl]);
  const keys = useAPIKeys(admin, refresh);

  // The tab's one shared socket (lib/live): an integration's status changing, or the
  // health checks' findings, re-read the list.
  useLiveRefresh(refetch, { topics: ["integration.status", "health.changed"], debounceMs: 1000, fallbackMs: SLOW_POLL_MS });

  // A link to one card (a health warning's Fix) lands on it once the cards exist; the
  // hub's own scroll ran before they loaded. A link to the Plex card while Plex isn't
  // connected yet ("Connect your Plex server" in Insights or Settings → Media servers) is there to
  // connect it, so its editor opens too.
  const scrolled = useRef("");
  useEffect(() => {
    if (!q.data || !hash || scrolled.current === hash) return;
    scrolled.current = hash;
    const target = hash.slice(1);
    if (SERVER_CARDS.includes(target)) {
      const it = q.data.flatMap((s) => s.items).find((x) => x.card === target) ?? (target === JELLYFIN_CARD ? JELLYFIN_UNSET : undefined);
      if (it && !it.configured) setEditing((e) => e ?? target);
    }
    document.getElementById(hash.slice(1))?.scrollIntoView({ block: "start" });
  }, [q.data, hash]);

  const setNotice = (card: string, msg: string | null) =>
    setNotices((n) => { const next = { ...n }; if (msg) next[card] = msg; else delete next[card]; return next; });

  const runTest = async (kind: ConnectionItem["kind"], ref: string, card: string) => {
    setTests((t) => ({ ...t, [card]: { running: true } }));
    try {
      const r = await connectionsApi.test(kind, ref);
      setTests((t) => ({ ...t, [card]: { ok: r.ok, detail: r.detail } }));
      // The answer carries the refreshed card: update it in place.
      q.mutate((old) => (old ?? []).map((s) => ({ ...s, items: s.items.map((x) => (x.card === card ? r.item : x)) })));
    } catch (e) {
      setTests((t) => ({ ...t, [card]: { ok: false, detail: (e as Error).message } }));
    }
  };

  // A saved client is tested at once, so a wrong URL or password shows now instead of at
  // the first grab.
  const clientSaved = (c: DownloadClient) => {
    setAdding(null);
    setEditing(null);
    void refreshAll().then(() => runTest("download_client", String(c.id), `download_client-${c.id}`));
  };

  const restoreBundled = async (kind?: "sabnzbd") => {
    setRestoring(true);
    setRestoreErr(null);
    try {
      await downloadClientApi.restoreBundledClient(kind);
      await refreshAll();
    } catch (e) {
      setRestoreErr(`Couldn't restore it: ${(e as Error).message}`);
    } finally {
      setRestoring(false);
    }
  };

  if (!q.data) {
    return error ? <ErrorState what="connections" message={error} onRetry={refetch} busy={q.loading} /> : <Skeleton variant="list" count={4} />;
  }

  const indexerOf = (it: ConnectionItem) => ix.data?.find((i) => String(i.id) === it.ref);
  const clientOf = (it: ConnectionItem) => dl.data?.clients.find((c) => String(c.id) === it.ref);

  // Switching an indexer or client off or on saves at once; its key or password goes back
  // blank, which keeps the stored one. A Prowlarr row switched off here stays off through
  // syncs (the server marks it switched off by you).
  const toggleIndexer = async (it: ConnectionItem, idx: Indexer) => {
    setNotice(it.card, null);
    try {
      await indexerApi.updateIndexer(idx.id, indexerBody(idx, { enabled: !idx.enabled }));
      await refreshAll();
    } catch (e) {
      setNotice(it.card, `Couldn't ${idx.enabled ? "disable" : "enable"} it: ${(e as Error).message}`);
    }
  };
  const toggleClient = async (it: ConnectionItem, c: DownloadClient) => {
    setNotice(it.card, null);
    try {
      await downloadClientApi.updateDownloadClient(c.id, clientBody(c, { enabled: !c.enabled }));
      await refreshAll();
    } catch (e) {
      setNotice(it.card, `Couldn't ${c.enabled ? "disable" : "enable"} it: ${(e as Error).message}`);
    }
  };

  // Deleting asks first and says what happens to what's already there; a refusal from the
  // server shows on the card instead of vanishing.
  const removeIndexer = async (it: ConnectionItem, idx: Indexer) => {
    const ok = await confirm({
      title: `Delete ${idx.name}?`,
      // Nothing from a usenet indexer seeds, so there's no seed rule to fall back.
      body: idx.kind === "newznab" ? "Searches stop using it." : "Searches stop using it. Seed rules on downloads already grabbed through it fall back to the 14-day default.",
      confirmLabel: "Delete",
      tone: "danger",
    });
    if (!ok) return;
    setNotice(it.card, null);
    try {
      await indexerApi.deleteIndexer(idx.id);
      if (editing === it.card) setEditing(null);
      await refreshAll();
    } catch (e) {
      setNotice(it.card, `Couldn't delete: ${(e as Error).message}`);
    }
  };
  const removeClient = async (it: ConnectionItem, c: DownloadClient) => {
    const already = isUsenetClient(c) ? "Jobs already there are untouched." : "Torrents already there are untouched.";
    const ok = await confirm({
      title: `Remove ${c.name}?`,
      body: c.bundled
        ? `Arrmada stops sending downloads to it. ${already} It stays removed after a restart; you can restore it from this page.`
        : `Arrmada stops sending downloads to it. ${already}`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setNotice(it.card, null);
    try {
      await downloadClientApi.deleteDownloadClient(c.id);
      if (editing === it.card) setEditing(null);
      await refreshAll();
    } catch (e) {
      setNotice(it.card, `Couldn't remove: ${(e as Error).message}`);
    }
  };

  // The editor a card opens inside itself, if it has one for this viewer.
  const editorFor = (it: ConnectionItem): ReactNode | undefined => {
    const done = () => { setEditing(null); void refreshAll(); };
    switch (it.kind) {
      case "indexer": {
        const idx = indexerOf(it);
        return idx ? <IndexerEditor key={idx.id} idx={idx} usenet={usenet} onSaved={done} onChanged={refreshAll} /> : undefined;
      }
      case "download_client": {
        const c = clientOf(it);
        return c ? <ClientEditor key={c.id} client={c} onSaved={clientSaved} onChanged={() => void refreshAll()} admin={admin} /> : undefined;
      }
      case "prowlarr":
        return <ProwlarrPanel onSynced={() => void refreshAll()} />;
      case "apikey":
        return admin ? <KeyEditor ids={keyRowsFor(it.ref)} ctl={keys} /> : undefined;
      case "flaresolverr":
        return admin ? <KeyEditor ids={["flaresolverr"]} ctl={keys} /> : undefined;
      // A media server connection is the admin's (PUT /plex and /jellyfin are admin-only):
      // its server decides who may sign in with it. A manager sees the card and can Test
      // the saved connection (POST /connections/{kind}/{ref}/test is a manager's).
      case "plex":
        return admin ? <PlexConnect onChanged={refresh} /> : undefined;
      case "jellyfin":
        return admin ? <JellyfinConnect onChanged={refresh} /> : undefined;
    }
    return undefined;
  };

  const menuFor = (it: ConnectionItem, open: boolean, toggle: () => void): MenuItem[] | undefined => {
    const idx = it.kind === "indexer" ? indexerOf(it) : undefined;
    const c = it.kind === "download_client" ? clientOf(it) : undefined;
    const row = idx ?? c;
    if (!row) return undefined;
    return [
      { label: open ? "Close editor" : "Edit", onSelect: toggle },
      {
        label: row.enabled ? "Disable" : "Enable",
        busyLabel: row.enabled ? "Disabling…" : "Enabling…",
        onSelect: () => (idx ? toggleIndexer(it, idx) : toggleClient(it, c as DownloadClient)),
      },
      { label: "Delete", tone: "danger", onSelect: () => void (idx ? removeIndexer(it, idx) : removeClient(it, c as DownloadClient)) },
    ];
  };

  const card = (it: ConnectionItem) => {
    const editor = editorFor(it);
    const open = editing === it.card && !!editor;
    const toggle = () => setEditing(open ? null : it.card);
    return (
      <ConnectionCard
        key={it.card}
        item={it}
        test={tests[it.card]}
        onTest={() => void runTest(it.kind, it.ref, it.card)}
        fix={editor ? { onClick: () => setEditing(it.card) } : linkFor(it)}
        onEdit={editor ? toggle : undefined}
        editing={open}
        menu={menuFor(it, open, toggle)}
        notice={notices[it.card]}
        targeted={hash === `#${it.card}`}
      >
        {open && editor}
      </ConnectionCard>
    );
  };

  const headerActions = (id: ConnectionSectionId): ReactNode => {
    switch (id) {
      case "search":
        return (
          <Button size="sm" variant="primary" onClick={() => setAdding("indexer")}>+ Add indexer</Button>
        );
      case "downloads":
        return (
          <>
            {dl.data?.can_restore_bundled && (
              <Button size="sm" variant="secondary" onClick={() => void restoreBundled()} busy={restoring} busyLabel="Restoring…">Restore bundled qBittorrent</Button>
            )}
            {dl.data?.can_restore_bundled_sabnzbd && (
              <Button size="sm" variant="secondary" onClick={() => void restoreBundled("sabnzbd")} busy={restoring} busyLabel="Restoring…">Restore bundled SABnzbd</Button>
            )}
            <Button size="sm" variant={adding === "client" ? "secondary" : "primary"} onClick={() => setAdding(adding === "client" ? null : "client")}>
              {adding === "client" ? "Cancel" : "+ Add client"}
            </Button>
          </>
        );
    }
    return undefined;
  };

  const above = (id: ConnectionSectionId): ReactNode => {
    if (id === "downloads") {
      return (
        <>
          {restoreErr && <p role="alert" className="m-0 text-sm" style={{ color: "var(--reject-text)" }}>{restoreErr}</p>}
          {dl.data?.can_restore_bundled && (
            <p className="m-0 text-sm text-ink-dim">The qBittorrent that comes with Arrmada was removed, so it gets no downloads.</p>
          )}
          {dl.data?.can_restore_bundled_sabnzbd && (
            <p className="m-0 text-sm text-ink-dim">The SABnzbd that runs alongside Arrmada was removed, so it gets no Usenet downloads.</p>
          )}
          {adding === "client" && <ClientForm onSaved={clientSaved} />}
        </>
      );
    }
    return undefined;
  };

  const below = (id: ConnectionSectionId): ReactNode => {
    if (id === "downloads" && dl.data?.categories) return <CategoryLine c={dl.data.categories} usenet={usenet} />;
    if (id === "metadata" && admin) {
      return (
        <>
          <DiscoveryRegion />
          <MusicBrainzCard />
        </>
      );
    }
    return undefined;
  };

  const serverCard = (server: "plex" | "jellyfin", it: ConnectionItem) => {
    const editor = editorFor(it);
    const open = editing === it.card && !!editor;
    const identity = server === "jellyfin" && jf.data?.server_name
      ? `${jf.data.server_name}${jf.data.version ? ` · ${SERVER_LABEL.jellyfin} ${jf.data.version}` : ""}`
      : undefined;
    return (
      <MediaServerCard
        key={it.card}
        server={server}
        item={it}
        identity={identity}
        test={tests[it.card]}
        onTest={() => void runTest(it.kind, it.ref, it.card)}
        onEdit={editor ? () => setEditing(open ? null : it.card) : undefined}
        editing={open}
        targeted={hash === `#${it.card}`}
      >
        {open && editor}
      </MediaServerCard>
    );
  };

  // The catalogue's Prowlarr tile: Prowlarr isn't added like an indexer, its card pulls
  // Prowlarr's indexers in, so the tile opens that card.
  const openProwlarr = () => {
    setAdding(null);
    setEditing(PROWLARR_CARD);
    document.getElementById(PROWLARR_CARD)?.scrollIntoView({ block: "start" });
  };

  return (
    <div className="flex flex-col gap-8">
      {error && <StaleBanner message={error} onRetry={refetch} />}
      {adding === "indexer" && (
        <Suspense fallback={null}>
          <AddIndexerModal usenet={usenet} onClose={() => setAdding(null)} onAdded={() => { setAdding(null); void refreshAll(); }} onProwlarr={openProwlarr} />
        </Suspense>
      )}
      <ConnectionSection
        id={MEDIA_SERVERS_ANCHOR}
        title="Media servers"
        subtitle="Where your library is watched. Connect Plex, Jellyfin or both: a request is ready as soon as either has it, and each one gets its own Watch button."
      >
        {plexItem && serverCard("plex", plexItem)}
        {serverCard("jellyfin", jellyfinItem)}
      </ConnectionSection>
      {sections.map((s) => ({ ...s, items: s.items.filter((x) => !isServer(x)) })).map((s) => (
        <ConnectionSection
          key={s.id}
          id={SECTION_ANCHOR[s.id] ?? s.id}
          title={TITLES[s.id] ?? s.title}
          subtitle={SUBTITLES[s.id] ?? ""}
          actions={headerActions(s.id)}
          above={above(s.id)}
          below={below(s.id)}
        >
          {s.items.length === 0 ? (
            <p className="m-0 text-sm text-ink-dim">
              {s.id === "downloads" ? "No download clients yet. Add qBittorrent or SABnzbd to start downloading." : "Nothing here yet."}
            </p>
          ) : (
            s.items.map(card)
          )}
        </ConnectionSection>
      ))}
    </div>
  );
}

// linkFor is where a card that isn't edited here goes: alerts and the audiobook server
// have their own pages. A manager's key cards have none — keys are the admin's.
function linkFor(it: ConnectionItem): CardFix | undefined {
  return it.fix_path ? { to: it.fix_path } : undefined;
}
