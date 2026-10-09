import { useMemo } from "react";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { SetupGate } from "./components/SetupGate";
import { Unreachable } from "./components/Unreachable";
import { useMe, isStaff } from "./lib/me";
import { buildRoutes } from "./lib/routes";
import type { UserRole } from "./lib/api";
import { Login } from "./pages/Login";

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

  if (loading) {
    return <div className="grid h-full place-items-center text-[13px] text-ink-dim">Loading…</div>;
  }

  // The server didn't answer at boot: say so (and keep retrying) rather than
  // showing a login form that can't work either.
  if (unreachable) {
    return <Unreachable />;
  }

  // Auth enabled + not signed in → login / first-run setup. signedOut: the session ended
  // while the app was open, so the screen says why.
  if (!user || !router) {
    return <Login signedOut={signedOut} />;
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
