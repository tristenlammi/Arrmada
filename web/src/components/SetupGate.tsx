import { Suspense, useEffect, useState } from "react";
import { api, type AuthUser } from "../lib/api";
import { lazyPage } from "../lib/lazyPage";

// Only an admin on a fresh install ever sees the wizard, so it is its own chunk.
const SetupWizard = lazyPage(() => import("../pages/SetupWizard"), "SetupWizard");

// SetupGate shows the first-run wizard to an admin until setup is finished or skipped.
// Everyone else, and an admin on a configured install, goes straight to the app. A
// failed check never blocks the app — it just shows the app.
export function SetupGate({ user, children }: { user: AuthUser; children: React.ReactNode }) {
  const [needed, setNeeded] = useState<boolean | null>(user.role === "admin" ? null : false);

  useEffect(() => {
    if (user.role !== "admin") return;
    api.setupState().then((s) => setNeeded(s.needed)).catch(() => setNeeded(false));
  }, [user.role]);

  if (needed === null) {
    return <div className="grid h-full place-items-center text-[13px] text-ink-dim">Loading…</div>;
  }
  if (needed) {
    return (
      <Suspense fallback={<div className="grid h-full place-items-center text-[13px] text-ink-dim">Loading…</div>}>
        <SetupWizard onDone={() => setNeeded(false)} />
      </Suspense>
    );
  }
  return <>{children}</>;
}
