import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type AuthUser } from "./api";

interface MeState {
  user: AuthUser | null;
  loading: boolean;
  // Module toggles (from /status) so nav + Discover can hide disabled modules live.
  booksEnabled: boolean;
  setBooksEnabled: (v: boolean) => void;
  musicEnabled: boolean;
  setMusicEnabled: (v: boolean) => void;
}

const MeContext = createContext<MeState>({ user: null, loading: true, booksEnabled: true, setBooksEnabled: () => {}, musicEnabled: true, setMusicEnabled: () => {} });

// MeProvider fetches the current user and module toggles once at boot so the whole app can
// branch on role (staff get the full console; requesters get the Discover-only shell) and
// hide modules an admin has turned off.
export function MeProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);
  const [booksEnabled, setBooksEnabled] = useState(true);
  const [musicEnabled, setMusicEnabled] = useState(true);
  useEffect(() => {
    Promise.allSettled([api.me(), api.status()]).then(([me, status]) => {
      if (me.status === "fulfilled") setUser(me.value);
      if (status.status === "fulfilled") {
        setBooksEnabled(status.value.books_enabled);
        setMusicEnabled(status.value.music_enabled);
      }
      setLoading(false);
    });
  }, []);
  return <MeContext.Provider value={{ user, loading, booksEnabled, setBooksEnabled, musicEnabled, setMusicEnabled }}>{children}</MeContext.Provider>;
}

export function useMe(): MeState {
  return useContext(MeContext);
}

// isStaff reports whether the role can administer (manager or admin). Non-staff users
// only ever see the Discover experience.
export function isStaff(user: AuthUser | null): boolean {
  return !!user && (user.role === "admin" || user.role === "manager");
}

export function isAdmin(user: AuthUser | null): boolean {
  return !!user && user.role === "admin";
}
