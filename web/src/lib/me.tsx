import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type AuthUser } from "./api";

interface MeState {
  user: AuthUser | null;
  loading: boolean;
  external: boolean; // request came from outside the LAN and isn't staff → the limited shell
  // Module toggles (from /status) so nav + Discover can hide disabled modules live.
  booksEnabled: boolean;
  setBooksEnabled: (v: boolean) => void;
  musicEnabled: boolean;
  setMusicEnabled: (v: boolean) => void;
  // Whether a TMDB key is set. Movies, Series and Discover show one role-aware "not set up"
  // message instead of failing feed by feed; saving the key flips it back without a reload.
  metadataReady: boolean;
  setMetadataReady: (v: boolean) => void;
}

const MeContext = createContext<MeState>({ user: null, loading: true, external: false, booksEnabled: true, setBooksEnabled: () => {}, musicEnabled: false, setMusicEnabled: () => {}, metadataReady: true, setMetadataReady: () => {} });

// MeProvider fetches the current user and module toggles once at boot so the whole app can
// branch on role (staff get the full console; requesters get their own small shell) and
// hide modules an admin has turned off.
export function MeProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);
  const [external, setExternal] = useState(false);
  const [booksEnabled, setBooksEnabled] = useState(true);
  // Music is a preview and off by default, so don't flash its nav entry before /status lands.
  const [musicEnabled, setMusicEnabled] = useState(false);
  // Assume ready until /status says otherwise, so a server that predates the field (or a
  // failed /status) never hides working pages behind a "not set up" message.
  const [metadataReady, setMetadataReady] = useState(true);
  useEffect(() => {
    Promise.allSettled([api.me(), api.status()]).then(([me, status]) => {
      if (me.status === "fulfilled") setUser(me.value);
      if (status.status === "fulfilled") {
        setExternal(status.value.external);
        setBooksEnabled(status.value.books_enabled);
        setMusicEnabled(status.value.music_enabled);
        setMetadataReady(status.value.metadata_ready ?? true);
      }
      setLoading(false);
    });
  }, []);
  return <MeContext.Provider value={{ user, loading, external, booksEnabled, setBooksEnabled, musicEnabled, setMusicEnabled, metadataReady, setMetadataReady }}>{children}</MeContext.Provider>;
}

export function useMe(): MeState {
  return useContext(MeContext);
}

// isStaff reports whether the role can administer (manager or admin). Non-staff users
// get the requester shell (Discover, Calendar, Books, Audiobooks), never the console.
export function isStaff(user: AuthUser | null): boolean {
  return !!user && (user.role === "admin" || user.role === "manager");
}

export function isAdmin(user: AuthUser | null): boolean {
  return !!user && user.role === "admin";
}
