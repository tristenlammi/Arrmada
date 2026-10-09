import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, ApiError, SIGNED_OUT_EVENT, type AuthUser } from "./api";
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
  // The boot calls got no answer from Arrmada (network down, server restarting, a
  // proxy's 502), so App shows "Can't reach Arrmada" rather than the login form.
  unreachable: boolean;
  // retry re-runs the boot calls and resolves true once Arrmada answered.
  retry: () => Promise<boolean>;
}

const MeContext = createContext<MeState>({ user: null, loading: true, signedOut: false, external: false, booksEnabled: true, setBooksEnabled: () => {}, musicEnabled: false, setMusicEnabled: () => {}, unreachable: false, retry: async () => false });

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
  const [unreachable, setUnreachable] = useState(false);
  const boot = useCallback(async (): Promise<boolean> => {
    const [me, status] = await Promise.allSettled([api.me(), api.status()]);
    if (bootUnreachable(me, status)) {
      setUnreachable(true);
      setLoading(false);
      return false;
    }
    setUser(me.status === "fulfilled" ? me.value : null);
    if (status.status === "fulfilled") {
      setExternal(status.value.external);
      setBooksEnabled(status.value.books_enabled);
      setMusicEnabled(status.value.music_enabled);
    }
    setUnreachable(false);
    setLoading(false);
    return true;
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
  useEffect(() => {
    void boot();
  }, [boot]);
  return <MeContext.Provider value={{ user, loading, signedOut, external, booksEnabled, setBooksEnabled, musicEnabled, setMusicEnabled, unreachable, retry: boot }}>{children}</MeContext.Provider>;
}

// bootUnreachable decides between "show Login" and "Can't reach Arrmada". A 401 or
// 403 from /me is Arrmada answering "not signed in", so Login is right. Anything
// else (a fetch TypeError, a 5xx, a proxy's 502 or HTML error page) means no real
// answer came back; /status is public, so any failure there counts the same way.
export function bootUnreachable(me: PromiseSettledResult<unknown>, status: PromiseSettledResult<unknown>): boolean {
  if (status.status === "rejected") return true;
  return me.status === "rejected" && !(me.reason instanceof ApiError && (me.reason.status === 401 || me.reason.status === 403));
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
