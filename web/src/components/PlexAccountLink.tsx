import { lazy, Suspense, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ApiError } from "../lib/api";
import { useMe } from "../lib/me";
import { setQueryData, useQuery } from "../lib/query";
import { plexApi, type PlexLink, type PlexLinkConflict } from "../lib/plexApi";
import { usePlexPinSignIn, type PlexFlow } from "../lib/plexSignIn";
import { useConfirm, useToast } from "../ui";

// "Plex account: Link / Linked as <name> · Unlink" for whoever is signed in — the Me page
// (everyone) and the staff sidebar footer. Linking gives this account its Plex watch
// history for Discover's "Recommended for you", and makes Sign in with Plex land here. Its
// own chunk: requesters only download it when they open Me.
//
// A redirect-mode link (iPhone, the installed app) comes back to /me?plexlink=<id>, and
// the Me page's copy finishes it (the sidebar's never resumes, so a staff /me, which shows
// both, doesn't race itself). Both copies read one cached answer, so they agree.

const PlexMergeDialog = lazy(() => import("./PlexMergeDialog"));

type LinkResult = { linked: true; plex_username?: string } | { conflict: PlexLinkConflict };

const LINK_KEY = "me:plex";

export default function PlexAccountLink({ variant }: { variant: "sidebar" | "me" }) {
  const toast = useToast();
  const confirm = useConfirm();
  const { user } = useMe();
  const { data: link, loading, error } = useQuery<PlexLink>(LINK_KEY, plexApi.myPlex, { staleMs: 60_000 });
  const setLink = (l: PlexLink) => setQueryData(LINK_KEY, l);
  const [conflict, setConflict] = useState<PlexLinkConflict | null>(null);
  // Admins: "Move link here" merges the duplicate requester holding this Plex account.
  const [merging, setMerging] = useState<number | null>(null);
  const [, setParams] = useSearchParams();
  // A fresh read after linking or merging: the server words the name and can_unlink.
  const load = () => plexApi.myPlex().then(setLink).catch(() => {});

  const flow = useMemo<PlexFlow<LinkResult>>(() => ({
    kind: "link",
    start: plexApi.linkStart,
    poll: async (id) => {
      try {
        const r = await plexApi.linkPoll(id);
        return r.linked ? { linked: true, plex_username: r.plex_username } : null;
      } catch (e) {
        // Already linked to another account: an answer, not a failure.
        if (e instanceof ApiError && e.status === 409 && e.body?.already_linked_to) return { conflict: e.body as unknown as PlexLinkConflict };
        throw e;
      }
    },
  }), []);
  const plex = usePlexPinSignIn({
    flow,
    onDone: (r) => {
      if ("conflict" in r) { setConflict(r.conflict); return; }
      setConflict(null);
      toast(`Linked to Plex${r.plex_username ? ` as ${r.plex_username}` : ""}`, { tone: "good" });
      void load();
    },
    onError: (m) => toast(m, { tone: "error" }),
    resumeParam: variant === "me" ? "plexlink" : undefined,
    strip: () => setParams((p) => { p.delete("plexlink"); return p; }, { replace: true }),
  });

  const unlink = async () => {
    const ok = await confirm({
      title: "Unlink your Plex account?",
      body: "Sign in with Plex won't open this account any more, and Discover stops using your Plex watch history. You can link it again any time.",
      confirmLabel: "Unlink",
    });
    if (!ok) return;
    try { setLink(await plexApi.unlink()); toast("Plex unlinked", { tone: "good" }); }
    catch (e) { toast((e as Error).message, { tone: "error" }); }
  };

  const sidebar = variant === "sidebar";
  const box = sidebar ? "flex flex-col gap-1 px-3.5 pb-3 text-[11px] text-ink-faint" : "flex min-h-[48px] flex-col justify-center gap-1 px-3.5 py-2.5 text-[13px] text-ink-dim";
  if (!link) {
    // The sidebar just leaves the line out; the Me page's card says what's going on.
    if (sidebar) return null;
    return <div className={box}>{loading && !error ? "Loading…" : "Couldn't load your Plex link."}</div>;
  }
  const busy = plex.phase !== "idle";
  const btn = "font-semibold";

  return (
    <div className={box}>
      <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <span>Plex account:</span>
        {busy ? (
          <span>{plex.phase === "finishing" ? "finishing…" : "waiting for Plex…"}</span>
        ) : link.linked ? (
          <>
            <span className="max-w-[140px] truncate" style={{ color: "var(--ink)" }} title={link.plex_username}>Linked{link.plex_username ? ` as ${link.plex_username}` : ""}</span>
            {link.can_unlink && <>· <button type="button" onClick={unlink} className={btn} style={{ color: "var(--ink-dim)" }}>Unlink</button></>}
          </>
        ) : (
          <button type="button" onClick={() => { setConflict(null); plex.begin(); }} className={btn} style={{ color: "var(--accent)" }}>Link</button>
        )}
      </div>
      {(plex.phase === "waiting" || plex.phase === "slow") && (
        <div className="flex flex-wrap gap-x-2">
          {plex.phase === "slow" && <button type="button" onClick={plex.continueHere} className={btn} style={{ color: "var(--accent)" }}>Plex window didn't open? Continue in this tab</button>}
          <button type="button" onClick={plex.cancel} className="underline">Cancel</button>
        </div>
      )}
      {conflict && (
        <div role="status" style={{ color: "var(--avoid)" }}>
          Already linked to {conflict.already_linked_to}.
          {conflict.mergeable && conflict.user_id && user && (
            <> <button type="button" onClick={() => setMerging(conflict.user_id ?? null)} className={btn} style={{ color: "var(--accent)" }}>Move link here</button></>
          )}
        </div>
      )}
      {merging !== null && user && (
        <Suspense fallback={null}>
          <PlexMergeDialog
            targetId={user.id}
            fromId={merging}
            onClose={() => setMerging(null)}
            onMerged={() => { setMerging(null); setConflict(null); toast("Merged — your Plex account is linked here", { tone: "good" }); void load(); }}
          />
        </Suspense>
      )}
    </div>
  );
}
