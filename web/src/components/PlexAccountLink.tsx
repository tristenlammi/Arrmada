import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ApiError } from "../lib/api";
import { useMe } from "../lib/me";
import { plexApi, type PlexLink, type PlexLinkConflict } from "../lib/plexApi";
import { usePlexPinSignIn, type PlexFlow } from "../lib/plexSignIn";
import { useConfirm, useToast } from "../ui";

// "Plex account: Link / Linked as <name> · Unlink" for whoever is signed in — the staff
// sidebar footer and the requester's account menu (it moves to the Me page once that
// exists). Linking gives this account its Plex watch history for Discover's "Recommended
// for you", and makes Sign in with Plex land here. Its own chunk: requesters only download
// it when they open the menu.
//
// A redirect-mode link (iPhone, the installed app) comes back to /discover?plexlink=<id>;
// whichever copy of this is mounted then finishes it.

const PlexMergeDialog = lazy(() => import("./PlexMergeDialog"));

type LinkResult = { linked: true; plex_username?: string } | { conflict: PlexLinkConflict };

export default function PlexAccountLink({ variant }: { variant: "sidebar" | "menu" }) {
  const toast = useToast();
  const confirm = useConfirm();
  const { user } = useMe();
  const [link, setLink] = useState<PlexLink | null>(null);
  const [conflict, setConflict] = useState<PlexLinkConflict | null>(null);
  // Admins: "Move link here" merges the duplicate requester holding this Plex account.
  const [merging, setMerging] = useState<number | null>(null);
  const [, setParams] = useSearchParams();
  const load = useCallback(() => plexApi.myPlex().then(setLink).catch(() => setLink(null)), []);
  useEffect(() => { void load(); }, [load]);

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
    resumeParam: "plexlink",
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

  if (!link) return null;
  const busy = plex.phase !== "idle";
  const btn = "font-semibold";
  const sidebar = variant === "sidebar";

  return (
    <div className={sidebar ? "flex flex-col gap-1 px-3.5 pb-3 text-[11px] text-ink-faint" : "flex flex-col gap-1 px-2.5 py-1.5 text-[12px] text-ink-dim"}>
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
