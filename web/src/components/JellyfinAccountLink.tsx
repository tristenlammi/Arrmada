import { lazy, Suspense, useState } from "react";
import { useMe } from "../lib/me";
import { jellyfinSignIn, type JellyfinLink, type JellyfinLinkConflict, type LinkResult } from "../lib/jellyfinSignIn";
import { setQueryData, useQuery } from "../lib/query";
import { useConfirm, useToast } from "../ui";
import { JellyfinSheet } from "./JellyfinSignInSheet";
import { SERVER_LABEL } from "./brand/servers";

// "Jellyfin: Link / Linked as <name> · Unlink" for whoever is signed in, on the Me page and
// in the staff sidebar's footer, each under the Plex line. Linking is signing in to Jellyfin once (password or Quick Connect): the
// next Sign in with Jellyfin then lands in this account, and Watch links put Jellyfin first.
// Its own chunk, with the sheet behind it.

const MergeDialog = lazy(() => import("./PlexMergeDialog"));

const LINK_KEY = "me:jellyfin";

export default function JellyfinAccountLink({ variant = "me" }: { variant?: "sidebar" | "me" }) {
  const toast = useToast();
  const confirm = useConfirm();
  const { user } = useMe();
  const { data: link, loading, error } = useQuery<JellyfinLink>(LINK_KEY, jellyfinSignIn.mine, { staleMs: 60_000 });
  const setLink = (l: JellyfinLink) => setQueryData(LINK_KEY, l);
  const load = () => jellyfinSignIn.mine().then(setLink).catch(() => {});
  const [sheet, setSheet] = useState(false);
  const [conflict, setConflict] = useState<JellyfinLinkConflict | null>(null);
  const [merging, setMerging] = useState<number | null>(null);
  // "Jellyfin", or "Emby" when the Jellyfin slot is connected to Emby.
  const name = SERVER_LABEL.jellyfin;

  const done = (r: LinkResult) => {
    setSheet(false);
    if ("conflict" in r) { setConflict(r.conflict); return; }
    setConflict(null);
    toast(`Linked to ${name}${r.jellyfin_username ? ` as ${r.jellyfin_username}` : ""}`, { tone: "good" });
    void load();
  };

  const unlink = async () => {
    const ok = await confirm({
      title: `Unlink ${name}?`,
      body: `Sign in with ${name} won't open this account any more. You can link it again any time.`,
      confirmLabel: "Unlink",
    });
    if (!ok) return;
    try { const l = await jellyfinSignIn.unlink(); setLink({ ...l, available: link?.available ?? true, quickconnect: link?.quickconnect ?? false }); toast(`${name} unlinked`, { tone: "good" }); }
    catch (e) { toast((e as Error).message, { tone: "error" }); }
  };

  const sidebar = variant === "sidebar";
  const box = sidebar
    ? "-mt-2 flex flex-col gap-1 px-3.5 pb-3 text-xs text-ink-faint"
    : "flex min-h-[48px] flex-col justify-center gap-1 border-t border-[var(--line-soft)] px-3.5 py-2.5 text-md text-ink-dim";
  if (!link) {
    // The sidebar just leaves the line out; the Me page's card says what's going on.
    if (sidebar) return null;
    return <div className={box}>{loading && !error ? "Loading…" : `Couldn't load your ${name} link.`}</div>;
  }
  // No Jellyfin server connected and nothing linked: nothing to offer.
  if (!link.available && !link.linked) return null;
  const btn = "font-semibold";

  return (
    <div className={box}>
      <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <span>{sidebar ? `${name} account:` : `${name}:`}</span>
        {link.linked ? (
          <>
            <span className="max-w-[160px] truncate" style={{ color: "var(--ink)" }} title={link.jellyfin_username}>Linked{link.jellyfin_username ? ` as ${link.jellyfin_username}` : ""}</span>
            {link.can_unlink && <>· <button type="button" onClick={unlink} className={btn} style={{ color: "var(--ink-dim)" }}>Unlink</button></>}
          </>
        ) : (
          <button type="button" onClick={() => { setConflict(null); setSheet(true); }} className={btn} style={{ color: "var(--accent-text)" }}>Link</button>
        )}
      </div>
      {conflict && (
        <div role="status" style={{ color: "var(--avoid-text)" }}>
          Already linked to {conflict.already_linked_to}.
          {conflict.mergeable && conflict.user_id && user && (
            <> <button type="button" onClick={() => setMerging(conflict.user_id ?? null)} className={btn} style={{ color: "var(--accent-text)" }}>Move link here</button></>
          )}
        </div>
      )}
      {sheet && (
        <JellyfinSheet<LinkResult>
          title={`Link ${name}`}
          submitLabel="Link"
          quickConnect={link.quickconnect}
          password={jellyfinSignIn.link}
          qcStart={jellyfinSignIn.linkQCStart}
          qcPoll={jellyfinSignIn.linkQCPoll}
          onDone={done}
          onClose={() => setSheet(false)}
        />
      )}
      {merging !== null && user && (
        <Suspense fallback={null}>
          <MergeDialog
            targetId={user.id}
            fromId={merging}
            onClose={() => setMerging(null)}
            onMerged={() => { setMerging(null); setConflict(null); toast(`Merged — your ${name} user is linked here`, { tone: "good" }); void load(); }}
          />
        </Suspense>
      )}
    </div>
  );
}
