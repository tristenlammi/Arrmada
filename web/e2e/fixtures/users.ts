import type { AuthUser } from "../../src/lib/api";

// The signed-in people the specs can be. "external" is a requester signed in from
// outside the LAN: the server flags the session external and the app shows the
// smaller outside shell.
export type Persona = "admin" | "manager" | "requester" | "external";

export interface PersonaInfo {
  user: AuthUser;
  external: boolean;
}

const created_at = "2026-01-05T10:00:00Z";

export const personas: Record<Persona, PersonaInfo> = {
  admin: { user: { id: 1, username: "admiral", role: "admin", auto_approve: true, created_at }, external: false },
  manager: { user: { id: 2, username: "quartermaster", role: "manager", auto_approve: true, created_at }, external: false },
  requester: { user: { id: 3, username: "deckhand", role: "requester", auto_approve: false, created_at }, external: false },
  external: { user: { id: 4, username: "visitor", role: "requester", auto_approve: false, created_at }, external: true },
};
