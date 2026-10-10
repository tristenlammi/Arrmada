import { Suspense, useEffect, useMemo } from "react";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { SetupGate } from "./components/SetupGate";
import { Unreachable } from "./components/Unreachable";
import { useMe, isStaff } from "./lib/me";
import { buildRoutes } from "./lib/routes";
import { lazyPage } from "./lib/lazyPage";
import type { UserRole } from "./lib/api";

// The sign-in screen is only for someone signed out, so a signed-in phone never
// downloads it along with Discover.
const Login = lazyPage(() => import("./pages/Login"), "Login");
const spinner = <div className="grid h-full place-items-center text-[13px] text-ink-dim">Loading…</div>;

// One router per session shape. Signing out and back in as someone else rebuilds it;
// the previous one is disposed so it stops listening to the browser's history.
let current: { key: string; router: ReturnType<typeof createBrowserRouter> } | null = null;

function routerFor(role: UserRole, external: boolean) {
  const key = `${role}|${external}`;
  if (current?.key !== key) {
    current?.router.dispose();
    current = { key, router: createBrowserRouter(buildRoutes({ role, external })) };
  }
  return current.router;
}

// Staff get the whole console; requesters and outside visitors get their own small shell.
export default function App() {
  const { user, loading, signedOut, unreachable, external } = useMe();
  const role = user?.role;
  const router = useMemo(() => (role ? routerFor(role, external) : null), [role, external]);
  // Signed out mid-session: the sign-in screen shouldn't keep the last page's tab title.
  useEffect(() => {
    if (!user) document.title = "Arrmada";
  }, [user]);

  if (loading) {
    return spinner;
  }

  // The server didn't answer at boot: say so (and keep retrying) rather than
  // showing a login form that can't work either.
  if (unreachable) {
    return <Unreachable />;
  }

  // Auth enabled + not signed in → login / first-run setup. signedOut: the session ended
  // while the app was open, so the screen says why.
  if (!user || !router) {
    return (
      <Suspense fallback={spinner}>
        <Login signedOut={signedOut} />
      </Suspense>
    );
  }

  // Admins see the first-run wizard before the console until setup is done or skipped.
  if (isStaff(user)) {
    return (
      <SetupGate user={user}>
        <RouterProvider router={router} />
      </SetupGate>
    );
  }
  return <RouterProvider router={router} />;
}
