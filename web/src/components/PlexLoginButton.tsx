import { useEffect, useMemo } from "react";
import type { AuthUser } from "../lib/api";
import { plexApi } from "../lib/plexApi";
import { usePlexPinSignIn, type PlexFlow } from "../lib/plexSignIn";
import { keepNextAcrossRedirect } from "../lib/session";

// The login page's "Sign in with Plex" button. Its own chunk: most visits to the login
// page are password sign-ins, and a requester's first download is tight.
//
// On a computer it opens plex.tv in a popup; on an iPhone, in the installed app or with
// popups blocked it sends the page to plex.tv, which sends it back with ?plexpin=, and
// this button (mounted again on that load) finishes the sign-in.
export default function PlexLoginButton({ disabled, onBusy, onError, onSignedIn }: {
  disabled: boolean;
  onBusy: (busy: boolean) => void;
  onError: (msg: string | null) => void;
  onSignedIn: () => void;
}) {
  const flow = useMemo<PlexFlow<AuthUser>>(() => ({
    kind: "login",
    start: plexApi.loginStart,
    poll: async (id) => (await plexApi.loginPoll(id)).user ?? null,
    beforeRedirect: keepNextAcrossRedirect,
  }), []);
  const { phase, begin, cancel, continueHere } = usePlexPinSignIn({ flow, onDone: onSignedIn, onError, resumeParam: "plexpin" });
  const busy = phase !== "idle";
  useEffect(() => onBusy(busy), [busy, onBusy]);

  return (
    <>
      <button
        type="button"
        onClick={() => { onError(null); begin(); }}
        disabled={disabled || busy}
        className="rounded-lg px-4 py-2.5 text-[13px] font-semibold"
        style={{ background: "#e5a00d", color: "#1f1300" }}
      >
        {phase === "finishing" ? "Finishing Plex sign-in…" : busy ? "Waiting for Plex…" : "Sign in with Plex"}
      </button>
      {(phase === "waiting" || phase === "slow") && (
        <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11.5px]">
          {phase === "slow" && (
            <button type="button" onClick={continueHere} className="font-semibold" style={{ color: "var(--accent)" }}>
              Plex window didn't open? Continue in this tab
            </button>
          )}
          <button type="button" onClick={cancel} className="text-ink-faint underline">Cancel</button>
        </div>
      )}
    </>
  );
}
