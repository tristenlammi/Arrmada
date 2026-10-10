import { req } from "./api";

// The Me page's own calls: notification choices, and (below) your password and signed-in
// devices. They live here rather than in api.ts so they download with the Me page only,
// not with every requester's first load.

/** Which notices reach your phones and Apprise link; the inbox keeps them all. */
export interface NotifyPrefs {
  approved: boolean;
  declined: boolean;
  ready: boolean;
  /** Staff: someone requested something. */
  new_request: boolean;
}

export const accountApi = {
  notifyPrefs: () => req<NotifyPrefs>("/api/v1/me/notify-prefs"),
  /** Changes only the keys given. */
  setNotifyPrefs: (change: Partial<NotifyPrefs>) =>
    req<NotifyPrefs>("/api/v1/me/notify-prefs", { method: "PUT", body: JSON.stringify(change) }),

  /** password_set: somebody knows a password for this account (false: Plex sign-in only). */
  account: () => req<{ password_set: boolean }>("/api/v1/me/account"),
  /** current is ignored when the account has no password yet. signed_out: other devices ended. */
  changePassword: (current: string, next: string) =>
    req<{ password_set: boolean; signed_out: number }>("/api/v1/me/password", { method: "POST", body: JSON.stringify({ current, new: next }) }),
  /** Signs out every other browser; this one stays. */
  signOutOthers: () => req<{ signed_out: number }>("/api/v1/me/sessions/revoke-others", { method: "POST" }),
  /** Where you're signed in (your own sessions only). */
  sessions: () => req<{ sessions: MySession[] }>("/api/v1/me/sessions").then((r) => r.sessions ?? []),
  signOutSession: (id: string) => req<void>(`/api/v1/me/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }),
  /** Admin: sign someone out of every browser. Answers a count, never their devices. */
  signOutEverywhere: (userId: number) => req<{ signed_out: number }>(`/api/v1/users/${userId}/sessions/revoke`, { method: "POST" }),
};

/** One browser you're signed in on. */
export interface MySession {
  id: string;
  created_at: string;
  last_seen_at: string;
  /** "Chrome on Windows", "Home Screen app on iPhone"… */
  device: string;
  /** "local" (your home network), a coarse address like "203.0.113.x", or "". */
  network: string;
  current: boolean;
}
