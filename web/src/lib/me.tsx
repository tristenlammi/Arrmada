import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api, SIGNED_OUT_EVENT, type AuthUser } from "./api";
import { rememberNext } from "./session";

interface MeState {
  user: AuthUser | null;
  loading: boolean;
  // The session ended while the app was open; the sign-in screen says so.
  signedOut: boolean;
  external: boolean; // request came from outside the LAN → Discover-only
  // Module toggles (from /status) so nav + Discover can hide disabled modules live.
  booksEnabled: boolean;
  setBooksEnabled: (v: boolean) => void;
  musicEnabled: boolean;
  setMusicEnabled: (v: boolean) => void;
}

const MeContext = createContext<MeState>({ user: null, loading: true, signedOut: false, external: false, booksEnabled: true, setBooksEnabled: () => {}, musicEnabled: false, setMusicEnabled: () => {} });

// MeProvider fetches the current user and module toggles once at boot so the whole app can
// branch on role (staff get the full console; requesters get the Discover-only shell) and
// hide modules an admin has turned off.
export function MeProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);
  const [external, setExternal] = useState(false);
  const [booksEnabled, setBooksEnabled] = useState(true);
  // Music is a preview and off by default, so don't flash its nav entry before /status lands.
  const [musicEnabled, setMusicEnabled] = useState(false);
  useEffect(() => {
    Promise.allSettled([api.me(), api.status()]).then(([me, status]) => {
      if (me.status === "fulfilled") setUser(me.value);
      if (status.status === "fulfilled") {
        setExternal(status.value.external);
        setBooksEnabled(status.value.books_enabled);
        setMusicEnabled(status.value.music_enabled);
      }
      setLoading(false);
    });
  }, []);
  // A session that ends mid-use (expired, revoked, password changed, account turned off)
  // swaps the whole app for the sign-in screen, remembering the page to come back to.
  // The layouts unmount, so their polls and the live socket stop with them.
  const [signedOut, setSignedOut] = useState(false);
  useEffect(() => {
    const onSignedOut = () => {
      rememberNext();
      setUser(null);
      setSignedOut(true);
    };
    window.addEventListener(SIGNED_OUT_EVENT, onSignedOut);
    return () => window.removeEventListener(SIGNED_OUT_EVENT, onSignedOut);
  }, []);
  return <MeContext.Provider value={{ user, loading, signedOut, external, booksEnabled, setBooksEnabled, musicEnabled, setMusicEnabled }}>{children}</MeContext.Provider>;
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
