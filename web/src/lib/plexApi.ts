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
  // Your own account's Plex link.
  myPlex: () => req<PlexLink>("/api/v1/me/plex"),
  linkStart: (m: PlexMode) => req<PlexPin>(`/api/v1/me/plex/link${mode(m)}`, { method: "POST" }),
  linkPoll: (id: number) => req<{ pending?: boolean; linked?: boolean; plex_username?: string }>(`/api/v1/me/plex/link/${id}`),
  unlink: () => req<PlexLink>("/api/v1/me/plex/link", { method: "DELETE" }),
  // Admin: merge a duplicate Plex requester (from) into the account it belongs with.
  mergePreview: (target: number, from: number) => req<PlexMergePreview>(`/api/v1/users/${target}/plex/merge?from=${from}`),
  merge: (target: number, from: number) =>
    req<{ merged: boolean }>(`/api/v1/users/${target}/plex/merge`, { method: "POST", body: JSON.stringify({ from_user_id: from }) }),
};

// PlexMergePreview is what a merge moves, as counts. Audiobook data is only yes/no: admins
// see how much people listen, never what.
export interface PlexMergePreview {
  from: string;
  to: string;
  plex_username?: string;
  requests: number;
  following: number;
  notifications: number;
  push_devices: number;
  quota_usage: number;
  audiobook_progress: boolean;
  audiobook_password: boolean;
  signed_in_devices: number;
  listening_apps: number;
}

export interface PlexLink {
  linked: boolean;
  plex_username?: string;
  /** The account has a password, so unlinking Plex leaves a way in. */
  can_unlink: boolean;
}

// PlexLinkConflict is the 409 when the Plex account belongs to another account. user_id
// and mergeable come to admins only, for a requester's link they may move here.
export interface PlexLinkConflict {
  already_linked_to: string;
  user_id?: number;
  mergeable?: boolean;
}

// PlexConnectResult: authorized with server_name means connected; with choices, several of
// the owner's servers answered and they pick one; with neither, no server could be reached.
export interface PlexConnectResult {
  authorized: boolean;
  server_name?: string;
  choices?: { name: string; machine_id: string; url: string }[];
}
