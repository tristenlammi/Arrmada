import { req, type AuthUser } from "./api";
import type { PlexMode, PlexPin } from "./plexSignIn";

// The Plex sign-in calls. They live here rather than in api.ts so they only download with
// the pages that use them (the login page's Plex button, Settings → Plex), not with every
// requester's first load.
const mode = (m: PlexMode) => (m === "redirect" ? "?mode=redirect" : "");

export const plexApi = {
  // Sign in with Plex on the login page. user is set once Plex has approved.
  loginStart: (m: PlexMode) => req<PlexPin>(`/api/v1/auth/plex/pin${mode(m)}`, { method: "POST" }),
  loginPoll: (id: number) => req<{ pending?: boolean; user?: AuthUser }>(`/api/v1/auth/plex/pin/${id}`),
  // Settings → Plex: connect the server (stores the token, finds the server).
  connectStart: (m: PlexMode) => req<PlexPin>(`/api/v1/insights/plex/auth${mode(m)}`, { method: "POST" }),
  connectPoll: (id: number) => req<PlexConnectResult>(`/api/v1/insights/plex/auth/${id}`),
};

// PlexConnectResult: authorized with server_name means connected; with choices, several of
// the owner's servers answered and they pick one; with neither, no server could be reached.
export interface PlexConnectResult {
  authorized: boolean;
  server_name?: string;
  choices?: { name: string; machine_id: string; url: string }[];
}
